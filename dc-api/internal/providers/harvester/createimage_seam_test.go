package harvester

// createimage_seam_test.go proves the image slice of the credential-locality
// work: CreateImage and resolveImage route through the cluster-access seam
// (c.access) so they work for a REMOTE (agent-only) zone, while the local Direct
// path is unchanged.
//
//   - CreateImage via Direct (toggle OFF / local client) → a plain create on the
//     dynamic client, with a client-assigned "image-<hex>" name (generateName is
//     gone: SSA on the agent path cannot use it, and a POST accepts an explicit
//     name fine).
//   - CreateImage routed (REMOTE client, no dynamic) → c.access.Create is invoked
//     with a NAMED object (metadata.name set, no metadata.generateName), the shape
//     the agent's server-side apply requires.
//   - resolveImage routes through c.access.List (not c.dynamic), so a remote client
//     can resolve an image's storageClass — it previously required c.dynamic.

import (
	"context"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/wso2/dc-api/internal/providers/clusteraccess"
)

// recordingImageAccessor is a synthetic Accessor that records the object passed
// to Create and serves a seeded list from List. It is deliberately independent of
// the vmwrite_seam_test.go countingAccessor (which does not capture the object or
// serve seeded List items). Only the two methods the image slice exercises are
// meaningful; the rest satisfy the interface inertly.
type recordingImageAccessor struct {
	createCalls int
	lastCreated *unstructured.Unstructured
	lastCreNS   string
	lastCreGVR  schema.GroupVersionResource

	listCalls  int
	lastListNS string
	listItems  []unstructured.Unstructured
}

var _ clusteraccess.Accessor = (*recordingImageAccessor)(nil)

func (a *recordingImageAccessor) Get(context.Context, schema.GroupVersionResource, string, string, metav1.GetOptions) (*unstructured.Unstructured, error) {
	return &unstructured.Unstructured{}, nil
}

func (a *recordingImageAccessor) List(_ context.Context, _ schema.GroupVersionResource, ns string, _ metav1.ListOptions) (*unstructured.UnstructuredList, error) {
	a.listCalls++
	a.lastListNS = ns
	return &unstructured.UnstructuredList{Items: a.listItems}, nil
}

func (a *recordingImageAccessor) Create(_ context.Context, gvr schema.GroupVersionResource, ns string, obj *unstructured.Unstructured, _ metav1.CreateOptions) (*unstructured.Unstructured, error) {
	a.createCalls++
	a.lastCreated = obj
	a.lastCreNS = ns
	a.lastCreGVR = gvr
	// Echo the object back the way a POST would (the created object carries the
	// name the caller assigned).
	return obj, nil
}

func (a *recordingImageAccessor) Apply(_ context.Context, _ schema.GroupVersionResource, _ string, obj *unstructured.Unstructured, _ string, _ bool) (*unstructured.Unstructured, error) {
	return obj, nil
}

func (a *recordingImageAccessor) Update(_ context.Context, _ schema.GroupVersionResource, _ string, obj *unstructured.Unstructured, _ metav1.UpdateOptions) (*unstructured.Unstructured, error) {
	return obj, nil
}

func (a *recordingImageAccessor) Delete(context.Context, schema.GroupVersionResource, string, string, metav1.DeleteOptions) error {
	return nil
}

// ── CreateImage: remote path routes via c.access.Create with a NAMED object ──

func TestCreateImage_Remote_RoutesViaAccessorWithName(t *testing.T) {
	rec := &recordingImageAccessor{}
	// A REMOTE client has no dynamic; every op must go through the accessor.
	c := NewRemoteClient(rec, "lk", "zone-1")

	img, err := c.CreateImage(context.Background(), "ubuntu-22.04", "https://example.com/img.qcow2")
	if err != nil {
		t.Fatalf("CreateImage (remote): %v", err)
	}

	// The create routed through the seam exactly once, on the image GVR, in the
	// default namespace.
	if rec.createCalls != 1 {
		t.Fatalf("accessor Create called %d times, want 1", rec.createCalls)
	}
	if rec.lastCreGVR != vmImageGVR {
		t.Errorf("accessor Create GVR = %v, want %v", rec.lastCreGVR, vmImageGVR)
	}
	if rec.lastCreNS != "default" {
		t.Errorf("accessor Create namespace = %q, want %q", rec.lastCreNS, "default")
	}

	// The object MUST carry metadata.name (SSA needs it) and MUST NOT carry
	// metadata.generateName (SSA cannot use it).
	name, hasName, _ := unstructured.NestedString(rec.lastCreated.Object, "metadata", "name")
	if !hasName || name == "" {
		t.Fatalf("created object has no metadata.name; got metadata=%v", rec.lastCreated.Object["metadata"])
	}
	if len(name) <= len("image-") || name[:len("image-")] != "image-" {
		t.Errorf("metadata.name = %q, want an \"image-<hex>\" name", name)
	}
	if _, hasGen, _ := unstructured.NestedString(rec.lastCreated.Object, "metadata", "generateName"); hasGen {
		t.Errorf("created object still sets metadata.generateName; SSA cannot use it")
	}

	// The returned Image ID uses the client-assigned name.
	if img.ID != "default/"+name {
		t.Errorf("image ID = %q, want %q", img.ID, "default/"+name)
	}
	if img.DisplayName != "ubuntu-22.04" {
		t.Errorf("image DisplayName = %q, want ubuntu-22.04", img.DisplayName)
	}

	// spec is preserved.
	dn, _, _ := unstructured.NestedString(rec.lastCreated.Object, "spec", "displayName")
	url, _, _ := unstructured.NestedString(rec.lastCreated.Object, "spec", "url")
	st, _, _ := unstructured.NestedString(rec.lastCreated.Object, "spec", "sourceType")
	if dn != "ubuntu-22.04" || url != "https://example.com/img.qcow2" || st != "download" {
		t.Errorf("spec = {displayName:%q url:%q sourceType:%q}, want ubuntu-22.04 / the URL / download", dn, url, st)
	}
}

// TestCreateImage_TwoCallsGetDistinctNames proves the client-assigned suffix is
// random per call (no collision on repeated imports of the same display name).
func TestCreateImage_TwoCallsGetDistinctNames(t *testing.T) {
	rec := &recordingImageAccessor{}
	c := NewRemoteClient(rec, "lk", "zone-1")

	a, err := c.CreateImage(context.Background(), "ubuntu", "https://example.com/a")
	if err != nil {
		t.Fatalf("CreateImage #1: %v", err)
	}
	b, err := c.CreateImage(context.Background(), "ubuntu", "https://example.com/b")
	if err != nil {
		t.Fatalf("CreateImage #2: %v", err)
	}
	if a.ID == b.ID {
		t.Errorf("two CreateImage calls produced the same ID %q — names must be unique", a.ID)
	}
}

// TestCreateImage_Routed_TogglesBetweenAgentAndDirect proves the Routed decision
// gates CreateImage per-verb/per-family: with VerbCreate activated for the image
// GVR the create routes to the agent accessor; with it de-activated it falls to
// Direct. This is the same routing contract the registry enforces via the
// per-family RouteVerbs allow-set (image now includes VerbCreate).
func TestCreateImage_Routed_TogglesBetweenAgentAndDirect(t *testing.T) {
	newRouted := func(agent, direct clusteraccess.Accessor, allow bool) *clusteraccess.Routed {
		return clusteraccess.NewRouted(direct, func(v clusteraccess.Verb, gvr schema.GroupVersionResource) (clusteraccess.Accessor, bool) {
			if allow && v == clusteraccess.VerbCreate && gvr == vmImageGVR {
				return agent, true
			}
			return nil, false
		})
	}

	// Toggle ON → the agent accessor gets the create; Direct is untouched.
	t.Run("on routes to agent", func(t *testing.T) {
		agent := &recordingImageAccessor{}
		direct := &recordingImageAccessor{}
		c := &Client{access: newRouted(agent, direct, true)}
		if _, err := c.CreateImage(context.Background(), "ubuntu", "https://example.com/a"); err != nil {
			t.Fatalf("CreateImage: %v", err)
		}
		if agent.createCalls != 1 {
			t.Errorf("agent Create called %d times, want 1", agent.createCalls)
		}
		if direct.createCalls != 0 {
			t.Errorf("direct Create called %d times with toggle ON, want 0", direct.createCalls)
		}
	})

	// Toggle OFF → Direct gets the create; the agent is untouched.
	t.Run("off uses direct", func(t *testing.T) {
		agent := &recordingImageAccessor{}
		direct := &recordingImageAccessor{}
		c := &Client{access: newRouted(agent, direct, false)}
		if _, err := c.CreateImage(context.Background(), "ubuntu", "https://example.com/a"); err != nil {
			t.Fatalf("CreateImage: %v", err)
		}
		if direct.createCalls != 1 {
			t.Errorf("direct Create called %d times, want 1", direct.createCalls)
		}
		if agent.createCalls != 0 {
			t.Errorf("agent Create called %d times with toggle OFF, want 0", agent.createCalls)
		}
	})
}

// ── CreateImage: local Direct path still works (byte-identical apart from name) ─

func TestCreateImage_Local_UsesDirect(t *testing.T) {
	dyn := newEmptyFakeDynamic()
	c := &Client{dynamic: dyn, access: clusteraccess.NewDirect(dyn)}

	img, err := c.CreateImage(context.Background(), "ubuntu-22.04", "https://example.com/img.qcow2")
	if err != nil {
		t.Fatalf("CreateImage (local): %v", err)
	}
	if img.Namespace != "default" || img.DisplayName != "ubuntu-22.04" {
		t.Errorf("image = %+v, want namespace=default displayName=ubuntu-22.04", img)
	}

	// The object really landed in the local fake store under the returned name.
	// img.ID is "default/<name>", so strip the "default/" prefix.
	name := img.ID[len("default/"):]
	got, err := dyn.Resource(vmImageGVR).Namespace("default").Get(context.Background(), name, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("image not found in direct store under %q: %v", name, err)
	}
	// The stored object carries a real name, not generateName.
	if got.GetName() != name {
		t.Errorf("stored object name = %q, want %q", got.GetName(), name)
	}
	if _, hasGen, _ := unstructured.NestedString(got.Object, "metadata", "generateName"); hasGen {
		t.Errorf("stored object unexpectedly carries metadata.generateName")
	}
}

// ── resolveImage routes through c.access.List (not c.dynamic) ────────────────

func TestResolveImage_RoutesViaAccessorList(t *testing.T) {
	// Seed the accessor's List with one ready image (status.storageClassName set).
	item := unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "harvesterhci.io/v1beta1",
		"kind":       "VirtualMachineImage",
		"metadata": map[string]interface{}{
			"name":      "image-abc123",
			"namespace": "default",
		},
		"spec":   map[string]interface{}{"displayName": "ubuntu-22.04"},
		"status": map[string]interface{}{"storageClassName": "longhorn-image-abc123"},
	}}
	rec := &recordingImageAccessor{listItems: []unstructured.Unstructured{item}}
	// REMOTE client: resolveImage must NOT touch c.dynamic (it is nil) — proving
	// the lookup routes through the seam.
	c := NewRemoteClient(rec, "lk", "zone-1")

	// By display name.
	id, sc, err := c.resolveImage(context.Background(), "ubuntu-22.04")
	if err != nil {
		t.Fatalf("resolveImage by displayName: %v", err)
	}
	if id != "default/image-abc123" || sc != "longhorn-image-abc123" {
		t.Errorf("resolveImage = (%q, %q), want (default/image-abc123, longhorn-image-abc123)", id, sc)
	}
	if rec.listCalls != 1 {
		t.Errorf("accessor List called %d times, want 1", rec.listCalls)
	}
	// Cross-namespace lookup: namespace must be empty (all namespaces).
	if rec.lastListNS != "" {
		t.Errorf("resolveImage List namespace = %q, want empty (all namespaces)", rec.lastListNS)
	}

	// By full ID also resolves.
	id2, _, err := c.resolveImage(context.Background(), "default/image-abc123")
	if err != nil {
		t.Fatalf("resolveImage by ID: %v", err)
	}
	if id2 != "default/image-abc123" {
		t.Errorf("resolveImage by ID = %q, want default/image-abc123", id2)
	}
}

// TestResolveImage_NotFound proves the seam-routed lookup still surfaces the
// not-found error when no image matches.
func TestResolveImage_NotFound(t *testing.T) {
	rec := &recordingImageAccessor{listItems: nil}
	c := NewRemoteClient(rec, "lk", "zone-1")

	if _, _, err := c.resolveImage(context.Background(), "missing"); err == nil {
		t.Fatal("resolveImage returned nil error for a missing image; want not-found")
	}
}
