// Command dc-agent is the regional agent for the DC control plane.
//
// One dc-agent runs in each region (per zone, eventually) and dials OUT to
// the control plane over WebSocket-over-TLS on 443 — nothing connects into
// the datacenter. The region's infrastructure credentials (Harvester
// kubeconfig, Rancher token) stay local; the only credential that travels is
// the agent's own bearer token.
//
// This binary is comms-only today: it establishes and maintains the channel
// (protocol v0: hello/hello_ack + ping/pong keepalive, reconnect with
// backoff). Executing desired-state operations against local
// Harvester/Rancher/KubeOVN — and therefore a Kubernetes client dependency —
// is a later phase; see docs/multi-region.md and the protocol package doc.
//
// main.go has ONE responsibility: start the program.
//  1. Load configuration from DCAGENT_* environment variables (fail fast).
//  2. Set up logging and signal handling.
//  3. Run the connection loop until SIGINT/SIGTERM.
package main

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/rs/zerolog"

	"github.com/wso2/dc-agent/internal/conn"
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
}

// loadConfig reads and validates all DCAGENT_* variables, returning every
// problem at once so a misconfigured deployment is fixed in one iteration.
func loadConfig() (config, error) {
	cfg := config{
		endpoint: os.Getenv("DCAGENT_ENDPOINT"),
		token:    os.Getenv("DCAGENT_TOKEN"),
		region:   os.Getenv("DCAGENT_REGION"),
		zone:     os.Getenv("DCAGENT_ZONE"),
		logLevel: os.Getenv("DCAGENT_LOG_LEVEL"),
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

	// ── Connection loop ───────────────────────────────────────────────────────
	// Runs until the context is cancelled, reconnecting on any failure.
	runner := conn.NewRunner(conn.Config{
		Endpoint: cfg.endpoint,
		Token:    cfg.token,
		Region:   cfg.region,
		Zone:     cfg.zone,
		Version:  version,
		Logger:   logger,
	})
	runner.Run(ctx)

	logger.Info().Msg("dc-agent stopped")
}
