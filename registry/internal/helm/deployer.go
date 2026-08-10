package helm

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"time"

	"go.uber.org/zap"
	"gopkg.in/yaml.v3"
	"helm.sh/helm/v3/pkg/action"
	"helm.sh/helm/v3/pkg/chart"
	"helm.sh/helm/v3/pkg/chart/loader"
	"helm.sh/helm/v3/pkg/cli"
	"helm.sh/helm/v3/pkg/getter"
	"helm.sh/helm/v3/pkg/repo"
	"helm.sh/helm/v3/pkg/storage/driver"
	"k8s.io/cli-runtime/pkg/genericclioptions"

	"github.com/wso2/open-cloud-datacenter/crds/registry/internal/config"
)

// chartCacheRoot holds the extracted Harbor chart. It lives under /tmp because
// the container's root filesystem is read-only.
var chartCacheRoot = "/tmp/helm-charts"

// Deployer installs/upgrades/uninstalls Harbor Helm releases.
// The provisioner runs directly on Harvester so Helm uses the pod's in-cluster config.
type Deployer struct {
	cfg    config.HelmConfig
	logger *zap.Logger
	env    *cli.EnvSettings

	// chartMu serializes chart acquisition.
	chartMu sync.Mutex
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

// ensureRepo verifies the Harbor chart repository is reachable.
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

// Install deploys Harbor into a namespace on the Harvester cluster. One release
// per namespace, so the namespace alone names it.
func (d *Deployer) Install(ctx context.Context, namespace string, values []byte) error {
	cfg, err := d.actionConfig(namespace)
	if err != nil {
		return err
	}

	valuesPath, cleanup, err := writeTempValues(namespace, values)
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

	rel := ReleaseName(namespace)

	// Level-triggered convergence: desired state is the freshly rendered
	// values; observed state is the deployed release's values. Missing →
	// install. Identical → no-op (the steady-state case). Drifted → upgrade
	// (e.g. a plan change). Upgrading is safe because every chart secret is
	// pinned in values, so a re-render can never rotate live credentials.
	history := action.NewHistory(cfg)
	history.Max = 1
	if _, err := history.Run(rel); err != nil {
		if !errors.Is(err, driver.ErrReleaseNotFound) {
			return fmt.Errorf("helm history: %w", err)
		}
		install := action.NewInstall(cfg)
		install.ReleaseName = rel
		install.Namespace = namespace
		install.CreateNamespace = false
		install.Wait = false
		install.Timeout = 10 * time.Minute
		install.Atomic = false

		d.logger.Info("installing harbor on harvester",
			zap.String("namespace", namespace),
			zap.String("target_cluster", "harvester"),
		)
		if _, err := install.RunWithContext(ctx, chart, vals); err != nil {
			return fmt.Errorf("helm install: %w", err)
		}
		return nil
	}

	// Release exists — diff desired values against deployed values.
	deployed, err := action.NewGetValues(cfg).Run(rel)
	if err != nil {
		return fmt.Errorf("helm get values: %w", err)
	}

	// PVC sizes must never flow through helm upgrade: StatefulSet
	// volumeClaimTemplates are immutable and Kubernetes cannot shrink claims.
	// Freeze the deployed sizes into the desired values; storage growth is the
	// reconciler's job (ensureStorageSize patches the PVCs directly).
	freezeStorageSizes(vals, deployed)

	// The internal Postgres password must NOT be re-sent on an upgrade. Harbor's
	// chart pipes database.internal.password through b64dec whenever the
	// database Secret already exists, and Sprig's b64dec returns err.Error()
	// instead of failing — so a plaintext password silently becomes
	// "illegal base64 data at input byte N" and Harbor's core can no longer
	// authenticate against the database it initialised on the first install.
	// Blanking it takes the chart's intended path: `default` falls through to
	// the stored base64 and b64dec round-trips it correctly. The password stays
	// pinned by the Secret the first install wrote.
	// Blanked on BOTH sides, so the password is excluded from drift detection.
	// It never legitimately changes, and comparing it would force one pointless
	// upgrade right after every install: the install stored the real value
	// while every later render sends blank.
	blankInternalDBPassword(vals)
	blankInternalDBPassword(deployed)

	if reflect.DeepEqual(vals, deployed) {
		return nil // no drift — nothing to do
	}

	d.logger.Info("values drift detected, upgrading harbor release",
		zap.String("namespace", namespace),
	)
	up := action.NewUpgrade(cfg)
	up.Namespace = namespace
	up.Wait = false // the reconciler polls readiness (Ping + RequeueAfter)
	up.Timeout = 10 * time.Minute
	up.MaxHistory = 5
	if _, err := up.RunWithContext(ctx, rel, chart, vals); err != nil {
		return fmt.Errorf("helm upgrade: %w", err)
	}
	return nil
}

// dbPasswordPath is where Harbor's chart reads the internal Postgres password.
var dbPasswordPath = []string{"database", "internal", "password"}

// blankInternalDBPassword clears database.internal.password so an upgrade
// preserves the password already stored in the chart's database Secret. See
// the call site in Install for why re-sending it corrupts the credential.
//
// Blanking before the drift comparison (not just before the upgrade) is
// deliberate: it means the desired values converge on a stable shape, so the
// release settles after exactly one upgrade instead of diffing forever against
// a value that can never match what is deployed.
func blankInternalDBPassword(vals map[string]interface{}) {
	nestedSet(vals, dbPasswordPath, "")
}

// freezeStorageSizes copies the deployed persistence sizes into the desired
// values so the upgrade diff never proposes a PVC size change (immutable
// through the chart). The reconciler grows PVCs directly instead.
func freezeStorageSizes(desired, deployed map[string]interface{}) {
	paths := [][]string{
		{"persistence", "persistentVolumeClaim", "registry", "size"},
		{"persistence", "persistentVolumeClaim", "jobservice", "jobLog", "size"},
		{"persistence", "persistentVolumeClaim", "database", "size"},
		{"persistence", "persistentVolumeClaim", "redis", "size"},
		{"persistence", "persistentVolumeClaim", "trivy", "size"},
	}
	for _, p := range paths {
		if v, ok := nestedGet(deployed, p); ok {
			nestedSet(desired, p, v)
		}
	}
}

// nestedGet returns the value at path in m, reporting whether it exists.
func nestedGet(m map[string]interface{}, path []string) (interface{}, bool) {
	var cur interface{} = m
	for _, k := range path {
		mm, ok := cur.(map[string]interface{})
		if !ok {
			return nil, false
		}
		if cur, ok = mm[k]; !ok {
			return nil, false
		}
	}
	return cur, true
}

// nestedSet writes v at path in m, doing nothing if the path is absent.
func nestedSet(m map[string]interface{}, path []string, v interface{}) {
	cur := m
	for _, k := range path[:len(path)-1] {
		next, ok := cur[k].(map[string]interface{})
		if !ok {
			return
		}
		cur = next
	}
	cur[path[len(path)-1]] = v
}

// Uninstall removes the Harbor Helm release from a namespace on Harvester.
// PVCs are NOT deleted (resourcePolicy: keep) — data survives until explicit hard delete.
func (d *Deployer) Uninstall(ctx context.Context, namespace string) error {
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
		_, err := uninstall.Run(ReleaseName(namespace))
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
	d.logger.Info("harbor uninstalled", zap.String("namespace", namespace))
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

// chartCacheDir is the extracted chart's location, scoped by chart version so
// a HARBOR_CHART_VERSION change never resolves to a stale chart.
func (d *Deployer) chartCacheDir() string {
	return filepath.Join(chartCacheRoot, "harbor-"+d.cfg.HarborChartVer)
}

// loadChart returns the Harbor chart, pulling it on first use. The chart is
// extracted into a scratch directory and moved into place with one atomic
// rename, so a failed pull never leaves a partial chart in the cache.
func (d *Deployer) loadChart() (*chart.Chart, error) {
	d.chartMu.Lock()
	defer d.chartMu.Unlock()

	chartDir := d.chartCacheDir()
	if _, err := os.Stat(chartDir); err == nil {
		return loader.Load(chartDir)
	}
	if err := os.MkdirAll(chartCacheRoot, 0700); err != nil {
		return nil, err
	}

	scratch, err := os.MkdirTemp(chartCacheRoot, "pull-")
	if err != nil {
		return nil, fmt.Errorf("create chart scratch dir: %w", err)
	}
	defer os.RemoveAll(scratch)

	pull := action.NewPullWithOpts(action.WithConfig(&action.Configuration{}))
	pull.Settings = d.env
	pull.RepoURL = d.cfg.HarborRepoURL
	pull.Version = d.cfg.HarborChartVer
	pull.DestDir = scratch
	pull.Untar = true
	if _, err := pull.Run("harbor"); err != nil {
		return nil, fmt.Errorf("pull harbor chart: %w", err)
	}

	if err := os.Rename(filepath.Join(scratch, "harbor"), chartDir); err != nil {
		// Lost a race to populate the cache — use what landed there.
		if _, statErr := os.Stat(chartDir); statErr == nil {
			return loader.Load(chartDir)
		}
		return nil, fmt.Errorf("move harbor chart into cache: %w", err)
	}
	return loader.Load(chartDir)
}

// writeTempValues writes values to a temporary file and returns its path
// along with a cleanup func that deletes it.
func writeTempValues(namespace string, values []byte) (path string, cleanup func(), err error) {
	f, err := os.CreateTemp("/tmp/helm-values", fmt.Sprintf("harbor-%s-*.yaml", namespace))
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

// parseValuesFile reads and unmarshals a YAML values file.
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

// yamlToMap unmarshals YAML into out.
func yamlToMap(data []byte, out map[string]interface{}) error {
	return yaml.Unmarshal(data, out)
}

// ReleaseName returns the Helm release name for a namespace's Harbor. Exported
// because the reconciler selects Harbor's objects by the release label Helm
// derives from it, and that selector must not drift from the name used here.
func ReleaseName(namespace string) string {
	return fmt.Sprintf("harbor-%s", namespace)
}
