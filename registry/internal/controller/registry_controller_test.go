package controller

import (
	"context"
	"testing"
	"time"

	"github.com/go-logr/logr"
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

func registryIn(namespace, name string) *registryv1alpha1.Registry {
	return &registryv1alpha1.Registry{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Spec:       registryv1alpha1.RegistrySpec{Plan: "starter"},
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

// A namespace's first Registry has to bring Harbor into being, since nobody
// creates a backend by hand.
func TestBindBackend_FirstRegistryProvisionsHarbor(t *testing.T) {
	reg := registryIn("acme-project-1", "web")
	fc := newFakeClient(t, reg)
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
	if got.Name != backendName {
		t.Errorf("backend name = %q, want %q", got.Name, backendName)
	}
	if got.Namespace != "acme-project-1" {
		t.Errorf("backend namespace = %q, want the Registry's own namespace", got.Namespace)
	}
	if got.Spec.Plan != "starter" {
		t.Errorf("backend plan = %q, want the smallest plan", got.Spec.Plan)
	}
	if !got.Spec.Autoscale.Enabled {
		t.Error("autoscale should be enabled on an operator-created backend")
	}
}

// The requirement this design exists for: a second Registry in a namespace that
// already has Harbor must only add a project to it, never provision a second
// deployment.
func TestBindBackend_SecondRegistryInSameNamespaceReusesHarbor(t *testing.T) {
	existing := readyBackend("acme-project-1")
	reg := registryIn("acme-project-1", "api")
	fc := newFakeClient(t, existing, registryIn("acme-project-1", "web"), reg)
	r := newRegistryReconciler(t, fc)

	backend, _, err := r.bindBackend(context.Background(), reg)
	if err != nil {
		t.Fatalf("bindBackend() error = %v", err)
	}
	if backend == nil {
		t.Fatal("bindBackend() returned nil for a Ready backend")
	}
	if backend.Namespace != "acme-project-1" || backend.Name != backendName {
		t.Errorf("bound to %s/%s, want the namespace's existing Harbor", backend.Namespace, backend.Name)
	}
	if n := len(listBackends(t, fc)); n != 1 {
		t.Errorf("got %d backends, want 1 — a second Registry must not provision another Harbor", n)
	}
}

// The other half of the same rule: a different namespace is a different Harbor,
// even when one is already running next door.
func TestBindBackend_DifferentNamespaceGetsItsOwnHarbor(t *testing.T) {
	fc := newFakeClient(t, readyBackend("acme-project-1"), registryIn("acme-project-2", "web"))
	r := newRegistryReconciler(t, fc)

	reg := registryIn("acme-project-2", "web")
	if _, _, err := r.bindBackend(context.Background(), reg); err != nil {
		t.Fatalf("bindBackend() error = %v", err)
	}

	backends := listBackends(t, fc)
	if len(backends) != 2 {
		t.Fatalf("got %d backends, want 2 — each namespace gets its own Harbor", len(backends))
	}
	seen := map[string]bool{}
	for _, b := range backends {
		if b.Name != backendName {
			t.Errorf("backend %s/%s does not use the fixed name %q", b.Namespace, b.Name, backendName)
		}
		if seen[b.Namespace] {
			t.Errorf("namespace %s has more than one backend", b.Namespace)
		}
		seen[b.Namespace] = true
	}
}

// Isolation: the backend is addressed by the Registry's own namespace, so a
// Registry can never resolve to another namespace's Harbor.
func TestBindBackend_CannotReachAnotherNamespacesHarbor(t *testing.T) {
	a := readyBackend("ns-a")
	b := readyBackend("ns-b")
	reg := registryIn("ns-b", "web")

	fc := newFakeClient(t, a, b, reg)
	r := newRegistryReconciler(t, fc)

	backend, _, err := r.bindBackend(context.Background(), reg)
	if err != nil {
		t.Fatalf("bindBackend() error = %v", err)
	}
	if backend == nil {
		t.Fatal("bindBackend() returned nil")
	}
	if backend.Namespace != "ns-b" {
		t.Errorf("Registry in ns-b bound to Harbor in %q — cross-namespace binding", backend.Namespace)
	}
}

// Two Registries in one namespace racing their first reconcile must still end
// up with a single Harbor; the fixed name lets the API server settle it.
func TestBindBackend_ConcurrentFirstRegistriesCreateOneHarbor(t *testing.T) {
	regA := registryIn("acme-project-1", "web")
	regB := registryIn("acme-project-1", "api")
	fc := newFakeClient(t, regA, regB)
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
// resolving to one project name would silently share images. Each Harbor serves
// one namespace, where Kubernetes already forbids duplicate names.
func TestHarborProjectName_UniqueWithinANamespace(t *testing.T) {
	if got := harborProjectName(registryIn("acme-project-1", "web")); got != "web" {
		t.Errorf("harborProjectName() = %q, want web", got)
	}

	// Only same-namespace registries share a Harbor, so only they can collide.
	seen := map[string]string{}
	for _, name := range []string{"web", "api", "docs"} {
		got := harborProjectName(registryIn("acme-project-1", name))
		if prev, dup := seen[got]; dup {
			t.Errorf("%s and %s collide on Harbor project %q", name, prev, got)
		}
		seen[got] = name
	}
}

// Deleting a Registry that never got a Harbor project must not hang: there is
// nothing to remove.
func TestHandleDelete_RegistryWithoutProjectReleasesImmediately(t *testing.T) {
	reg := registryIn("acme-project-1", "web")
	reg.Finalizers = []string{registryFinalizer}
	reg.DeletionTimestamp = &metav1.Time{Time: time.Now()}

	fc := newFakeClient(t, reg)
	r := newRegistryReconciler(t, fc)

	res, err := r.handleDelete(context.Background(), reg, testLogger())
	if err != nil {
		t.Fatalf("handleDelete() error = %v", err)
	}
	if res.RequeueAfter != 0 {
		t.Errorf("handleDelete() requeued after %v for a Registry with no Harbor project", res.RequeueAfter)
	}
	for _, f := range reg.Finalizers {
		if f == registryFinalizer {
			t.Error("finalizer still present; a Registry with no Harbor project has nothing to clean up")
		}
	}
}

// registriesForBackend feeds the watch that wakes Registries when Harbor comes
// up. It must cover the namespace's Registries and nothing else — including the
// ones that have never bound, which are exactly the ones waiting on it.
func TestRegistriesForBackend_OnlyItsOwnNamespace(t *testing.T) {
	backend := readyBackend("ns-a")
	fc := newFakeClient(t, backend,
		registryIn("ns-a", "web"),
		registryIn("ns-a", "api"),
		registryIn("ns-b", "web"))
	r := newRegistryReconciler(t, fc)

	reqs := r.registriesForBackend(context.Background(), backend)
	if len(reqs) != 2 {
		t.Fatalf("got %d requests, want 2 (only ns-a's Registries)", len(reqs))
	}
	for _, req := range reqs {
		if req.Namespace != "ns-a" {
			t.Errorf("enqueued %s, which is not served by the ns-a backend", req.NamespacedName)
		}
	}
}

// Harbor ships a built-in project named "library" and it is PUBLIC.
// CreateHarborProject treats 409 as success, so without a guard a Registry
// named "library" would adopt that project, mint a push robot against it, and
// report Ready — publishing the namespace's images world-readable. The project
// name is the Registry's own name, so this is a name a user can just pick.
func TestHarborProjectName_ReservedNamesAreRejected(t *testing.T) {
	if !reservedProjectNames[harborProjectName(registryIn("acme-project-1", "library"))] {
		t.Error(`a Registry named "library" resolves to Harbor's public built-in project and must be refused`)
	}
	if !reservedProjectNames[harborProjectName(registryIn("acme-project-1", "LIBRARY"))] {
		t.Error("the reserved-name check must survive case folding, since harborProjectName lowercases")
	}
	for _, ok := range []string{"web", "api", "libraries", "my-library"} {
		if reservedProjectNames[harborProjectName(registryIn("acme-project-1", ok))] {
			t.Errorf("%q is not reserved and must be allowed", ok)
		}
	}
}

// readyBackend builds a namespace's backend in the state a Registry can bind to.
func readyBackend(namespace string) *registryv1alpha1.RegistryBackend {
	return &registryv1alpha1.RegistryBackend{
		ObjectMeta: metav1.ObjectMeta{Name: backendName, Namespace: namespace},
		Spec:       registryv1alpha1.RegistryBackendSpec{Plan: "starter"},
		Status: registryv1alpha1.RegistryBackendStatus{
			Phase:           phaseReady,
			EffectivePlan:   "starter",
			AdminSecretName: backendName + "-harbor-admin",
			RegistryURL:     "https://registry." + namespace + ".example.com",
		},
	}
}
