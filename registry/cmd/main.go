package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/go-logr/zapr"
	"go.uber.org/zap"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	registryv1alpha1 "github.com/wso2/open-cloud-datacenter/crds/registry/api/v1alpha1"
	"github.com/wso2/open-cloud-datacenter/crds/registry/internal/api"
	"github.com/wso2/open-cloud-datacenter/crds/registry/internal/audit"
	"github.com/wso2/open-cloud-datacenter/crds/registry/internal/config"
	"github.com/wso2/open-cloud-datacenter/crds/registry/internal/controller"
	"github.com/wso2/open-cloud-datacenter/crds/registry/internal/crypto"
	"github.com/wso2/open-cloud-datacenter/crds/registry/internal/db"
	"github.com/wso2/open-cloud-datacenter/crds/registry/internal/helm"
	"github.com/wso2/open-cloud-datacenter/crds/registry/internal/k8s"
	"github.com/wso2/open-cloud-datacenter/crds/registry/internal/metrics"
	"github.com/wso2/open-cloud-datacenter/crds/registry/internal/worker"
)

var scheme = runtime.NewScheme()

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(registryv1alpha1.AddToScheme(scheme))

	os.MkdirAll("/tmp/helm-values", 0700)
	os.MkdirAll("/tmp/helm-charts", 0700)

	if os.Getenv("ENV") == "production" {
		os.Setenv("GIN_MODE", "release")
	}
	_ = time.Now()
}

func main() {
	logger, _ := zap.NewProduction()
	defer logger.Sync()
	ctrl.SetLogger(zapr.NewLogger(logger))

	cfg, err := config.Load()
	if err != nil {
		logger.Fatal("failed to load config", zap.Error(err))
	}

	// Database (platform PostgreSQL — runs in registry namespace on Harvester)
	store, err := db.New(cfg.DB, logger)
	if err != nil {
		logger.Fatal("failed to connect to database", zap.Error(err))
	}
	defer store.Close()

	if err := store.Migrate(); err != nil {
		logger.Fatal("failed to run migrations", zap.Error(err))
	}

	// Encryption key (mounted from Secret registry-master-key)
	masterKey, err := os.ReadFile(cfg.Encryption.MasterKeyPath)
	if err != nil {
		logger.Fatal("failed to read master key", zap.Error(err))
	}
	cipher, err := crypto.NewCipher(masterKey)
	if err != nil {
		logger.Fatal("failed to initialize cipher", zap.Error(err))
	}

	// Kubernetes client for Harbor deployments — uses in-cluster config (runs on Harvester)
	harvesterK8sClient, err := k8s.NewHarvesterClient()
	if err != nil {
		logger.Fatal("failed to create harvester kubernetes client", zap.Error(err))
	}
	logger.Info("connected to harvester cluster (in-cluster)")

	helmDeployer, err := helm.NewDeployer(cfg.Helm, logger)
	if err != nil {
		logger.Fatal("failed to create helm deployer", zap.Error(err))
	}

	reg := metrics.NewRegistry()
	auditLog := audit.NewLogger(store, logger)

	// Background worker — polls DB for PENDING/DELETING jobs and drives the 7-step Harbor flow
	deployWorker := worker.New(store, helmDeployer, harvesterK8sClient, cipher, cfg.Helm, auditLog, reg, logger)
	go deployWorker.Run(context.Background())

	// ── Controller-runtime manager ─────────────────────────────────────────────
	// Watches RegistryInstance CRs and syncs intent (spec) to state (DB + Harbor).
	// Uses the same in-cluster config as the k8s client above.
	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme:                 scheme,
		HealthProbeBindAddress: ":8081",
		Metrics:                metricsserver.Options{BindAddress: "0"},
		LeaderElection:         false,
	})
	if err != nil {
		logger.Fatal("failed to create controller manager", zap.Error(err))
	}

	if err := (&controller.RegistryBackendReconciler{
		Client: mgr.GetClient(),
		Store:  store,
	}).SetupWithManager(mgr); err != nil {
		logger.Fatal("failed to setup RegistryBackend controller", zap.Error(err))
	}

	if err := (&controller.RegistryInstanceReconciler{
		Client: mgr.GetClient(),
		Store:  store,
	}).SetupWithManager(mgr); err != nil {
		logger.Fatal("failed to setup RegistryInstance controller", zap.Error(err))
	}

	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		logger.Fatal("failed to add healthz check", zap.Error(err))
	}
	if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		logger.Fatal("failed to add readyz check", zap.Error(err))
	}

	// ctrl.SetupSignalHandler returns a context cancelled on SIGTERM/SIGINT.
	// Both the manager and the HTTP server shut down from the same signal.
	sigCtx := ctrl.SetupSignalHandler()

	go func() {
		logger.Info("starting controller manager")
		if err := mgr.Start(sigCtx); err != nil {
			logger.Fatal("controller manager error", zap.Error(err))
		}
	}()

	// ── HTTP gateway server ────────────────────────────────────────────────────
	// Serves REST AP	I for dc-api (credentials, status queries, legacy provisioner UI).
	// DC-API can also create RegistryInstance CRs directly via K8s dynamic client.
	srv := api.NewServer(cfg, store, cipher, auditLog, reg, logger)
	httpServer := &http.Server{
		Addr:         fmt.Sprintf(":%d", cfg.Server.Port),
		Handler:      srv.Router(),
		ReadTimeout:  cfg.Server.ReadTimeout,
		WriteTimeout: cfg.Server.WriteTimeout,
	}

	go func() {
		logger.Info("registry provisioner starting", zap.Int("port", cfg.Server.Port))
		if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Fatal("server error", zap.Error(err))
		}
	}()

	// Wait for shutdown signal (same context as manager)
	<-sigCtx.Done()
	logger.Info("shutting down server")
	shutCtx, cancel := context.WithTimeout(context.Background(), cfg.Server.ShutdownTimeout)
	defer cancel()
	if err := httpServer.Shutdown(shutCtx); err != nil {
		logger.Error("server forced shutdown", zap.Error(err))
	}
	logger.Info("server exited")
}
