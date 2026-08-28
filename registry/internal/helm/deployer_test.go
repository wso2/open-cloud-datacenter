package helm

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	"helm.sh/helm/v3/pkg/cli"

	"github.com/wso2/open-cloud-datacenter/crds/registry/internal/config"
)

func TestNestedGet(t *testing.T) {
	m := map[string]interface{}{
		"persistence": map[string]interface{}{
			"persistentVolumeClaim": map[string]interface{}{
				"registry": map[string]interface{}{
					"size": "5Gi",
				},
			},
		},
	}

	tests := []struct {
		name    string
		path    []string
		wantVal interface{}
		wantOK  bool
	}{
		{
			name:    "existing nested leaf",
			path:    []string{"persistence", "persistentVolumeClaim", "registry", "size"},
			wantVal: "5Gi",
			wantOK:  true,
		},
		{
			name:   "missing top-level key",
			path:   []string{"nope"},
			wantOK: false,
		},
		{
			name:   "missing intermediate key",
			path:   []string{"persistence", "persistentVolumeClaim", "database", "size"},
			wantOK: false,
		},
		{
			name:   "path walks into a non-map leaf",
			path:   []string{"persistence", "persistentVolumeClaim", "registry", "size", "tooDeep"},
			wantOK: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			v, ok := nestedGet(m, tt.path)
			if ok != tt.wantOK {
				t.Fatalf("nestedGet() ok = %v, want %v", ok, tt.wantOK)
			}
			if ok && v != tt.wantVal {
				t.Errorf("nestedGet() = %v, want %v", v, tt.wantVal)
			}
		})
	}
}

func TestNestedGet_NilMap(t *testing.T) {
	if _, ok := nestedGet(nil, []string{"anything"}); ok {
		t.Error("nestedGet(nil, ...) ok = true, want false")
	}
}

func TestNestedSet(t *testing.T) {
	t.Run("overwrites an existing leaf", func(t *testing.T) {
		m := map[string]interface{}{
			"persistence": map[string]interface{}{
				"persistentVolumeClaim": map[string]interface{}{
					"database": map[string]interface{}{
						"size": "20Gi",
					},
				},
			},
		}
		nestedSet(m, []string{"persistence", "persistentVolumeClaim", "database", "size"}, "5Gi")
		got, _ := nestedGet(m, []string{"persistence", "persistentVolumeClaim", "database", "size"})
		if got != "5Gi" {
			t.Errorf("after nestedSet, leaf = %v, want 5Gi", got)
		}
	})

	t.Run("missing intermediate path is a no-op, not a panic", func(t *testing.T) {
		m := map[string]interface{}{"persistence": map[string]interface{}{}}
		nestedSet(m, []string{"persistence", "persistentVolumeClaim", "trivy", "size"}, "5Gi")
		if _, ok := nestedGet(m, []string{"persistence", "persistentVolumeClaim", "trivy", "size"}); ok {
			t.Error("nestedSet created missing intermediate structure; expected a silent no-op")
		}
	})
}

func TestFreezeStorageSizes(t *testing.T) {
	deployed := map[string]interface{}{
		"persistence": map[string]interface{}{
			"persistentVolumeClaim": map[string]interface{}{
				"registry": map[string]interface{}{"size": "5Gi"},
				"jobservice": map[string]interface{}{
					"jobLog": map[string]interface{}{"size": "1Gi"},
				},
				"database": map[string]interface{}{"size": "5Gi"},
				"redis":    map[string]interface{}{"size": "1Gi"},
				"trivy":    map[string]interface{}{"size": "5Gi"},
			},
		},
	}
	// desired simulates a freshly re-rendered chart proposing a plan-upgrade
	// bump to every storage field — exactly the diff freezeStorageSizes must
	// suppress, since none of these are actually appliable through Helm.
	desired := map[string]interface{}{
		"persistence": map[string]interface{}{
			"persistentVolumeClaim": map[string]interface{}{
				"registry": map[string]interface{}{"size": "50Gi"},
				"jobservice": map[string]interface{}{
					"jobLog": map[string]interface{}{"size": "10Gi"},
				},
				"database": map[string]interface{}{"size": "50Gi"},
				"redis":    map[string]interface{}{"size": "10Gi"},
				"trivy":    map[string]interface{}{"size": "50Gi"},
				// a field freezeStorageSizes doesn't know about must survive untouched
				"unrelatedField": "keep-me",
			},
		},
	}

	freezeStorageSizes(desired, deployed)

	if !reflect.DeepEqual(desired, func() map[string]interface{} {
		want := map[string]interface{}{
			"persistence": map[string]interface{}{
				"persistentVolumeClaim": map[string]interface{}{
					"registry": map[string]interface{}{"size": "5Gi"},
					"jobservice": map[string]interface{}{
						"jobLog": map[string]interface{}{"size": "1Gi"},
					},
					"database":       map[string]interface{}{"size": "5Gi"},
					"redis":          map[string]interface{}{"size": "1Gi"},
					"trivy":          map[string]interface{}{"size": "5Gi"},
					"unrelatedField": "keep-me",
				},
			},
		}
		return want
	}()) {
		t.Errorf("freezeStorageSizes() did not converge desired to deployed sizes:\ngot:  %#v", desired)
	}
}

func TestFreezeStorageSizes_MissingDeployedPathIsSkipped(t *testing.T) {
	// Simulates a brand-new release: deployed has no persistence block at all
	// yet (first install, GetValues hasn't returned anything meaningful).
	deployed := map[string]interface{}{}
	desired := map[string]interface{}{
		"persistence": map[string]interface{}{
			"persistentVolumeClaim": map[string]interface{}{
				"registry": map[string]interface{}{"size": "5Gi"},
			},
		},
	}
	before := "5Gi"

	freezeStorageSizes(desired, deployed)

	got, _ := nestedGet(desired, []string{"persistence", "persistentVolumeClaim", "registry", "size"})
	if got != before {
		t.Errorf("freezeStorageSizes() modified desired when deployed had nothing to freeze from; got %v, want unchanged %v", got, before)
	}
}

func TestReleaseName(t *testing.T) {
	tests := []struct {
		namespace string
		want      string
	}{
		{"acme", "harbor-acme"},
		{"ns-with-dashes", "harbor-ns-with-dashes"},
	}
	for _, tt := range tests {
		if got := ReleaseName(tt.namespace); got != tt.want {
			t.Errorf("ReleaseName(%q) = %q, want %q", tt.namespace, got, tt.want)
		}
	}
}

// --- loadChart: version-scoped, atomically-populated chart cache ---

func TestChartCacheDir_IncludesChartVersion(t *testing.T) {
	d1 := &Deployer{cfg: config.HelmConfig{HarborChartVer: "1.14.0"}}
	d2 := &Deployer{cfg: config.HelmConfig{HarborChartVer: "1.15.0"}}
	if d1.chartCacheDir() == d2.chartCacheDir() {
		t.Fatalf("two chart versions resolved to the same cache dir %q — a version change would load a stale chart", d1.chartCacheDir())
	}
	if !strings.HasSuffix(d1.chartCacheDir(), "harbor-1.14.0") {
		t.Errorf("chartCacheDir() = %q, want it to end in harbor-1.14.0", d1.chartCacheDir())
	}
}

// writeMinimalChart lays down the smallest tree loader.Load accepts, so
// loadChart can be exercised without pulling from a real chart repository.
func writeMinimalChart(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("MkdirAll(%q): %v", dir, err)
	}
	meta := "apiVersion: v2\nname: harbor\nversion: 1.14.0\n"
	if err := os.WriteFile(filepath.Join(dir, "Chart.yaml"), []byte(meta), 0o600); err != nil {
		t.Fatalf("write Chart.yaml: %v", err)
	}
}

// A cached chart is served from disk. Under -race, the concurrent calls also
// cover chartMu.
func TestLoadChart_ConcurrentCallsUseTheCacheWithoutRacing(t *testing.T) {
	root := t.TempDir()
	orig := chartCacheRoot
	chartCacheRoot = root
	t.Cleanup(func() { chartCacheRoot = orig })

	d := &Deployer{cfg: config.HelmConfig{HarborChartVer: "1.14.0"}}
	writeMinimalChart(t, d.chartCacheDir())

	const goroutines = 8
	var wg sync.WaitGroup
	errs := make([]error, goroutines)
	names := make([]string, goroutines)
	for i := range goroutines {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			c, err := d.loadChart()
			errs[i] = err
			if c != nil && c.Metadata != nil {
				names[i] = c.Metadata.Name
			}
		}(i)
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("goroutine %d: loadChart() error = %v", i, err)
		}
		if names[i] != "harbor" {
			t.Errorf("goroutine %d: chart name = %q, want %q", i, names[i], "harbor")
		}
	}
}

// A failed pull must leave nothing at the cache path, so the next call
// re-pulls instead of loading a partial chart.
func TestLoadChart_FailedPullLeavesNoPartialCache(t *testing.T) {
	root := t.TempDir()
	orig := chartCacheRoot
	chartCacheRoot = root
	t.Cleanup(func() { chartCacheRoot = orig })

	// An unreachable repo URL guarantees the pull fails.
	d := &Deployer{
		cfg: config.HelmConfig{
			HarborChartVer: "1.14.0",
			HarborRepoURL:  "http://127.0.0.1:1/does-not-exist",
		},
		env: cli.New(),
	}

	if _, err := d.loadChart(); err == nil {
		t.Fatal("loadChart() error = nil, want a failure against an unreachable repo")
	}
	if _, err := os.Stat(d.chartCacheDir()); !os.IsNotExist(err) {
		t.Errorf("cache dir %q exists after a failed pull — a later load would treat it as valid", d.chartCacheDir())
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatalf("ReadDir(%q): %v", root, err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "pull-") {
			t.Errorf("scratch dir %q was left behind after a failed pull", e.Name())
		}
	}
}
