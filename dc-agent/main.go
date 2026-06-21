// Command dc-agent is the regional agent for the DC control plane.
//
// One dc-agent runs in each region (per zone, eventually) and dials OUT to
// the control plane over WebSocket-over-TLS on 443 — nothing connects into
// the datacenter. The region's infrastructure credentials (Harvester
// kubeconfig, Rancher token) stay local; the only credential that travels is
// the agent's own bearer token.
//
// The binary maintains the channel (protocol v0: hello/hello_ack + ping/pong
// keepalive, reconnect with backoff) and serves protocol v1 operation verbs
// against the local cluster when it has access to one. Today that is the
// read-only get_inventory; the mutating verbs (apply/delete) follow. When the
// agent has no local-cluster access it still runs — presence-only.
//
// main.go has ONE responsibility: start the program.
//  1. Load configuration from DCAGENT_* environment variables (fail fast).
//  2. Set up logging, the local-cluster executor, and signal handling.
//  3. Run the connection loop until SIGINT/SIGTERM.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/rs/zerolog"

	"github.com/wso2/dc-agent/internal/conn"
	"github.com/wso2/dc-agent/internal/executor"
)

// version is the agent build version, reported to the control plane in the
// hello frame. Overridden at build time:
//
//	go build -ldflags "-X main.version=v0.1.0"
var version = "dev"

// tokenPrefix is the fixed prefix of agent tokens minted by the control
// plane (POST /v1/admin/regions). Validated at startup so a pasted-wrong
// credential fails immediately instead of as a cryptic 401 loop.
const tokenPrefix = "dcagent_"

// config holds the agent's runtime configuration, sourced from DCAGENT_*
// environment variables (12-factor, same convention as dc-api's DCAPI_*).
type config struct {
	endpoint string // DCAGENT_ENDPOINT  e.g. wss://controlplane.example.com/v1/agent/ws
	token    string // DCAGENT_TOKEN     bearer token, "dcagent_…"
	region   string // DCAGENT_REGION    region identifier, e.g. "lk"
	zone     string // DCAGENT_ZONE      zone within the region, e.g. "zone-1"
	logLevel string // DCAGENT_LOG_LEVEL trace|debug|info|warn|error (default info)
	// kubeconfig (DCAGENT_KUBECONFIG) is optional: empty uses the in-cluster
	// ServiceAccount (production); a path loads that kubeconfig (local dev, e.g.
	// the agent on a laptop pointed at Harvester). No cluster access → the agent
	// runs presence-only (no get_inventory).
	kubeconfig string
}

// loadConfig reads and validates all DCAGENT_* variables, returning every
// problem at once so a misconfigured deployment is fixed in one iteration.
func loadConfig() (config, error) {
	cfg := config{
		endpoint:   os.Getenv("DCAGENT_ENDPOINT"),
		token:      os.Getenv("DCAGENT_TOKEN"),
		region:     os.Getenv("DCAGENT_REGION"),
		zone:       os.Getenv("DCAGENT_ZONE"),
		logLevel:   os.Getenv("DCAGENT_LOG_LEVEL"),
		kubeconfig: os.Getenv("DCAGENT_KUBECONFIG"),
	}
	if cfg.logLevel == "" {
		cfg.logLevel = "info"
	}

	var problems []string

	switch {
	case cfg.endpoint == "":
		problems = append(problems, "DCAGENT_ENDPOINT is required (e.g. wss://controlplane.example.com/v1/agent/ws)")
	default:
		u, err := url.Parse(cfg.endpoint)
		if err != nil {
			problems = append(problems, fmt.Sprintf("DCAGENT_ENDPOINT is not a valid URL: %v", err))
		} else if u.Scheme != "wss" && u.Scheme != "ws" {
			problems = append(problems, fmt.Sprintf("DCAGENT_ENDPOINT scheme must be wss (or ws for local dev), got %q", u.Scheme))
		}
	}

	switch {
	case cfg.token == "":
		problems = append(problems, "DCAGENT_TOKEN is required")
	case !strings.HasPrefix(cfg.token, tokenPrefix):
		problems = append(problems, fmt.Sprintf("DCAGENT_TOKEN must start with %q (control-plane-minted agent token)", tokenPrefix))
	}

	if cfg.region == "" {
		problems = append(problems, "DCAGENT_REGION is required (e.g. lk)")
	}
	if cfg.zone == "" {
		// Zones are first-class from day one (region → zone model); an
		// implicit default would bake single-zone assumptions into deployments.
		problems = append(problems, "DCAGENT_ZONE is required (e.g. zone-1)")
	}

	if _, err := zerolog.ParseLevel(cfg.logLevel); err != nil {
		problems = append(problems, fmt.Sprintf("DCAGENT_LOG_LEVEL %q is invalid: %v", cfg.logLevel, err))
	}

	if len(problems) > 0 {
		return config{}, fmt.Errorf("invalid configuration:\n  - %s", strings.Join(problems, "\n  - "))
	}
	return cfg, nil
}

func main() {
	// ── Configuration ─────────────────────────────────────────────────────────
	cfg, err := loadConfig()
	if err != nil {
		l := zerolog.New(os.Stdout).With().Timestamp().Logger()
		l.Fatal().Err(err).Msg("failed to load configuration — check DCAGENT_* environment variables")
	}

	// ── Logging ───────────────────────────────────────────────────────────────
	level, _ := zerolog.ParseLevel(cfg.logLevel) // validated in loadConfig
	zerolog.SetGlobalLevel(level)
	logger := zerolog.New(os.Stdout).With().Timestamp().Logger()
	logger.Info().
		Str("version", version).
		Str("region", cfg.region).
		Str("zone", cfg.zone).
		Str("endpoint", cfg.endpoint).
		Str("log_level", level.String()).
		Msg("dc-agent starting")

	// ── Signal-driven shutdown ────────────────────────────────────────────────
	// SIGTERM is how Kubernetes asks a pod to stop; SIGINT covers Ctrl-C in
	// local runs. Cancelling the context unwinds the connection loop, which
	// sends a normal WebSocket close to the control plane.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// ── Local-cluster executor (optional) ─────────────────────────────────────
	// The executor reads the zone's own cluster. Building it lazily creates the
	// clients; a real call happens only when the control plane requests an op.
	// No cluster access is not fatal — the agent runs presence-only, exactly as
	// before v1, and advertises no ops (the server returns OP_UNSUPPORTED).
	var (
		dispatcher       conn.Dispatcher
		streamDispatcher conn.StreamDispatcher
	)
	if exec, err := executor.NewKubeExecutorFromConfig(cfg.kubeconfig); err != nil {
		logger.Warn().Err(err).Msg("no local-cluster access; running presence-only (set DCAGENT_KUBECONFIG for local dev)")
	} else {
		dispatcher, streamDispatcher = buildDispatchers(exec, logger)
		logger.Info().
			Strs("ops", []string{executor.OpGetInventory, executor.OpApply, executor.OpDelete, executor.OpGetStatus, executor.OpWatchStatus}).
			Msg("local-cluster executor ready")
	}

	// ── Connection loop ───────────────────────────────────────────────────────
	// Runs until the context is cancelled, reconnecting on any failure.
	runner := conn.NewRunner(conn.Config{
		Endpoint:         cfg.endpoint,
		Token:            cfg.token,
		Region:           cfg.region,
		Zone:             cfg.zone,
		Version:          version,
		Logger:           logger,
		Dispatcher:       dispatcher,
		StreamDispatcher: streamDispatcher,
	})
	runner.Run(ctx)

	logger.Info().Msg("dc-agent stopped")
}

// buildDispatchers wires the protocol-v1 op handlers onto exec. Each
// request/response handler unmarshals its params, calls the matching executor
// method, and marshals the result: a returned error becomes an EXEC_ERROR res,
// while a conn.BadRequest-wrapped error (params-unmarshal failure) or an executor
// BAD_REQUEST fault becomes a BAD_REQUEST res. The streaming watch_status handler
// emits one progress frame per observed event before its terminal summary.
//
// It is a package-level function (not inline in main) so the wire path — params
// field names, result shapes, error mapping — is unit-testable against an
// executor.Stub driven through the real conn loop.
func buildDispatchers(exec executor.Executor, logger zerolog.Logger) (conn.Dispatcher, conn.StreamDispatcher) {
	dispatcher := conn.Dispatcher{
		executor.OpGetInventory: func(ctx context.Context, _ json.RawMessage) (json.RawMessage, error) {
			inv, err := exec.GetInventory(ctx)
			if err != nil {
				return nil, err
			}
			return json.Marshal(inv)
		},
		executor.OpApply: func(ctx context.Context, params json.RawMessage) (json.RawMessage, error) {
			var p struct {
				Manifest     json.RawMessage `json:"manifest"`
				FieldManager string          `json:"field_manager"`
				Force        bool            `json:"force"`
			}
			if err := json.Unmarshal(params, &p); err != nil {
				return nil, conn.BadRequest(fmt.Errorf("apply params: %w", err))
			}
			res, err := exec.Apply(ctx, p.Manifest, p.FieldManager, p.Force)
			if err != nil {
				return nil, err
			}
			logger.Info().
				Str("op", executor.OpApply).
				Str("gvk", res.APIVersion+"/"+res.Kind).
				Str("object", res.Namespace+"/"+res.Name).
				Str("resource_version", res.ResourceVersion).
				Msg("applied object")
			return json.Marshal(res)
		},
		executor.OpDelete: func(ctx context.Context, params json.RawMessage) (json.RawMessage, error) {
			var p struct {
				executor.ResourceRef
				PropagationPolicy string `json:"propagation_policy"`
			}
			if err := json.Unmarshal(params, &p); err != nil {
				return nil, conn.BadRequest(fmt.Errorf("delete params: %w", err))
			}
			res, err := exec.Delete(ctx, p.ResourceRef, p.PropagationPolicy)
			if err != nil {
				return nil, err
			}
			logger.Info().
				Str("op", executor.OpDelete).
				Str("gvk", p.APIVersion+"/"+p.Kind).
				Str("object", p.Namespace+"/"+p.Name).
				Bool("existed", res.Existed).
				Msg("deleted object")
			return json.Marshal(res)
		},
		executor.OpGetStatus: func(ctx context.Context, params json.RawMessage) (json.RawMessage, error) {
			var ref executor.ResourceRef
			if err := json.Unmarshal(params, &ref); err != nil {
				return nil, conn.BadRequest(fmt.Errorf("get_status params: %w", err))
			}
			res, err := exec.GetStatus(ctx, ref)
			if err != nil {
				return nil, err
			}
			return json.Marshal(res)
		},
	}
	streamDispatcher := conn.StreamDispatcher{
		executor.OpWatchStatus: func(ctx context.Context, params json.RawMessage, emit conn.Emitter) (json.RawMessage, error) {
			var p struct {
				executor.ResourceRef
				MaxSnapshots int `json:"max_snapshots"`
			}
			if err := json.Unmarshal(params, &p); err != nil {
				return nil, conn.BadRequest(fmt.Errorf("watch_status params: %w", err))
			}
			res, err := exec.WatchStatus(ctx, p.ResourceRef, p.MaxSnapshots, func(stage string, snap executor.StatusSnapshot) {
				b, err := json.Marshal(snap)
				if err != nil {
					return // a snapshot that won't marshal is dropped, not fatal
				}
				emit(stage, b)
			})
			if err != nil {
				return nil, err
			}
			logger.Info().
				Str("op", executor.OpWatchStatus).
				Str("gvk", p.APIVersion+"/"+p.Kind).
				Str("object", p.Namespace+"/"+p.Name).
				Int("snapshots_sent", res.SnapshotsSent).
				Str("reason", res.Reason).
				Msg("watch_status stream ended")
			return json.Marshal(res)
		},
	}
	return dispatcher, streamDispatcher
}
