package controller

import (
	"context"
	"errors"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	registryv1alpha1 "github.com/wso2/open-cloud-datacenter/crds/registry/api/v1alpha1"
)

func newInstanceReconciler(t *testing.T, fc client.WithWatch) *RegistryInstanceReconciler {
	return &RegistryInstanceReconciler{
		Client:   fc,
		Scheme:   newTestScheme(t),
		Recorder: record.NewFakeRecorder(32),
	}
}

// --- resolveBackend: the three states the referenced RegistryBackend can be in ---

func TestResolveBackend_BackendNotFound(t *testing.T) {
	instance := &registryv1alpha1.RegistryInstance{
		ObjectMeta: metav1.ObjectMeta{Name: "ri-sample", Namespace: "registry-system"},
		Spec: registryv1alpha1.RegistryInstanceSpec{
			BackendRef: registryv1alpha1.BackendRef{Name: "rb-missing", Namespace: "registry-system"},
		},
	}
	fc := fake.NewClientBuilder().
		WithScheme(newTestScheme(t)).
		WithStatusSubresource(&registryv1alpha1.RegistryInstance{}).
		WithObjects(instance).
		Build()

	r := newInstanceReconciler(t, fc)
	backend, res, err := r.resolveBackend(context.Background(), instance)

	if backend != nil {
		t.Errorf("resolveBackend() backend = %+v, want nil when the Backend doesn't exist", backend)
	}
	if err != nil {
		t.Errorf("resolveBackend() err = %v, want nil (a missing Backend is a wait-state, not a hard error)", err)
	}
	if res.RequeueAfter != 30*time.Second {
		t.Errorf("resolveBackend() RequeueAfter = %v, want 30s for a not-found Backend", res.RequeueAfter)
	}
}

func TestResolveBackend_BackendNotReady(t *testing.T) {
	backend := &registryv1alpha1.RegistryBackend{
		ObjectMeta: metav1.ObjectMeta{Name: "rb-acme", Namespace: "registry-system"},
		Status:     registryv1alpha1.RegistryBackendStatus{Phase: phaseProvisioning},
	}
	instance := &registryv1alpha1.RegistryInstance{
		ObjectMeta: metav1.ObjectMeta{Name: "ri-sample", Namespace: "registry-system"},
		Spec: registryv1alpha1.RegistryInstanceSpec{
			BackendRef: registryv1alpha1.BackendRef{Name: "rb-acme", Namespace: "registry-system"},
		},
	}
	fc := fake.NewClientBuilder().
		WithScheme(newTestScheme(t)).
		WithStatusSubresource(&registryv1alpha1.RegistryInstance{}).
		WithObjects(backend, instance).
		Build()

	r := newInstanceReconciler(t, fc)
	got, res, err := r.resolveBackend(context.Background(), instance)

	if got != nil {
		t.Errorf("resolveBackend() backend = %+v, want nil while Backend.Status.Phase = %q", got, phaseProvisioning)
	}
	if err != nil {
		t.Errorf("resolveBackend() err = %v, want nil", err)
	}
	if res.RequeueAfter != 15*time.Second {
		t.Errorf("resolveBackend() RequeueAfter = %v, want 15s for a not-yet-Ready Backend", res.RequeueAfter)
	}
}

func TestResolveBackend_BackendReady(t *testing.T) {
	backend := &registryv1alpha1.RegistryBackend{
		ObjectMeta: metav1.ObjectMeta{Name: "rb-acme", Namespace: "registry-system"},
		Status: registryv1alpha1.RegistryBackendStatus{
			Phase:           phaseReady,
			AdminSecretName: "rb-acme-harbor-admin",
			RegistryURL:     "https://registry.acme.example.com",
		},
	}
	instance := &registryv1alpha1.RegistryInstance{
		ObjectMeta: metav1.ObjectMeta{Name: "ri-sample", Namespace: "registry-system"},
		Spec: registryv1alpha1.RegistryInstanceSpec{
			BackendRef: registryv1alpha1.BackendRef{Name: "rb-acme", Namespace: "registry-system"},
		},
	}
	fc := fake.NewClientBuilder().
		WithScheme(newTestScheme(t)).
		WithStatusSubresource(&registryv1alpha1.RegistryInstance{}).
		WithObjects(backend, instance).
		Build()

	r := newInstanceReconciler(t, fc)
	got, res, err := r.resolveBackend(context.Background(), instance)

	if err != nil {
		t.Fatalf("resolveBackend() err = %v, want nil", err)
	}
	if got == nil {
		t.Fatal("resolveBackend() backend = nil, want the Ready Backend to be returned")
	}
	if got.Status.RegistryURL != backend.Status.RegistryURL {
		t.Errorf("resolveBackend() returned backend with RegistryURL = %q, want %q", got.Status.RegistryURL, backend.Status.RegistryURL)
	}
	if res.RequeueAfter != 0 {
		t.Errorf("resolveBackend() RequeueAfter = %v, want 0 (no wait needed, proceed immediately)", res.RequeueAfter)
	}
}

func TestResolveBackend_ReadyPhaseButMissingSecretNameStillWaits(t *testing.T) {
	// Guards against a narrow race: Phase could theoretically read "Ready"
	// from a stale cache read before AdminSecretName was persisted. All three
	// fields are checked, not just Phase.
	backend := &registryv1alpha1.RegistryBackend{
		ObjectMeta: metav1.ObjectMeta{Name: "rb-acme", Namespace: "registry-system"},
		Status: registryv1alpha1.RegistryBackendStatus{
			Phase:       phaseReady,
			RegistryURL: "https://registry.acme.example.com",
			// AdminSecretName intentionally left empty
		},
	}
	instance := &registryv1alpha1.RegistryInstance{
		ObjectMeta: metav1.ObjectMeta{Name: "ri-sample", Namespace: "registry-system"},
		Spec: registryv1alpha1.RegistryInstanceSpec{
			BackendRef: registryv1alpha1.BackendRef{Name: "rb-acme", Namespace: "registry-system"},
		},
	}
	fc := fake.NewClientBuilder().
		WithScheme(newTestScheme(t)).
		WithStatusSubresource(&registryv1alpha1.RegistryInstance{}).
		WithObjects(backend, instance).
		Build()

	r := newInstanceReconciler(t, fc)
	got, _, err := r.resolveBackend(context.Background(), instance)
	if err != nil {
		t.Fatalf("resolveBackend() err = %v, want nil", err)
	}
	if got != nil {
		t.Error("resolveBackend() returned a backend despite AdminSecretName being empty")
	}
}

// --- fail(): the terminal-error path for spec-level errors ---

func TestFail_SetsFailedPhaseAndReturnsTerminalError(t *testing.T) {
	instance := &registryv1alpha1.RegistryInstance{
		ObjectMeta: metav1.ObjectMeta{Name: "ri-sample", Namespace: "registry-system"},
		Spec: registryv1alpha1.RegistryInstanceSpec{
			Plan: "not-a-real-plan",
		},
	}
	fc := fake.NewClientBuilder().
		WithScheme(newTestScheme(t)).
		WithStatusSubresource(&registryv1alpha1.RegistryInstance{}).
		WithObjects(instance).
		Build()

	r := newInstanceReconciler(t, fc)
	cause := errors.New(`unknown plan "not-a-real-plan"; valid: starter, professional, enterprise`)
	res, err := r.fail(context.Background(), instance, "resolve plan", cause)

	if res.Requeue || res.RequeueAfter != 0 {
		t.Errorf("fail() result = %+v, want a zero Result (TerminalError means the runtime must not requeue)", res)
	}
	if !errors.Is(err, reconcile.TerminalError(nil)) {
		t.Errorf("fail() err = %v, want a reconcile.TerminalError wrapping %v", err, cause)
	}
	if !errors.Is(err, cause) {
		t.Errorf("fail() err = %v, does not wrap the original cause %v", err, cause)
	}

	var fresh registryv1alpha1.RegistryInstance
	if err := fc.Get(context.Background(), client.ObjectKeyFromObject(instance), &fresh); err != nil {
		t.Fatalf("Get() after fail() error = %v", err)
	}
	if fresh.Status.Phase != phaseFailed {
		t.Errorf("fail() left Status.Phase = %q, want %q", fresh.Status.Phase, phaseFailed)
	}
}

// --- projectQuotaBytes: plan-name validation delegates to helm.PlanFor ---

func TestProjectQuotaBytes(t *testing.T) {
	tests := []struct {
		plan    string
		want    int64
		wantErr bool
	}{
		{"starter", 5 * 1024 * 1024 * 1024, false},
		{"professional", 20 * 1024 * 1024 * 1024, false},
		{"enterprise", 100 * 1024 * 1024 * 1024, false},
		{"not-a-real-plan", 0, true},
		{"", 0, true},
	}
	for _, tt := range tests {
		t.Run(tt.plan, func(t *testing.T) {
			got, err := projectQuotaBytes(tt.plan)
			if (err != nil) != tt.wantErr {
				t.Fatalf("projectQuotaBytes(%q) error = %v, wantErr %v", tt.plan, err, tt.wantErr)
			}
			if !tt.wantErr && got != tt.want {
				t.Errorf("projectQuotaBytes(%q) = %d, want %d", tt.plan, got, tt.want)
			}
		})
	}
}

// --- pure helper functions ---

func TestProjectNameFor(t *testing.T) {
	tests := []struct {
		name string
		cr   *registryv1alpha1.RegistryInstance
		want string
	}{
		{
			name: "explicit RegistryName wins",
			cr: &registryv1alpha1.RegistryInstance{Spec: registryv1alpha1.RegistryInstanceSpec{
				RegistryName: "my-registry", ProjectID: "proj-123",
			}},
			want: "my-registry",
		},
		{
			name: "falls back to ProjectID when RegistryName is unset",
			cr: &registryv1alpha1.RegistryInstance{Spec: registryv1alpha1.RegistryInstanceSpec{
				ProjectID: "proj-123",
			}},
			want: "proj-123",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := projectNameFor(tt.cr); got != tt.want {
				t.Errorf("projectNameFor() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestCredentialsSecretName(t *testing.T) {
	cr := &registryv1alpha1.RegistryInstance{ObjectMeta: metav1.ObjectMeta{Name: "ri-sample"}}
	want := "registry-credentials-ri-sample"
	if got := credentialsSecretName(cr); got != want {
		t.Errorf("credentialsSecretName() = %q, want %q", got, want)
	}
}

func TestShortSuffix(t *testing.T) {
	tests := []struct {
		name string
		want string
	}{
		{"ri-sample", "ri-sample"},
		{"RI-Sample-Mixed-Case", "ri-sample-mi"},         // truncated to 12 chars, then lowercased
		{"exactly12chr", "exactly12chr"},                 // exactly 12 chars, unchanged
		{"thisIsWayMoreThanTwelveChars", "thisiswaymor"}, // truncated to 12
	}
	for _, tt := range tests {
		cr := &registryv1alpha1.RegistryInstance{ObjectMeta: metav1.ObjectMeta{Name: tt.name}}
		if got := shortSuffix(cr); got != tt.want {
			t.Errorf("shortSuffix(%q) = %q, want %q", tt.name, got, tt.want)
		}
	}
}
