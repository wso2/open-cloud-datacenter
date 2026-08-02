package main

import (
	"os"

	"github.com/go-logr/zapr"
	"go.uber.org/zap"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/metrics/filters"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	registryv1alpha1 "github.com/wso2/open-cloud-datacenter/crds/registry/api/v1alpha1"
	"github.com/wso2/open-cloud-datacenter/crds/registry/internal/config"
	"github.com/wso2/open-cloud-datacenter/crds/registry/internal/controller"
	"github.com/wso2/open-cloud-datacenter/crds/registry/internal/helm"
)

var scheme = runtime.NewScheme()

// init registers the API types with the scheme and creates Helm's scratch dirs.
func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(registryv1alpha1.AddToScheme(scheme))

	// Helm renders values/charts to these scratch dirs.
	_ = os.MkdirAll("/tmp/helm-values", 0700)
	_ = os.MkdirAll("/tmp/helm-charts", 0700)
}

// main starts the controller manager and both reconcilers.
func main() {
	logger, _ := zap.NewProduction()
	defer func() { _ = logger.Sync() }()
	ctrl.SetLogger(zapr.NewLogger(logger))

	cfg, err := config.Load()
	if err != nil {
		logger.Fatal("failed to load config", zap.Error(err))
	}

	helmDeployer, err := helm.NewDeployer(cfg.Helm, logger)
	if err != nil {
		logger.Fatal("failed to create helm deployer", zap.Error(err))
	}

	// Leader election is ON so that running multiple replicas yields exactly
	// one active reconciler (hot standby for failover).
	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme:                 scheme,
		HealthProbeBindAddress: ":8081",
		// Metrics served over HTTPS; the API server validates each scraper's
		// bearer token. With CertDir unset the serving certificate is
		// self-signed for localhost, so set CertDir to a cert-manager
		// certificate covering the Service DNS name to make it verifiable.
		Metrics: metricsserver.Options{
			BindAddress:    ":8443",
			SecureServing:  true,
			FilterProvider: filters.WithAuthenticationAndAuthorization,
		},
		LeaderElection:   true,
		LeaderElectionID: "registry-provisioner.opencloud.wso2.com",
	})
	if err != nil {
		logger.Fatal("failed to create controller manager", zap.Error(err))
	}

	if err := (&controller.RegistryBackendReconciler{
		Client:   mgr.GetClient(),
		Scheme:   mgr.GetScheme(),
		Recorder: mgr.GetEventRecorderFor("registrybackend"),
		Helm:     helmDeployer,
		HelmCfg:  cfg.Helm,
	}).SetupWithManager(mgr); err != nil {
		logger.Fatal("failed to setup RegistryBackend controller", zap.Error(err))
	}

	if err := (&controller.RegistryReconciler{
		Client:   mgr.GetClient(),
		Scheme:   mgr.GetScheme(),
		Recorder: mgr.GetEventRecorderFor("registry"),
		HelmCfg:  cfg.Helm,
	}).SetupWithManager(mgr); err != nil {
		logger.Fatal("failed to setup Registry controller", zap.Error(err))
	}

	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		logger.Fatal("failed to add healthz check", zap.Error(err))
	}
	if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		logger.Fatal("failed to add readyz check", zap.Error(err))
	}

	logger.Info("starting registry operator")
	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		logger.Fatal("controller manager error", zap.Error(err))
	}
}
