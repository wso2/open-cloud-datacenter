package controller

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/events"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	registryv1alpha1 "github.com/wso2/open-cloud-datacenter/crds/registry/api/v1alpha1"
	"github.com/wso2/open-cloud-datacenter/crds/registry/internal/helm"
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
		Recorder: events.NewFakeRecorder(32),
		Helm:     &fakeDeployer{},
	}
}

// fakeDeployer records the release calls the reconciler makes, standing in for
// the real Helm client so the delete path can run without a cluster.
type fakeDeployer struct {
	installed    []string
	uninstalled  []string
	uninstallErr error
}

func (f *fakeDeployer) Install(_ context.Context, namespace string, _ []byte) error {
	f.installed = append(f.installed, namespace)
	return nil
}

func (f *fakeDeployer) Uninstall(_ context.Context, namespace string) error {
	if f.uninstallErr != nil {
		return f.uninstallErr
	}
	f.uninstalled = append(f.uninstalled, namespace)
	return nil
}

// --- patchStatus: optimistic-concurrency retry loop ---

func TestPatchStatus_RetriesOnceOnConflictThenSucceeds(t *testing.T) {
	cr := &registryv1alpha1.RegistryBackend{
		ObjectMeta: metav1.ObjectMeta{Name: backendName, Namespace: "acme-project-1"},
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
		ObjectMeta: metav1.ObjectMeta{Name: backendName, Namespace: "acme-project-1"},
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

// --- handleDelete: deletion blocked while dependent Registries exist ---

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
			Name:              backendName,
			Namespace:         "acme-project-1",
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

// --- PVC selection: Harbor shares its namespace with the user's workloads ---

const testNS = "acme-project-1"

// harborPVC builds a claim carrying the labels Harbor's chart really applies.
// Copied from a live release rather than assumed: the chart uses the LEGACY
// Helm convention (app/release/component/heritage), NOT
// app.kubernetes.io/instance. Getting this wrong makes the selector match
// nothing while every test still passes.
func harborPVC(name, component string) *corev1.PersistentVolumeClaim {
	return &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: testNS,
			Labels: map[string]string{
				"app":       "harbor",
				"release":   helm.ReleaseName(testNS),
				"component": component,
				"heritage":  "Helm",
			},
		},
	}
}

func backendIn(namespace string) *registryv1alpha1.RegistryBackend {
	return &registryv1alpha1.RegistryBackend{
		ObjectMeta: metav1.ObjectMeta{Name: backendName, Namespace: namespace},
	}
}

func TestDeletePVCs_OnlyDeletesThisReleasesPVCs(t *testing.T) {
	matching := harborPVC("database-data-harbor-acme-project-1-database-0", "database")
	// A claim from an unrelated workload sharing the namespace: selecting on
	// namespace alone would destroy user data here.
	userWorkload := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "postgres-data",
			Namespace: testNS,
			Labels:    map[string]string{"app": "my-app", "release": "my-app"},
		},
	}
	unlabeled := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{Name: "some-unrelated-pvc", Namespace: testNS},
	}

	fc := fake.NewClientBuilder().
		WithScheme(newTestScheme(t)).
		WithObjects(matching, userWorkload, unlabeled).
		Build()

	r := newBackendReconciler(t, fc)
	if err := r.deletePVCs(context.Background(), backendIn(testNS)); err != nil {
		t.Fatalf("deletePVCs() error = %v", err)
	}

	assertExists := func(name string, wantExists bool) {
		var pvc corev1.PersistentVolumeClaim
		err := fc.Get(context.Background(), client.ObjectKey{Namespace: testNS, Name: name}, &pvc)
		exists := err == nil
		if !wantExists && !apierrors.IsNotFound(err) && err != nil {
			t.Fatalf("unexpected error checking %q: %v", name, err)
		}
		if exists != wantExists {
			t.Errorf("PVC %q exists = %v, want %v", name, exists, wantExists)
		}
	}
	assertExists(matching.Name, false)
	assertExists(userWorkload.Name, true)
	assertExists(unlabeled.Name, true)
}

// Harbor's PVCs are matched by a name suffix, which is only unambiguous within
// its own release. A user claim in the same namespace that happens to end in
// "-registry" must not be resized.
func TestEnsureStorageSize_IgnoresPVCsOutsideTheHarborRelease(t *testing.T) {
	foreign := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{Name: "my-app-registry", Namespace: testNS},
		Spec: corev1.PersistentVolumeClaimSpec{
			Resources: corev1.VolumeResourceRequirements{
				Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("1Gi")},
			},
		},
	}

	fc := fake.NewClientBuilder().WithScheme(newTestScheme(t)).WithObjects(foreign).Build()

	r := newBackendReconciler(t, fc)
	plan := helm.SizePlan{RegistryStorage: "50Gi", DBStorage: "5Gi"}
	if err := r.ensureStorageSize(context.Background(), backendIn(testNS), plan); err != nil {
		t.Fatalf("ensureStorageSize() error = %v, want nil", err)
	}

	var got corev1.PersistentVolumeClaim
	if err := fc.Get(context.Background(), client.ObjectKeyFromObject(foreign), &got); err != nil {
		t.Fatalf("Get() after ensureStorageSize error = %v", err)
	}
	want := resource.MustParse("1Gi")
	if gotQty := got.Spec.Resources.Requests[corev1.ResourceStorage]; gotQty.Cmp(want) != 0 {
		t.Errorf("unrelated PVC was resized to %s, want it left at %s", gotQty.String(), want.String())
	}
}

// --- ensureStorageSize: growing a PVC with no existing Requests map ---

func TestEnsureStorageSize_HandlesNilRequestsMapWithoutPanicking(t *testing.T) {
	// No Spec.Resources.Requests set at all — Requests is a nil map, the
	// exact shape that panics on a plain map-index assignment.
	pvc := harborPVC("harbor-acme-project-1-registry", "registry")

	fc := fake.NewClientBuilder().
		WithScheme(newTestScheme(t)).
		WithObjects(pvc).
		Build()

	r := newBackendReconciler(t, fc)
	plan := helm.SizePlan{RegistryStorage: "50Gi", DBStorage: "5Gi"}
	if err := r.ensureStorageSize(context.Background(), backendIn(testNS), plan); err != nil {
		t.Fatalf("ensureStorageSize() error = %v, want nil", err)
	}

	var got corev1.PersistentVolumeClaim
	if err := fc.Get(context.Background(), client.ObjectKeyFromObject(pvc), &got); err != nil {
		t.Fatalf("Get() after ensureStorageSize error = %v", err)
	}
	want := resource.MustParse("50Gi")
	if gotQty := got.Spec.Resources.Requests[corev1.ResourceStorage]; gotQty.Cmp(want) != 0 {
		t.Errorf("PVC storage request = %s, want %s", gotQty.String(), want.String())
	}
}

// --- ensureAdminSecret: never regenerates over an existing Secret ---

// Every reconcile re-runs ensureAdminSecret, so it must return the stored values
// rather than fresh ones. Regenerating would leave the Postgres volume holding
// the previous password in its data files, and Harbor's core could never
// authenticate to its own database again.
func TestEnsureAdminSecret_ReusesStoredValuesInsteadOfRegenerating(t *testing.T) {
	cr := backendIn(testNS)
	fc := fake.NewClientBuilder().WithScheme(newTestScheme(t)).WithObjects(cr).Build()
	r := newBackendReconciler(t, fc)

	first, name, err := r.ensureAdminSecret(context.Background(), cr)
	if err != nil {
		t.Fatalf("ensureAdminSecret() first call error = %v", err)
	}
	if first.AdminPass == "" || first.DBPass == "" || first.EncryptionKey == "" {
		t.Fatal("first call left a credential empty")
	}

	// Second call stands in for any later reconcile: same namespace, same fixed
	// backend name, so the same Secret is found.
	second, secondName, err := r.ensureAdminSecret(context.Background(), cr)
	if err != nil {
		t.Fatalf("ensureAdminSecret() second call error = %v", err)
	}
	if secondName != name {
		t.Errorf("secret name changed between calls: %q then %q", name, secondName)
	}
	if second != first {
		t.Errorf("credentials were regenerated over an existing Secret:\n first  = %+v\n second = %+v", first, second)
	}
}

// A key added to harborSecretGenerators after the Secret was first created must
// be filled in, without disturbing any key already there — self-heal must never
// become silent rotation of a live credential.
func TestEnsureAdminSecret_FillsMissingKeyWithoutRotatingExistingOnes(t *testing.T) {
	cr := backendIn(testNS)
	partial := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: cr.Name + "-harbor-admin", Namespace: testNS},
		Data:       map[string][]byte{"HARBOR_ADMIN_PASSWORD": []byte("Aa1-preexisting-admin-pass")},
	}
	fc := fake.NewClientBuilder().WithScheme(newTestScheme(t)).WithObjects(cr, partial).Build()
	r := newBackendReconciler(t, fc)

	got, _, err := r.ensureAdminSecret(context.Background(), cr)
	if err != nil {
		t.Fatalf("ensureAdminSecret() error = %v", err)
	}
	if got.AdminPass != "Aa1-preexisting-admin-pass" {
		t.Errorf("existing admin password was rotated: got %q", got.AdminPass)
	}
	if got.DBPass == "" || got.EncryptionKey == "" || got.CoreSecret == "" {
		t.Errorf("missing keys were not generated: %+v", got)
	}
}

// --- deletion: blocked by default, cascades with force ---

// Without the annotation the delete must be refused, and the refusal has to
// name the override — a block that doesn't say how to proceed just looks broken.
func TestHandleDelete_BlocksAndNamesTheOverride(t *testing.T) {
	cr := backendTerminating(nil)
	fc := fake.NewClientBuilder().WithScheme(newTestScheme(t)).
		WithStatusSubresource(&registryv1alpha1.RegistryBackend{}).
		WithObjects(cr, registryIn(testNS, "web")).
		Build()

	r := newBackendReconciler(t, fc)
	res, err := r.handleDelete(context.Background(), cr, logf.Log)
	if err != nil {
		t.Fatalf("handleDelete() error = %v, want nil (blocked is a requeue, not an error)", err)
	}
	if res.RequeueAfter != 15*time.Second {
		t.Errorf("RequeueAfter = %v, want 15s", res.RequeueAfter)
	}

	var fresh registryv1alpha1.RegistryBackend
	if err := fc.Get(context.Background(), client.ObjectKeyFromObject(cr), &fresh); err != nil {
		t.Fatalf("Get() = %v", err)
	}
	if fresh.Status.Phase != phaseTerminating {
		t.Errorf("Phase = %q, want %q", fresh.Status.Phase, phaseTerminating)
	}
	if !strings.Contains(fresh.Status.Message, forceAnnotation) {
		t.Errorf("block message does not name the override: %q", fresh.Status.Message)
	}
	if len(fresh.Finalizers) == 0 {
		t.Error("finalizer removed despite an unresolved blocker")
	}
	// The Registry must be untouched — blocking protects it, it does not delete it.
	var regs registryv1alpha1.RegistryList
	_ = fc.List(context.Background(), &regs, client.InNamespace(testNS))
	if len(regs.Items) != 1 {
		t.Errorf("a blocked delete removed Registries: %d left, want 1", len(regs.Items))
	}
}

// With force=true the backend cascades. Deleting the Registries is a
// correctness requirement, not convenience: a survivor reconciles, calls
// bindBackend, finds no backend and creates one — resurrecting the Harbor.
func TestHandleDelete_ForceCascades(t *testing.T) {
	cr := backendTerminating(map[string]string{forceAnnotation: "true"})
	fc := fake.NewClientBuilder().WithScheme(newTestScheme(t)).
		WithStatusSubresource(&registryv1alpha1.RegistryBackend{}).
		WithObjects(cr, registryIn(testNS, "web"), registryIn(testNS, "api"),
			registryIn("other-ns", "web")). // different Harbor; must be untouched
		Build()

	r := newBackendReconciler(t, fc)
	if _, err := r.handleDelete(context.Background(), cr, logf.Log); err != nil {
		t.Fatalf("handleDelete() error = %v", err)
	}

	var inNS registryv1alpha1.RegistryList
	_ = fc.List(context.Background(), &inNS, client.InNamespace(testNS))
	if len(inNS.Items) != 0 {
		t.Errorf("left %d Registry(s) behind; they would recreate the backend", len(inNS.Items))
	}
	var other registryv1alpha1.RegistryList
	_ = fc.List(context.Background(), &other, client.InNamespace("other-ns"))
	if len(other.Items) != 1 {
		t.Errorf("cascade escaped its namespace: other-ns has %d, want 1", len(other.Items))
	}
}

// Only "true" arms it; anything else leaves the guard in place.
func TestHandleDelete_NonTrueForceValuesStillBlock(t *testing.T) {
	for _, v := range []string{"", "yes", "1", "True", "TRUE"} {
		cr := backendTerminating(map[string]string{forceAnnotation: v})
		fc := fake.NewClientBuilder().WithScheme(newTestScheme(t)).
			WithStatusSubresource(&registryv1alpha1.RegistryBackend{}).
			WithObjects(cr, registryIn(testNS, "web")).
			Build()
		if _, err := newBackendReconciler(t, fc).handleDelete(context.Background(), cr, logf.Log); err != nil {
			t.Fatalf("handleDelete(%q) error = %v", v, err)
		}
		var regs registryv1alpha1.RegistryList
		_ = fc.List(context.Background(), &regs, client.InNamespace(testNS))
		if len(regs.Items) != 1 {
			t.Errorf("force=%q armed the cascade; only \"true\" should", v)
		}
	}
}

// The terminal delete path. Every other delete test returns before the release
// is touched — blocked by a dependent, or short-circuited by a missing finalizer
// — so this is what covers the reclaim itself: uninstall, then the volumes and
// the credential Secret, then the finalizer.
func TestHandleDelete_IdleBackendUninstallsAndReclaims(t *testing.T) {
	cr := backendTerminating(nil)
	pvc := harborPVC("database-data-harbor-acme-project-1-database-0", "database")
	sec := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{
		Name:      cr.Name + "-harbor-admin",
		Namespace: testNS,
	}}

	fc := fake.NewClientBuilder().WithScheme(newTestScheme(t)).
		WithStatusSubresource(&registryv1alpha1.RegistryBackend{}).
		WithObjects(cr, pvc, sec).
		Build()

	r := newBackendReconciler(t, fc)
	fd := r.Helm.(*fakeDeployer)

	if _, err := r.handleDelete(context.Background(), cr, logf.Log); err != nil {
		t.Fatalf("handleDelete() error = %v", err)
	}

	if len(fd.uninstalled) != 1 || fd.uninstalled[0] != testNS {
		t.Errorf("uninstalled = %v, want exactly [%s]", fd.uninstalled, testNS)
	}

	ctx := context.Background()
	var gotPVC corev1.PersistentVolumeClaim
	if err := fc.Get(ctx, client.ObjectKeyFromObject(pvc), &gotPVC); !apierrors.IsNotFound(err) {
		t.Errorf("Harbor PVC survived the delete (err = %v); the chart keeps volumes, "+
			"so nothing else reclaims them", err)
	}
	var gotSec corev1.Secret
	if err := fc.Get(ctx, client.ObjectKeyFromObject(sec), &gotSec); !apierrors.IsNotFound(err) {
		t.Errorf("credential Secret survived the delete (err = %v)", err)
	}
	if controllerutil.ContainsFinalizer(cr, backendFinalizer) {
		t.Error("finalizer still held after a completed reclaim; the object would never go away")
	}
}

// A failed uninstall must hold the finalizer rather than release it, or the
// backend would disappear while its release stayed installed.
func TestHandleDelete_FailedUninstallKeepsTheFinalizer(t *testing.T) {
	cr := backendTerminating(nil)
	fc := fake.NewClientBuilder().WithScheme(newTestScheme(t)).
		WithStatusSubresource(&registryv1alpha1.RegistryBackend{}).
		WithObjects(cr).
		Build()

	r := newBackendReconciler(t, fc)
	r.Helm = &fakeDeployer{uninstallErr: errors.New("tiller unreachable")}

	if _, err := r.handleDelete(context.Background(), cr, logf.Log); err == nil {
		t.Fatal("handleDelete() returned nil error despite a failed uninstall")
	}
	if !controllerutil.ContainsFinalizer(cr, backendFinalizer) {
		t.Error("finalizer released after a failed uninstall; the release would be orphaned")
	}
}

// backendTerminating builds a backend mid-delete, optionally annotated.
func backendTerminating(annotations map[string]string) *registryv1alpha1.RegistryBackend {
	return &registryv1alpha1.RegistryBackend{
		ObjectMeta: metav1.ObjectMeta{
			Name: backendName, Namespace: testNS,
			Annotations:       annotations,
			Finalizers:        []string{backendFinalizer},
			DeletionTimestamp: &metav1.Time{Time: time.Now()},
		},
	}
}
