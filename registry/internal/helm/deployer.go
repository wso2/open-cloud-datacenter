package helm

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"go.uber.org/zap"
	"gopkg.in/yaml.v3"
	"helm.sh/helm/v3/pkg/action"
	"helm.sh/helm/v3/pkg/chart"
	"helm.sh/helm/v3/pkg/chart/loader"
	"helm.sh/helm/v3/pkg/cli"
	"helm.sh/helm/v3/pkg/getter"
	"helm.sh/helm/v3/pkg/repo"
	"k8s.io/cli-runtime/pkg/genericclioptions"

	"github.com/wso2/open-cloud-datacenter/crds/registry/internal/config"
)

// Deployer installs/upgrades/uninstalls Harbor Helm releases.
// The provisioner runs directly on Harvester so Helm uses the pod's in-cluster config.
type Deployer struct {
	cfg    config.HelmConfig
	logger *zap.Logger
	env    *cli.EnvSettings
}

// NewDeployer creates a Deployer. Helm targets Harvester via the pod's in-cluster config
// since the provisioner runs directly on Harvester in the registry namespace.
func NewDeployer(cfg config.HelmConfig, logger *zap.Logger) (*Deployer, error) {
	env := cli.New()
	d := &Deployer{cfg: cfg, logger: logger, env: env}
	if err := d.ensureRepo(); err != nil {
		return nil, err
	}
	return d, nil
}

func (d *Deployer) ensureRepo() error {
	entry := &repo.Entry{
		Name: "harbor",
		URL:  d.cfg.HarborRepoURL,
	}
	r, err := repo.NewChartRepository(entry, getter.All(d.env))
	if err != nil {
		return fmt.Errorf("create chart repo: %w", err)
	}
	if _, err = r.DownloadIndexFile(); err != nil {
		return fmt.Errorf("download harbor repo index: %w", err)
	}
	return nil
}

// Install deploys a new Harbor instance for a tenant into their management namespace
// on the Harvester cluster.
func (d *Deployer) Install(ctx context.Context, tenantID, namespace string, values []byte) error {
	cfg, err := d.actionConfig(namespace)
	if err != nil {
		return err
	}

	valuesPath, cleanup, err := writeTempValues(tenantID, values)
	defer cleanup()
	if err != nil {
		return err
	}

	chart, err := d.loadChart()
	if err != nil {
		return err
	}

	vals, err := parseValuesFile(valuesPath)
	if err != nil {
		return err
	}

	install := action.NewInstall(cfg)
	install.ReleaseName = releaseName(tenantID)
	install.Namespace = namespace
	install.CreateNamespace = false
	install.Wait = false
	install.Timeout = 10 * time.Minute
	install.Atomic = false

	// Check if release already exists (retry after partial failure) — skip install entirely.
	// Do NOT upgrade: upgrade regenerates passwords which breaks the running Harbor DB.
	// WaitForAllReady will confirm pods are healthy before proceeding to bootstrap.
	history := action.NewHistory(cfg)
	history.Max = 1
	if _, err := history.Run(releaseName(tenantID)); err == nil {
		d.logger.Info("release already exists, skipping install",
			zap.String("tenant", tenantID),
		)
		return nil
	}

	d.logger.Info("installing harbor on harvester",
		zap.String("tenant", tenantID),
		zap.String("namespace", namespace),
		zap.String("target_cluster", "harvester"),
	)
	if _, err := install.RunWithContext(ctx, chart, vals); err != nil {
		return fmt.Errorf("helm install: %w", err)
	}
	return nil
}

// Uninstall removes the Harbor Helm release from the tenant's management namespace on Harvester.
// PVCs are NOT deleted (resourcePolicy: keep) — data survives until explicit hard delete.
func (d *Deployer) Uninstall(ctx context.Context, tenantID, namespace string) error {
	cfg, err := d.actionConfig(namespace)
	if err != nil {
		return err
	}
	uninstall := action.NewUninstall(cfg)
	uninstall.Wait = false
	uninstall.IgnoreNotFound = true

	type result struct{ err error }
	ch := make(chan result, 1)
	go func() {
		_, err := uninstall.Run(releaseName(tenantID))
		ch <- result{err}
	}()
	select {
	case <-ctx.Done():
		return fmt.Errorf("helm uninstall timed out: %w", ctx.Err())
	case r := <-ch:
		if r.err != nil {
			return fmt.Errorf("helm uninstall: %w", r.err)
		}
	}
	d.logger.Info("harbor uninstalled", zap.String("tenant", tenantID))
	return nil
}

// --- private helpers ---

// actionConfig builds a Helm action configuration using the pod's in-cluster config.
// The provisioner runs on Harvester directly so no external kubeconfig is needed.
func (d *Deployer) actionConfig(namespace string) (*action.Configuration, error) {
	cfg := new(action.Configuration)

	configFlags := genericclioptions.NewConfigFlags(true)
	configFlags.Namespace = &namespace

	if err := cfg.Init(configFlags, namespace, "secret", func(format string, v ...interface{}) {
		d.logger.Debug(fmt.Sprintf(format, v...))
	}); err != nil {
		return nil, fmt.Errorf("init helm action config: %w", err)
	}
	return cfg, nil
}

func (d *Deployer) loadChart() (*chart.Chart, error) {
	chartDir := filepath.Join("/tmp/helm-charts", "harbor")
	if _, err := os.Stat(chartDir); err == nil {
		return loader.Load(chartDir)
	}
	if err := os.MkdirAll("/tmp/helm-charts", 0700); err != nil {
		return nil, err
	}
	pull := action.NewPullWithOpts(action.WithConfig(&action.Configuration{}))
	pull.Settings = d.env
	pull.RepoURL = d.cfg.HarborRepoURL
	pull.Version = d.cfg.HarborChartVer
	pull.DestDir = "/tmp/helm-charts"
	pull.Untar = true
	if _, err := pull.Run("harbor"); err != nil {
		return nil, fmt.Errorf("pull harbor chart: %w", err)
	}
	return loader.Load(chartDir)
}

func writeTempValues(tenantID string, values []byte) (path string, cleanup func(), err error) {
	f, err := os.CreateTemp("/tmp/helm-values", fmt.Sprintf("harbor-%s-*.yaml", tenantID))
	if err != nil {
		return "", func() {}, err
	}
	cleanup = func() {
		f.Close()
		os.Remove(f.Name()) // always delete — contains passwords
	}
	if _, err := f.Write(values); err != nil {
		return "", cleanup, err
	}
	return f.Name(), cleanup, nil
}

func parseValuesFile(path string) (map[string]interface{}, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	vals := map[string]interface{}{}
	if err := yamlToMap(data, vals); err != nil {
		return nil, fmt.Errorf("parse values: %w", err)
	}
	return vals, nil
}

func yamlToMap(data []byte, out map[string]interface{}) error {
	return yaml.Unmarshal(data, out)
}

func releaseName(tenantID string) string {
	return fmt.Sprintf("harbor-%s", tenantID)
}
