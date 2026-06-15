// Command webhook runs the DC-API mutating admission webhook for KubeVirt
// VirtualMachine resources.
//
// Purpose:
//   When Rancher provisions RKE2 cluster node VMs via HarvesterConfig, the
//   resulting VirtualMachine CRD does not carry the OVN-specific annotations
//   that KubeOVN's CNI needs to allocate the same MAC address for the OVN
//   logical switch port (LSP).  This webhook injects those annotations before
//   the VM object reaches the apiserver, ensuring MAC alignment across the
//   VM vNIC, the virt-launcher pod NIC, and the OVN LSP — which is the
//   prerequisite for L2 return-path delivery in bridge mode.
//
// Environment variables (all prefixed DCWEBHOOK_):
//
//	DCWEBHOOK_LISTEN_ADDR   HTTPS listen address (default: :9443)
//	DCWEBHOOK_CERT_FILE     Path to TLS certificate PEM file (required)
//	DCWEBHOOK_KEY_FILE      Path to TLS private key PEM file (required)
//	DCWEBHOOK_KUBECONFIG    Optional. Empty → in-cluster config (the production
//	                        default: runs in the Harvester cluster with a
//	                        ServiceAccount + RBAC). Set (base64 or raw YAML) only
//	                        for out-of-cluster/local runs.
//	DCWEBHOOK_LOG_LEVEL     zerolog level: debug|info|warn|error (default: info)
//
// Running locally (for development, not production):
//
//	# Generate a self-signed cert+key:
//	openssl req -x509 -newkey ec -pkeyopt ec_paramgen_curve:P-256 \
//	  -keyout /tmp/wh.key -out /tmp/wh.crt -days 365 -nodes -subj '/CN=localhost'
//
//	export DCWEBHOOK_CERT_FILE=/tmp/wh.crt
//	export DCWEBHOOK_KEY_FILE=/tmp/wh.key
//	export DCWEBHOOK_KUBECONFIG=$(base64 < ~/.kube/harvester-dev.yaml)
//	./webhook
//
// The binary does not connect to PostgreSQL or an OIDC provider — it only
// needs a Kubernetes dynamic client to resolve NAD CRDs at admission time.
package main

import (
	"context"
	"encoding/base64"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/kelseyhightower/envconfig"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"github.com/wso2/dc-api/internal/webhook"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

type config struct {
	ListenAddr string `envconfig:"LISTEN_ADDR" default:":9443"`
	CertFile   string `envconfig:"CERT_FILE"   required:"true"`
	KeyFile    string `envconfig:"KEY_FILE"    required:"true"`
	// Kubeconfig is optional. Empty → in-cluster config (the webhook runs in
	// the Harvester cluster with a ServiceAccount + RBAC, the GitOps default).
	// Set it (base64 or raw YAML) only for out-of-cluster/local runs.
	Kubeconfig string `envconfig:"KUBECONFIG"`
	LogLevel   string `envconfig:"LOG_LEVEL"   default:"info"`
}

func main() {
	// ── Config ────────────────────────────────────────────────────────────────
	var cfg config
	if err := envconfig.Process("DCWEBHOOK", &cfg); err != nil {
		l := zerolog.New(os.Stdout).With().Timestamp().Logger()
		l.Fatal().Err(err).Msg("webhook: failed to load config — check DCWEBHOOK_* env vars")
	}

	// ── Logging ───────────────────────────────────────────────────────────────
	level, err := zerolog.ParseLevel(cfg.LogLevel)
	if err != nil {
		level = zerolog.InfoLevel
	}
	zerolog.SetGlobalLevel(level)
	log.Logger = zerolog.New(os.Stdout).With().Timestamp().Logger()
	log.Info().Str("listen", cfg.ListenAddr).Str("log_level", level.String()).Msg("DC-API webhook starting")

	// ── Kubernetes dynamic client ─────────────────────────────────────────────
	// In-cluster by default: the webhook runs in the Harvester cluster and reads
	// NetworkAttachmentDefinitions through its ServiceAccount (RBAC). A kubeconfig
	// is only needed for out-of-cluster runs (local dev) — set DCWEBHOOK_KUBECONFIG
	// then (accepts base64 or raw YAML, same as the harvester driver).
	var restCfg *rest.Config
	if cfg.Kubeconfig == "" {
		restCfg, err = rest.InClusterConfig()
		if err != nil {
			log.Fatal().Err(err).Msg("webhook: in-cluster config failed (set DCWEBHOOK_KUBECONFIG for out-of-cluster runs)")
		}
	} else {
		kubeconfigBytes, decErr := base64.StdEncoding.DecodeString(cfg.Kubeconfig)
		if decErr != nil {
			kubeconfigBytes = []byte(cfg.Kubeconfig)
		}
		restCfg, err = clientcmd.RESTConfigFromKubeConfig(kubeconfigBytes)
		if err != nil {
			log.Fatal().Err(err).Msg("webhook: parse kubeconfig failed")
		}
	}

	dynClient, err := dynamic.NewForConfig(restCfg)
	if err != nil {
		log.Fatal().Err(err).Msg("webhook: create dynamic client failed")
	}
	log.Info().Msg("webhook: Kubernetes dynamic client ready")

	// ── Webhook wiring ────────────────────────────────────────────────────────
	nadLookup := webhook.NewDynamicNADLookup(dynClient)
	mutator := webhook.NewMutator(nadLookup, log.Logger)
	wrapper := webhook.NewAdmissionReviewWrapper(mutator)
	srv := webhook.NewServer(wrapper, log.Logger)

	// ── HTTPS server ──────────────────────────────────────────────────────────
	httpSrv := &http.Server{
		Addr:         cfg.ListenAddr,
		Handler:      srv.Handler(),
		ReadTimeout:  5 * time.Second,
		WriteTimeout: 5 * time.Second,
		IdleTimeout:  30 * time.Second,
	}

	// Start in a goroutine so we can listen for shutdown signals.
	go func() {
		log.Info().Str("addr", cfg.ListenAddr).Msg("webhook: HTTPS server listening")
		if err := httpSrv.ListenAndServeTLS(cfg.CertFile, cfg.KeyFile); err != nil && err != http.ErrServerClosed {
			log.Fatal().Err(err).Msg("webhook: HTTPS server failed")
		}
	}()

	// ── Graceful shutdown ─────────────────────────────────────────────────────
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, os.Interrupt, syscall.SIGTERM)
	<-quit
	log.Info().Msg("webhook: shutdown signal received — draining")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := httpSrv.Shutdown(shutdownCtx); err != nil {
		log.Error().Err(err).Msg("webhook: graceful shutdown timed out")
	}
	log.Info().Msg("webhook: stopped cleanly")
}
