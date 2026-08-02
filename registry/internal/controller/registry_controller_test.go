package controller

import (
	"context"
	"testing"
	"time"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	registryv1alpha1 "github.com/wso2/open-cloud-datacenter/crds/registry/api/v1alpha1"
)

// testLogger is a discarding logger for functions that take one.
func testLogger() logr.Logger { return logr.Discard() }

func newRegistryReconciler(t *testing.T, fc client.WithWatch) *RegistryReconciler {
	return &RegistryReconciler{
		Client:   fc,
		Scheme:   newTestScheme(t),
		Recorder: record.NewFakeRecorder(64),
	}
}

// namespaceInProject builds a namespace carrying the project assignment Rancher
// would have given it.
func namespaceInProject(name, projectID string) *corev1.Namespace {
	return &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{
		Name:        name,
		Annotations: map[string]string{tenantProjectKey: "c-m-test:" + projectID},
	}}
}

func registryIn(namespace, name string) *registryv1alpha1.Registry {
	return &registryv1alpha1.Registry{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Spec:       registryv1alpha1.RegistrySpec{Plan: "starter", ReclaimPolicy: reclaimRetain},
	}
}

func newFakeClient(t *testing.T, objs ...client.Object) client.WithWatch {
	return fake.NewClientBuilder().
		WithScheme(newTestScheme(t)).
		WithStatusSubresource(&registryv1alpha1.Registry{}, &registryv1alpha1.RegistryBackend{}).
		WithObjects(objs...).
		Build()
}

func listBackends(t *testing.T, fc client.WithWatch) []registryv1alpha1.RegistryBackend {
	t.Helper()
	var l registryv1alpha1.RegistryBackendList
	if err := fc.List(context.Background(), &l); err != nil {
		t.Fatalf("list backends: %v", err)
	}
	return l.Items
}

// The tenant's first Registry has to bring Harbor into being, since nobody
// creates a backend by hand.
func TestBindBackend_FirstRegistryProvisionsHarbor(t *testing.T) {
	reg := registryIn("acme-project-1", "web")
	fc := newFakeClient(t, namespaceInProject("acme-project-1", "p-acme"), reg)
	r := newRegistryReconciler(t, fc)

	backend, res, err := r.bindBackend(context.Background(), reg)
	if err != nil {
		t.Fatalf("bindBackend() error = %v", err)
	}
	if backend != nil {
		t.Error("bindBackend() returned a backend on the pass that created it; Harbor is not ready yet")
	}
	if res.RequeueAfter == 0 {
		t.Error("bindBackend() did not requeue while Harbor provisions")
	}

	backends := listBackends(t, fc)
	if len(backends) != 1 {
		t.Fatalf("got %d backends, want exactly 1", len(backends))
	}
	got := backends[0]
	if got.Name != "rb-p-acme" {
		t.Errorf("backend name = %q, want rb-p-acme", got.Name)
	}
	if got.Spec.TenantID != "p-acme" {
		t.Errorf("backend tenantID = %q, want p-acme", got.Spec.TenantID)
	}
	if got.Spec.Plan != "starter" {
		t.Errorf("backend plan = %q, want the smallest plan", got.Spec.Plan)
	}
	if got.Spec.ReclaimPolicy != reclaimRetain {
		t.Errorf("backend reclaimPolicy = %q, want Retain so images survive", got.Spec.ReclaimPolicy)
	}
	if !got.Spec.Autoscale.Enabled {
		t.Error("autoscale should be enabled on an operator-created backend")
	}
	if got.Labels[tenantLabel] != "p-acme" {
		t.Errorf("backend tenant label = %q, want p-acme", got.Labels[tenantLabel])
	}
}

// A second namespace in the same tenant must reuse the Harbor already running,
// not provision another one.
func TestBindBackend_SecondRegistryReusesSameHarbor(t *testing.T) {
	existing := readyBackend("rb-p-acme", "p-acme")
	reg := registryIn("acme-project-2", "api")
	fc := newFakeClient(t, namespaceInProject("acme-project-2", "p-acme"), existing, reg)
	r := newRegistryReconciler(t, fc)

	backend, _, err := r.bindBackend(context.Background(), reg)
	if err != nil {
		t.Fatalf("bindBackend() error = %v", err)
	}
	if backend == nil {
		t.Fatal("bindBackend() returned nil for a Ready backend")
	}
	if backend.Name != "rb-p-acme" {
		t.Errorf("bound to %q, want the tenant's existing rb-p-acme", backend.Name)
	}
	if n := len(listBackends(t, fc)); n != 1 {
		t.Errorf("got %d backends, want 1 — a second Registry must not provision another Harbor", n)
	}
}

// Isolation: the tenant is read from the namespace, so a Registry in one tenant
// can never resolve to another tenant's Harbor.
func TestBindBackend_CannotReachAnotherTenantsHarbor(t *testing.T) {
	tenantA := readyBackend("rb-p-a", "p-a")
	tenantB := readyBackend("rb-p-b", "p-b")
	reg := registryIn("b-project-1", "web")

	fc := newFakeClient(t, namespaceInProject("b-project-1", "p-b"), tenantA, tenantB, reg)
	r := newRegistryReconciler(t, fc)

	backend, _, err := r.bindBackend(context.Background(), reg)
	if err != nil {
		t.Fatalf("bindBackend() error = %v", err)
	}
	if backend == nil {
		t.Fatal("bindBackend() returned nil")
	}
	if backend.Spec.TenantID != "p-b" {
		t.Errorf("Registry in tenant p-b bound to tenant %q — cross-tenant binding", backend.Spec.TenantID)
	}
}

// A namespace outside any project has no tenant. Waiting is the only safe
// answer; defaulting would hand it someone else's registry.
func TestBindBackend_NamespaceWithoutProjectWaits(t *testing.T) {
	reg := registryIn("orphan-ns", "web")
	fc := newFakeClient(t, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "orphan-ns"}}, reg)
	r := newRegistryReconciler(t, fc)

	backend, res, err := r.bindBackend(context.Background(), reg)
	if err != nil {
		t.Fatalf("bindBackend() error = %v, want nil (a wait, not a failure)", err)
	}
	if backend != nil {
		t.Error("bindBackend() bound a Registry whose namespace belongs to no project")
	}
	if res.RequeueAfter == 0 {
		t.Error("bindBackend() did not requeue to wait for project assignment")
	}
	if n := len(listBackends(t, fc)); n != 0 {
		t.Errorf("created %d backends for an unassigned namespace, want 0", n)
	}
}

// Two namespaces in one tenant racing their first Registry must still end up
// with a single Harbor; the derived name lets the API server settle it.
func TestBindBackend_ConcurrentFirstRegistriesCreateOneHarbor(t *testing.T) {
	regA := registryIn("acme-project-1", "web")
	regB := registryIn("acme-project-2", "api")
	fc := newFakeClient(t,
		namespaceInProject("acme-project-1", "p-acme"),
		namespaceInProject("acme-project-2", "p-acme"),
		regA, regB)
	r := newRegistryReconciler(t, fc)

	if _, _, err := r.bindBackend(context.Background(), regA); err != nil {
		t.Fatalf("first bindBackend() error = %v", err)
	}
	// The second sees the object the first created and must adopt it rather than
	// erroring on AlreadyExists.
	if _, _, err := r.bindBackend(context.Background(), regB); err != nil {
		t.Fatalf("second bindBackend() error = %v, want it to adopt the existing backend", err)
	}
	if n := len(listBackends(t, fc)); n != 1 {
		t.Errorf("got %d backends, want exactly 1", n)
	}
}

// Harbor treats a create of an existing project as success, so two Registries
// resolving to one project name would silently share images. Deriving the name
// from the namespace makes that impossible.
func TestHarborProjectName_UniquePerNamespaceAndRegistry(t *testing.T) {
	same := harborProjectName(registryIn("acme-project-1", "web"))
	if same != "acme-project-1-web" {
		t.Errorf("harborProjectName() = %q, want acme-project-1-web", same)
	}

	seen := map[string]string{}
	for _, c := range []struct{ ns, name string }{
		{"acme-project-1", "web"},
		{"acme-project-1", "api"}, // several registries in one namespace
		{"acme-project-2", "web"}, // same registry name, different namespace
		{"beta-project-1", "web"}, // different tenant entirely
	} {
		reg := registryIn(c.ns, c.name)
		got := harborProjectName(reg)
		if prev, dup := seen[got]; dup {
			t.Errorf("%s/%s and %s collide on Harbor project %q", c.ns, c.name, prev, got)
		}
		seen[got] = c.ns + "/" + c.name
	}
}

// Deleting a Registry that never bound must not hang: there is no Harbor project
// to remove.
func TestHandleDelete_UnboundRegistryReleasesImmediately(t *testing.T) {
	reg := registryIn("acme-project-1", "web")
	reg.Spec.ReclaimPolicy = reclaimDelete
	reg.Finalizers = []string{registryFinalizer}
	reg.DeletionTimestamp = &metav1.Time{Time: time.Now()}

	fc := newFakeClient(t, namespaceInProject("acme-project-1", "p-acme"), reg)
	r := newRegistryReconciler(t, fc)

	res, err := r.handleDelete(context.Background(), reg, testLogger())
	if err != nil {
		t.Fatalf("handleDelete() error = %v", err)
	}
	if res.RequeueAfter != 0 {
		t.Errorf("handleDelete() requeued after %v for a Registry that never bound", res.RequeueAfter)
	}
	for _, f := range reg.Finalizers {
		if f == registryFinalizer {
			t.Error("finalizer still present; an unbound Registry has nothing to clean up")
		}
	}
}

// readyBackend builds a backend in the state a Registry can bind to.
func readyBackend(name, tenant string) *registryv1alpha1.RegistryBackend {
	return &registryv1alpha1.RegistryBackend{
		ObjectMeta: metav1.ObjectMeta{Name: name, Labels: map[string]string{tenantLabel: tenant}},
		Spec: registryv1alpha1.RegistryBackendSpec{
			TenantID: tenant,
			Plan:     "starter",
		},
		Status: registryv1alpha1.RegistryBackendStatus{
			Phase:           phaseReady,
			EffectivePlan:   "starter",
			HarborNamespace: harborNamespace(tenant),
			AdminSecretName: name + "-harbor-admin",
			RegistryURL:     "https://registry." + tenant + ".example.com",
		},
	}
}
