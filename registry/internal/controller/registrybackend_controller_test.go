package controller

import (
	"context"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	registryv1alpha1 "github.com/wso2/open-cloud-datacenter/crds/registry/api/v1alpha1"
)

func newTestScheme(t *testing.T) *runtime.Scheme {
	s := runtime.NewScheme()
	if err := scheme.AddToScheme(s); err != nil {
		t.Fatalf("adding client-go scheme: %v", err)
	}
	if err := registryv1alpha1.AddToScheme(s); err != nil {
		t.Fatalf("adding registry v1alpha1 scheme: %v", err)
	}
	return s
}

func newBackendReconciler(t *testing.T, fc client.WithWatch) *RegistryBackendReconciler {
	return &RegistryBackendReconciler{
		Client:   fc,
		Scheme:   newTestScheme(t),
		Recorder: record.NewFakeRecorder(32),
	}
}

// --- patchStatus: optimistic-concurrency retry loop ---

func TestPatchStatus_RetriesOnceOnConflictThenSucceeds(t *testing.T) {
	cr := &registryv1alpha1.RegistryBackend{
		ObjectMeta: metav1.ObjectMeta{Name: "rb-acme", Namespace: "registry-system"},
		Spec:       registryv1alpha1.RegistryBackendSpec{TenantID: "acme"},
	}

	updateCalls := 0
	fc := fake.NewClientBuilder().
		WithScheme(newTestScheme(t)).
		WithStatusSubresource(&registryv1alpha1.RegistryBackend{}).
		WithObjects(cr).
		WithInterceptorFuncs(interceptor.Funcs{
			SubResourceUpdate: func(ctx context.Context, c client.Client, subResourceName string, obj client.Object, opts ...client.SubResourceUpdateOption) error {
				updateCalls++
				if updateCalls == 1 {
					gvr := schema.GroupResource{Group: registryv1alpha1.GroupVersion.Group, Resource: "registrybackends"}
					return apierrors.NewConflict(gvr, obj.GetName(), nil)
				}
				return c.Status().Update(ctx, obj, opts...)
			},
		}).
		Build()

	r := newBackendReconciler(t, fc)
	key := client.ObjectKeyFromObject(cr)

	err := r.patchStatus(context.Background(), key, func(s *registryv1alpha1.RegistryBackendStatus) {
		s.Phase = phaseReady
		s.Message = "Harbor is running"
	})
	if err != nil {
		t.Fatalf("patchStatus() error = %v, want nil (should succeed on the retry)", err)
	}
	if updateCalls != 2 {
		t.Errorf("Status().Update was called %d times, want exactly 2 (one conflict + one success)", updateCalls)
	}

	var fresh registryv1alpha1.RegistryBackend
	if err := fc.Get(context.Background(), key, &fresh); err != nil {
		t.Fatalf("Get() after patchStatus error = %v", err)
	}
	if fresh.Status.Phase != phaseReady {
		t.Errorf("Status.Phase = %q after patchStatus, want %q", fresh.Status.Phase, phaseReady)
	}
}

func TestPatchStatus_ExhaustsRetriesAndReturnsError(t *testing.T) {
	cr := &registryv1alpha1.RegistryBackend{
		ObjectMeta: metav1.ObjectMeta{Name: "rb-acme", Namespace: "registry-system"},
	}

	updateCalls := 0
	fc := fake.NewClientBuilder().
		WithScheme(newTestScheme(t)).
		WithStatusSubresource(&registryv1alpha1.RegistryBackend{}).
		WithObjects(cr).
		WithInterceptorFuncs(interceptor.Funcs{
			SubResourceUpdate: func(ctx context.Context, c client.Client, subResourceName string, obj client.Object, opts ...client.SubResourceUpdateOption) error {
				updateCalls++
				gvr := schema.GroupResource{Group: registryv1alpha1.GroupVersion.Group, Resource: "registrybackends"}
				return apierrors.NewConflict(gvr, obj.GetName(), nil)
			},
		}).
		Build()

	r := newBackendReconciler(t, fc)
	err := r.patchStatus(context.Background(), client.ObjectKeyFromObject(cr), func(s *registryv1alpha1.RegistryBackendStatus) {
		s.Phase = phaseReady
	})
	if err == nil {
		t.Fatal("patchStatus() error = nil, want an error after exhausting retries on permanent conflicts")
	}
	if !strings.Contains(err.Error(), "too many conflicts") {
		t.Errorf("patchStatus() error = %q, want it to mention %q", err.Error(), "too many conflicts")
	}
	// for attempt := 0; attempt < 2; attempt++ means exactly 2 Update attempts, never a 3rd.
	if updateCalls != 2 {
		t.Errorf("Status().Update was called %d times, want exactly 2 (the loop caps at 2 attempts)", updateCalls)
	}
}

// --- handleDelete: deletion blocked while dependent RegistryInstances exist ---

func TestHandleDelete_BlocksWhileDependentInstanceExists(t *testing.T) {
	cr := &registryv1alpha1.RegistryBackend{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "rb-acme",
			Namespace:         "registry-system",
			Finalizers:        []string{backendFinalizer},
			DeletionTimestamp: &metav1.Time{Time: time.Now()},
		},
		Spec: registryv1alpha1.RegistryBackendSpec{TenantID: "acme"},
	}
	blockingInstance := &registryv1alpha1.RegistryInstance{
		ObjectMeta: metav1.ObjectMeta{Name: "ri-sample", Namespace: "registry-system"},
		Spec: registryv1alpha1.RegistryInstanceSpec{
			BackendRef: registryv1alpha1.BackendRef{Name: "rb-acme", Namespace: "registry-system"},
		},
	}

	fc := fake.NewClientBuilder().
		WithScheme(newTestScheme(t)).
		WithStatusSubresource(&registryv1alpha1.RegistryBackend{}).
		WithObjects(cr, blockingInstance).
		Build()

	r := newBackendReconciler(t, fc)
	res, err := r.handleDelete(context.Background(), cr, logf.Log)
	if err != nil {
		t.Fatalf("handleDelete() error = %v, want nil (blocked is not an error, it's a requeue)", err)
	}
	if res.RequeueAfter != 15*time.Second {
		t.Errorf("handleDelete() RequeueAfter = %v, want 15s", res.RequeueAfter)
	}

	var fresh registryv1alpha1.RegistryBackend
	if err := fc.Get(context.Background(), client.ObjectKeyFromObject(cr), &fresh); err != nil {
		t.Fatalf("Get() after handleDelete error = %v", err)
	}
	if fresh.Status.Phase != phaseTerminating {
		t.Errorf("Status.Phase = %q, want %q while blocked", fresh.Status.Phase, phaseTerminating)
	}
	// The finalizer must still be present — the object was never allowed to
	// actually finish deleting while a dependent RegistryInstance exists.
	found := false
	for _, f := range fresh.Finalizers {
		if f == backendFinalizer {
			found = true
		}
	}
	if !found {
		t.Error("finalizer was removed despite an unresolved blocker — deletion should never have been allowed to proceed")
	}
}

func TestHandleDelete_NoFinalizerIsNoOp(t *testing.T) {
	// If the finalizer was already removed (e.g. a previous reconcile already
	// finished cleanup), handleDelete must do nothing further rather than
	// re-running cleanup steps.
	//
	// Note: a real API server (and the fake client, deliberately emulating
	// it) refuses to ever persist an object with a DeletionTimestamp set but
	// zero finalizers — the moment the last finalizer clears, the object is
	// purged immediately. So this exact combination can't be seeded via
	// WithObjects; it only exists as this function's own in-memory argument.
	// That in turn means this early-return guard is defensive against a state
	// the real API server can never actually hand back to Reconcile.
	cr := &registryv1alpha1.RegistryBackend{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "rb-acme",
			Namespace:         "registry-system",
			DeletionTimestamp: &metav1.Time{Time: time.Now()},
			// no finalizers
		},
	}
	fc := fake.NewClientBuilder().
		WithScheme(newTestScheme(t)).
		WithStatusSubresource(&registryv1alpha1.RegistryBackend{}).
		Build()

	r := newBackendReconciler(t, fc)
	res, err := r.handleDelete(context.Background(), cr, logf.Log)
	if err != nil {
		t.Fatalf("handleDelete() error = %v, want nil", err)
	}
	if res.RequeueAfter != 0 {
		t.Errorf("handleDelete() RequeueAfter = %v, want 0 (no more work to do)", res.RequeueAfter)
	}
}

// --- deletePVCs: only PVCs matching this tenant's Helm-instance label are touched ---

func TestDeletePVCs_OnlyDeletesMatchingLabeledPVCs(t *testing.T) {
	cr := &registryv1alpha1.RegistryBackend{
		Spec: registryv1alpha1.RegistryBackendSpec{TenantID: "acme"},
	}
	ns := "acme-management"

	matching := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "data-harbor-acme-database-0",
			Namespace: ns,
			Labels:    map[string]string{"app.kubernetes.io/instance": "harbor-acme"},
		},
	}
	otherTenant := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "data-harbor-beta-database-0",
			Namespace: ns,
			Labels:    map[string]string{"app.kubernetes.io/instance": "harbor-beta"},
		},
	}
	unlabeled := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "some-unrelated-pvc",
			Namespace: ns,
		},
	}

	fc := fake.NewClientBuilder().
		WithScheme(newTestScheme(t)).
		WithObjects(matching, otherTenant, unlabeled).
		Build()

	r := newBackendReconciler(t, fc)
	if err := r.deletePVCs(context.Background(), cr); err != nil {
		t.Fatalf("deletePVCs() error = %v", err)
	}

	assertExists := func(name string, wantExists bool) {
		var pvc corev1.PersistentVolumeClaim
		err := fc.Get(context.Background(), client.ObjectKey{Namespace: ns, Name: name}, &pvc)
		exists := err == nil
		if !wantExists && !apierrors.IsNotFound(err) && err != nil {
			t.Fatalf("unexpected error checking %q: %v", name, err)
		}
		if exists != wantExists {
			t.Errorf("PVC %q exists = %v, want %v", name, exists, wantExists)
		}
	}
	assertExists(matching.Name, false)
	assertExists(otherTenant.Name, true)
	assertExists(unlabeled.Name, true)
}
