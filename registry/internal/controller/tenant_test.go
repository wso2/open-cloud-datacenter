package controller

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestTenantIDFromProject(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		want    string
		wantErr bool
	}{
		{"rancher cluster:project form", "c-m-abc123:p-xyz789", "p-xyz789", false},
		{"bare project id", "p-xyz789", "p-xyz789", false},
		{"uppercase is normalised", "C-M-ABC:P-XYZ", "p-xyz", false},
		{"surrounding space", " c-m-abc:p-xyz ", "p-xyz", false},
		{"cluster with no project", "c-m-abc:", "", true},
		{"empty", "", "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := tenantIDFromProject(tt.raw)
			if (err != nil) != tt.wantErr {
				t.Fatalf("tenantIDFromProject(%q) error = %v, wantErr %v", tt.raw, err, tt.wantErr)
			}
			if !tt.wantErr && got != tt.want {
				t.Errorf("tenantIDFromProject(%q) = %q, want %q", tt.raw, got, tt.want)
			}
		})
	}
}

// The tenant comes from the namespace, so it is worth proving both that a
// project-assigned namespace resolves and that an unassigned one refuses rather
// than falling back to a default.
func TestTenantForNamespace(t *testing.T) {
	assigned := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{
		Name:        "acme-project-1",
		Annotations: map[string]string{tenantProjectKey: "c-m-abc:p-acme"},
	}}
	labelled := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{
		Name:   "acme-project-2",
		Labels: map[string]string{tenantProjectKey: "c-m-abc:p-acme"},
	}}
	orphan := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "no-project"}}

	fc := fake.NewClientBuilder().
		WithScheme(newTestScheme(t)).
		WithObjects(assigned, labelled, orphan).
		Build()
	r := newRegistryReconciler(t, fc)

	for _, ns := range []string{"acme-project-1", "acme-project-2"} {
		got, err := r.tenantForNamespace(context.Background(), ns)
		if err != nil {
			t.Fatalf("tenantForNamespace(%q) error = %v", ns, err)
		}
		if got != "p-acme" {
			t.Errorf("tenantForNamespace(%q) = %q, want p-acme", ns, got)
		}
	}

	if _, err := r.tenantForNamespace(context.Background(), "no-project"); err == nil {
		t.Error("tenantForNamespace() on a namespace with no project returned nil error; " +
			"an unassigned namespace must not resolve to any tenant")
	}

	if _, err := r.tenantForNamespace(context.Background(), "does-not-exist"); err == nil {
		t.Error("tenantForNamespace() on a missing namespace returned nil error")
	}
}

func TestBackendNameForTenant(t *testing.T) {
	// Deterministic naming is what lets concurrent first Registries converge on
	// one backend and leave the race to the API server.
	if got := backendNameForTenant("p-acme"); got != "rb-p-acme" {
		t.Errorf("backendNameForTenant() = %q, want rb-p-acme", got)
	}
	if backendNameForTenant("p-a") == backendNameForTenant("p-b") {
		t.Error("different tenants produced the same backend name")
	}
}
