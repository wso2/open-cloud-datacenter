package helm

import (
	"reflect"
	"testing"
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
	// Simulates a brand-new tenant: deployed has no persistence block at all
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
		tenantID string
		want     string
	}{
		{"acme", "harbor-acme"},
		{"tenant-with-dashes", "harbor-tenant-with-dashes"},
	}
	for _, tt := range tests {
		if got := releaseName(tt.tenantID); got != tt.want {
			t.Errorf("releaseName(%q) = %q, want %q", tt.tenantID, got, tt.want)
		}
	}
}
