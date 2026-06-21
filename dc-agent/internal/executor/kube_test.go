package executor

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/watch"
	dynfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/fake"
	ktesting "k8s.io/client-go/testing"
)

// ── M-B test scaffolding ───────────────────────────────────────────────────────
//
// The four M-B verbs resolve GVK→GVR through a RESTMapper and act through the
// dynamic client. Tests inject a static meta.RESTMapper (cluster discovery stands
// nowhere in a fake) registering one namespaced kind (ConfigMap) and one
// cluster-scoped kind (a stand-in CRD, kubeovn.io/v1 Vpc) so both scopes are
// exercised. ConfigMap/Vpc are convenient unstructured carriers — the verbs are
// kind-agnostic, so the kinds chosen don't matter beyond their scope.

var (
	cmGVR  = schema.GroupVersionResource{Group: "", Version: "v1", Resource: "configmaps"}
	vpcGVR = schema.GroupVersionResource{Group: "kubeovn.io", Version: "v1", Resource: "vpcs"}
)

// mbMapper is the static RESTMapper the M-B verb tests inject: ConfigMap
// (namespaced) and Vpc (cluster-scoped). An unregistered kind yields a NoMatch,
// which the executor maps to a BAD_REQUEST-class fault.
func mbMapper() meta.RESTMapper {
	m := meta.NewDefaultRESTMapper([]schema.GroupVersion{
		{Group: "", Version: "v1"},
		{Group: "kubeovn.io", Version: "v1"},
	})
	m.Add(schema.GroupVersionKind{Group: "", Version: "v1", Kind: "ConfigMap"}, meta.RESTScopeNamespace)
	m.Add(schema.GroupVersionKind{Group: "kubeovn.io", Version: "v1", Kind: "Vpc"}, meta.RESTScopeRoot)
	return m
}

// mbListKinds maps the test GVRs to their list kinds for the dynamic fake.
func mbListKinds() map[schema.GroupVersionResource]string {
	return map[schema.GroupVersionResource]string{
		cmGVR:  "ConfigMapList",
		vpcGVR: "VpcList",
	}
}

// configMap builds an unstructured ConfigMap with the given name/namespace and an
// optional .status.phase (empty phase ⇒ no .status subobject at all).
func configMap(name, namespace, statusPhase string) *unstructured.Unstructured {
	o := &unstructured.Unstructured{}
	o.SetAPIVersion("v1")
	o.SetKind("ConfigMap")
	o.SetName(name)
	if namespace != "" {
		o.SetNamespace(namespace)
	}
	if statusPhase != "" {
		_ = unstructured.SetNestedField(o.Object, statusPhase, "status", "phase")
	}
	return o
}

// installSSAReactor makes the dynamic fake honor server-side apply as an upsert.
//
// The fake's ResourceInterface.Apply issues an ApplyPatchType PATCH to the object
// tracker, whose default reactor does NOT implement SSA: it requires the object to
// pre-exist (apply-create → "not found") and cannot strategic-merge an
// unstructured object (apply-update → "unable to find api field…"). This reactor
// emulates the real apiserver's apply upsert: decode the manifest, create it
// (uid + rv "1") when absent, or replace it (rv bumped, uid preserved) when
// present, then return the stored object — which is exactly what KubeExecutor.Apply
// reads its result from. It is a TEST shim for the fake's limitation, not
// production behavior.
func installSSAReactor(t *testing.T, dc *dynfake.FakeDynamicClient) {
	t.Helper()
	tracker := dc.Tracker()
	dc.PrependReactor("patch", "*", func(action ktesting.Action) (bool, runtime.Object, error) {
		pa, ok := action.(ktesting.PatchAction)
		if !ok || pa.GetPatchType() != types.ApplyPatchType {
			return false, nil, nil // not an apply — let the default chain handle it
		}
		gvr := pa.GetResource()
		ns := pa.GetNamespace()
		name := pa.GetName()

		obj := &unstructured.Unstructured{}
		if err := obj.UnmarshalJSON(pa.GetPatch()); err != nil {
			return true, nil, err
		}
		obj.SetName(name)
		if ns != "" {
			obj.SetNamespace(ns)
		}

		existing, err := tracker.Get(gvr, ns, name)
		switch {
		case apierrors.IsNotFound(err):
			obj.SetUID(types.UID("uid-" + name))
			obj.SetResourceVersion("1")
			if cerr := tracker.Create(gvr, obj, ns); cerr != nil {
				return true, nil, cerr
			}
			return true, obj, nil
		case err != nil:
			return true, nil, err
		default:
			ex := existing.(*unstructured.Unstructured)
			obj.SetUID(ex.GetUID()) // identity is stable across applies
			obj.SetResourceVersion("2")
			if uerr := tracker.Update(gvr, obj, ns); uerr != nil {
				return true, nil, uerr
			}
			return true, obj, nil
		}
	})
}

// isBadRequestFault reports whether err (or anything it wraps) is the executor's
// BAD_REQUEST-class fault — the same BadRequest() bool marker the conn dispatcher
// keys on to return a BAD_REQUEST res rather than EXEC_ERROR.
func isBadRequestFault(err error) bool {
	var br interface{ BadRequest() bool }
	return errors.As(err, &br) && br.BadRequest()
}

func TestKubeExecutorGetInventory(t *testing.T) {
	node := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: "node-1"},
		Status: corev1.NodeStatus{
			Allocatable: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("4"),
				corev1.ResourceMemory: resource.MustParse("8Gi"),
			},
			Conditions: []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionTrue}},
		},
	}
	running := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "running", Namespace: "default"},
		Spec: corev1.PodSpec{
			NodeName: "node-1",
			Containers: []corev1.Container{{
				Name: "c",
				Resources: corev1.ResourceRequirements{Requests: corev1.ResourceList{
					corev1.ResourceCPU:    resource.MustParse("500m"),
					corev1.ResourceMemory: resource.MustParse("1Gi"),
				}},
			}},
		},
		Status: corev1.PodStatus{Phase: corev1.PodRunning},
	}
	// A succeeded pod must NOT count toward allocation.
	done := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "done", Namespace: "default"},
		Spec: corev1.PodSpec{
			NodeName: "node-1",
			Containers: []corev1.Container{{
				Name: "c",
				Resources: corev1.ResourceRequirements{Requests: corev1.ResourceList{
					corev1.ResourceCPU: resource.MustParse("8"),
				}},
			}},
		},
		Status: corev1.PodStatus{Phase: corev1.PodSucceeded},
	}
	kube := fake.NewSimpleClientset(node, running, done)

	vmGVR := schema.GroupVersionResource{Group: "kubevirt.io", Version: "v1", Resource: "virtualmachines"}
	vm := &unstructured.Unstructured{}
	vm.SetGroupVersionKind(schema.GroupVersionKind{Group: "kubevirt.io", Version: "v1", Kind: "VirtualMachine"})
	vm.SetName("vm-1")
	vm.SetNamespace("default")
	dyn := dynfake.NewSimpleDynamicClientWithCustomListKinds(
		runtime.NewScheme(),
		map[schema.GroupVersionResource]string{vmGVR: "VirtualMachineList"},
		vm,
	)

	inv, err := NewKubeExecutor(kube, dyn).GetInventory(context.Background())
	if err != nil {
		t.Fatalf("GetInventory: %v", err)
	}

	if inv.VMCount != 1 {
		t.Errorf("VMCount = %d, want 1", inv.VMCount)
	}
	if len(inv.Nodes) != 1 {
		t.Fatalf("nodes = %d, want 1", len(inv.Nodes))
	}
	n := inv.Nodes[0]
	if n.Name != "node-1" || !n.Ready {
		t.Errorf("node = %+v, want node-1 ready", n)
	}
	if n.CPUAllocatableM != 4000 {
		t.Errorf("CPUAllocatableM = %d, want 4000", n.CPUAllocatableM)
	}
	if n.CPUUsedM != 500 { // only the running pod; the succeeded pod's 8 cores excluded
		t.Errorf("CPUUsedM = %d, want 500", n.CPUUsedM)
	}
	if n.MemAllocatableMB != 8192 {
		t.Errorf("MemAllocatableMB = %d, want 8192", n.MemAllocatableMB)
	}
	if n.MemUsedMB != 1024 {
		t.Errorf("MemUsedMB = %d, want 1024", n.MemUsedMB)
	}
}

func TestNodeReady(t *testing.T) {
	notReady := &corev1.Node{Status: corev1.NodeStatus{
		Conditions: []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionFalse}},
	}}
	if nodeReady(notReady) {
		t.Error("nodeReady = true for a NotReady node")
	}
	noCondition := &corev1.Node{}
	if nodeReady(noCondition) {
		t.Error("nodeReady = true for a node with no Ready condition")
	}
}

// ── Apply ───────────────────────────────────────────────────────────────────

// TestKubeExecutorApply_Create applies a manifest for a not-yet-existing object
// and asserts the result identity and that the object now exists in the cluster.
// SSA upsert against the dynamic fake is provided by installSSAReactor (the fake's
// own apply reactor can't create — see that helper's comment).
func TestKubeExecutorApply_Create(t *testing.T) {
	dc := dynfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), mbListKinds())
	installSSAReactor(t, dc)
	k := NewKubeExecutorWithMapper(fake.NewSimpleClientset(), dc, mbMapper())

	manifest, err := configMap("cm-1", "tenant-abc", "").MarshalJSON()
	if err != nil {
		t.Fatalf("marshal manifest: %v", err)
	}

	res, err := k.Apply(context.Background(), manifest, "dc-api", false)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if res.APIVersion != "v1" || res.Kind != "ConfigMap" || res.Namespace != "tenant-abc" || res.Name != "cm-1" {
		t.Errorf("ApplyResult identity = %+v, want v1/ConfigMap tenant-abc/cm-1", res)
	}
	if res.UID == "" || res.ResourceVersion == "" {
		t.Errorf("ApplyResult uid/resourceVersion empty: %+v", res)
	}

	// The object must actually exist post-apply.
	got, err := dc.Resource(cmGVR).Namespace("tenant-abc").Get(context.Background(), "cm-1", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("post-apply Get: %v", err)
	}
	if got.GetUID() != types.UID(res.UID) || got.GetResourceVersion() != res.ResourceVersion {
		t.Errorf("post-apply object uid/rv (%q/%q) != ApplyResult (%q/%q)",
			got.GetUID(), got.GetResourceVersion(), res.UID, res.ResourceVersion)
	}
}

// TestKubeExecutorApply_StripsServerManagedFields guards the SSA pre-strip: a
// manifest that arrives carrying metadata.managedFields (and a stale
// metadata.resourceVersion) must have BOTH removed before the apply patch goes to
// the cluster. The real apiserver hard-errors ("cannot apply an object with
// managed fields already set") on a managedFields-bearing apply; a stale
// resourceVersion would assert an unintended optimistic-concurrency precondition.
// The fake doesn't enforce either, so we assert directly on the patch bytes the
// executor sent: neither field may be present.
func TestKubeExecutorApply_StripsServerManagedFields(t *testing.T) {
	dc := dynfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), mbListKinds())

	// Capture the apply-patch payload (the object the executor sent upstream).
	// installSSAReactor is prepended first; this observer is prepended AFTER it so
	// it sits at the FRONT of the chain (PrependReactor pushes to the head) and
	// runs before the SSA reactor consumes the action — it observes, then falls
	// through.
	installSSAReactor(t, dc)
	var capturedPatch []byte
	dc.PrependReactor("patch", "*", func(action ktesting.Action) (bool, runtime.Object, error) {
		if pa, ok := action.(ktesting.PatchAction); ok && pa.GetPatchType() == types.ApplyPatchType {
			capturedPatch = append([]byte(nil), pa.GetPatch()...)
		}
		return false, nil, nil // observe only; let installSSAReactor handle it
	})
	k := NewKubeExecutorWithMapper(fake.NewSimpleClientset(), dc, mbMapper())

	// Build a manifest that carries server-managed metadata an apply must not send.
	obj := configMap("cm-1", "tenant-abc", "Running")
	obj.SetResourceVersion("12345")
	_ = unstructured.SetNestedSlice(obj.Object, []interface{}{
		map[string]interface{}{
			"manager":    "kubectl-client-side-apply",
			"operation":  "Update",
			"apiVersion": "v1",
		},
	}, "metadata", "managedFields")
	manifest, err := obj.MarshalJSON()
	if err != nil {
		t.Fatalf("marshal manifest: %v", err)
	}
	// Sanity: the field is really present in the input we hand to Apply.
	if !json.Valid(manifest) {
		t.Fatalf("test manifest is not valid JSON")
	}

	res, err := k.Apply(context.Background(), manifest, "dc-api", false)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if res.Name != "cm-1" || res.Namespace != "tenant-abc" {
		t.Errorf("ApplyResult identity = %+v, want tenant-abc/cm-1", res)
	}

	if capturedPatch == nil {
		t.Fatal("apply patch was never captured")
	}
	patched := &unstructured.Unstructured{}
	if err := patched.UnmarshalJSON(capturedPatch); err != nil {
		t.Fatalf("decode captured patch: %v", err)
	}
	if _, found, _ := unstructured.NestedSlice(patched.Object, "metadata", "managedFields"); found {
		t.Error("apply patch still carried metadata.managedFields (must be stripped before SSA)")
	}
	if patched.GetResourceVersion() != "" {
		t.Errorf("apply patch still carried metadata.resourceVersion %q (must be cleared before SSA)", patched.GetResourceVersion())
	}
	// The substantive content must survive the strip.
	if phase, _, _ := unstructured.NestedString(patched.Object, "status", "phase"); phase != "Running" {
		t.Errorf("apply patch lost .status.phase: got %q, want Running", phase)
	}
}

// TestKubeExecutorApply_Update applies over an existing object and asserts the uid
// is preserved (stable identity) while the resourceVersion advances.
func TestKubeExecutorApply_Update(t *testing.T) {
	seed := configMap("cm-1", "tenant-abc", "Pending")
	seed.SetUID("uid-existing")
	seed.SetResourceVersion("100")
	dc := dynfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), mbListKinds(), seed)
	installSSAReactor(t, dc)
	k := NewKubeExecutorWithMapper(fake.NewSimpleClientset(), dc, mbMapper())

	manifest, _ := configMap("cm-1", "tenant-abc", "Running").MarshalJSON()
	res, err := k.Apply(context.Background(), manifest, "dc-api", true)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if res.UID != "uid-existing" {
		t.Errorf("Apply over existing object changed uid: got %q, want uid-existing", res.UID)
	}
	if res.ResourceVersion == "100" || res.ResourceVersion == "" {
		t.Errorf("Apply did not advance resourceVersion: got %q", res.ResourceVersion)
	}
}

// TestKubeExecutorApply_DefaultsFieldManager applies with an empty field manager
// and asserts the executor substitutes the "dc-api" default (the apply still
// succeeds; the default is documented contract).
func TestKubeExecutorApply_DefaultsFieldManager(t *testing.T) {
	dc := dynfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), mbListKinds())
	// A single reactor both observes the field manager on the apply PATCH and
	// satisfies the apply (a chained observe-only reactor would never run, since
	// the SSA reactor at the chain front returns handled=true first).
	var seenFM string
	tracker := dc.Tracker()
	dc.PrependReactor("patch", "configmaps", func(action ktesting.Action) (bool, runtime.Object, error) {
		pa, ok := action.(ktesting.PatchAction)
		if !ok || pa.GetPatchType() != types.ApplyPatchType {
			return false, nil, nil
		}
		seenFM = action.(ktesting.PatchActionImpl).PatchOptions.FieldManager
		obj := &unstructured.Unstructured{}
		if err := obj.UnmarshalJSON(pa.GetPatch()); err != nil {
			return true, nil, err
		}
		obj.SetName(pa.GetName())
		obj.SetNamespace(pa.GetNamespace())
		obj.SetUID("uid-1")
		obj.SetResourceVersion("1")
		if err := tracker.Create(pa.GetResource(), obj, pa.GetNamespace()); err != nil {
			return true, nil, err
		}
		return true, obj, nil
	})
	k := NewKubeExecutorWithMapper(fake.NewSimpleClientset(), dc, mbMapper())

	manifest, _ := configMap("cm-1", "tenant-abc", "").MarshalJSON()
	if _, err := k.Apply(context.Background(), manifest, "", false); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if seenFM != defaultFieldManager {
		t.Errorf("empty field_manager not defaulted: PatchOptions.FieldManager = %q, want %q", seenFM, defaultFieldManager)
	}
}

// TestKubeExecutorApply_BadManifest asserts an unparseable manifest, and one
// missing kind/name, are BAD_REQUEST-class faults (not EXEC_ERROR, not a panic).
func TestKubeExecutorApply_BadManifest(t *testing.T) {
	dc := dynfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), mbListKinds())
	k := NewKubeExecutorWithMapper(fake.NewSimpleClientset(), dc, mbMapper())

	cases := []struct {
		name     string
		manifest string
	}{
		{"not json", `this is not json`},
		{"missing kind", `{"apiVersion":"v1","metadata":{"name":"cm-1","namespace":"x"}}`},
		{"missing name", `{"apiVersion":"v1","kind":"ConfigMap","metadata":{"namespace":"x"}}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := k.Apply(context.Background(), json.RawMessage(tc.manifest), "dc-api", false)
			if err == nil {
				t.Fatalf("Apply(%s) err = nil, want BAD_REQUEST fault", tc.name)
			}
			if !isBadRequestFault(err) {
				t.Errorf("Apply(%s) err = %v, want a BadRequest()=true fault", tc.name, err)
			}
		})
	}
}

// ── Delete ──────────────────────────────────────────────────────────────────

// TestKubeExecutorDelete_Exists deletes a seeded object and asserts Existed:true
// and that the object is gone.
func TestKubeExecutorDelete_Exists(t *testing.T) {
	seed := configMap("cm-1", "tenant-abc", "")
	dc := dynfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), mbListKinds(), seed)
	k := NewKubeExecutorWithMapper(fake.NewSimpleClientset(), dc, mbMapper())

	res, err := k.Delete(context.Background(), ResourceRef{APIVersion: "v1", Kind: "ConfigMap", Namespace: "tenant-abc", Name: "cm-1"}, "")
	if err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if !res.Existed {
		t.Errorf("Delete of present object Existed = false, want true")
	}
	_, err = dc.Resource(cmGVR).Namespace("tenant-abc").Get(context.Background(), "cm-1", metav1.GetOptions{})
	if !apierrors.IsNotFound(err) {
		t.Errorf("object still present after Delete (err=%v), want NotFound", err)
	}
}

// TestKubeExecutorDelete_Absent deletes a missing object and asserts the
// idempotent-delete contract: success with Existed:false, NOT an error.
func TestKubeExecutorDelete_Absent(t *testing.T) {
	dc := dynfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), mbListKinds())
	k := NewKubeExecutorWithMapper(fake.NewSimpleClientset(), dc, mbMapper())

	res, err := k.Delete(context.Background(), ResourceRef{APIVersion: "v1", Kind: "ConfigMap", Namespace: "tenant-abc", Name: "ghost"}, "")
	if err != nil {
		t.Fatalf("Delete of absent object returned error %v, want nil (idempotent)", err)
	}
	if res.Existed {
		t.Errorf("Delete of absent object Existed = true, want false")
	}
}

// TestKubeExecutorDelete_PropagationPolicy asserts each legal policy is accepted
// and an illegal one is a BAD_REQUEST-class fault before any cluster call.
func TestKubeExecutorDelete_PropagationPolicy(t *testing.T) {
	legal := []string{"Foreground", "Background", "Orphan"}
	for _, policy := range legal {
		t.Run("legal/"+policy, func(t *testing.T) {
			seed := configMap("cm-1", "tenant-abc", "")
			dc := dynfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), mbListKinds(), seed)
			k := NewKubeExecutorWithMapper(fake.NewSimpleClientset(), dc, mbMapper())
			res, err := k.Delete(context.Background(), ResourceRef{APIVersion: "v1", Kind: "ConfigMap", Namespace: "tenant-abc", Name: "cm-1"}, policy)
			if err != nil {
				t.Fatalf("Delete with policy %q: %v", policy, err)
			}
			if !res.Existed {
				t.Errorf("Delete with policy %q Existed = false, want true", policy)
			}
		})
	}

	t.Run("illegal", func(t *testing.T) {
		seed := configMap("cm-1", "tenant-abc", "")
		dc := dynfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), mbListKinds(), seed)
		k := NewKubeExecutorWithMapper(fake.NewSimpleClientset(), dc, mbMapper())
		_, err := k.Delete(context.Background(), ResourceRef{APIVersion: "v1", Kind: "ConfigMap", Namespace: "tenant-abc", Name: "cm-1"}, "Nope")
		if err == nil {
			t.Fatal("Delete with illegal policy err = nil, want BAD_REQUEST fault")
		}
		if !isBadRequestFault(err) {
			t.Errorf("illegal policy err = %v, want a BadRequest()=true fault", err)
		}
		// The object must NOT have been deleted — the policy is rejected first.
		if _, gerr := dc.Resource(cmGVR).Namespace("tenant-abc").Get(context.Background(), "cm-1", metav1.GetOptions{}); gerr != nil {
			t.Errorf("object deleted despite illegal policy (err=%v), want still present", gerr)
		}
	})
}

// ── GetStatus ─────────────────────────────────────────────────────────────────

// TestKubeExecutorGetStatus_Found reads a seeded object's status and asserts the
// .status subobject, resourceVersion, and generation round-trip.
func TestKubeExecutorGetStatus_Found(t *testing.T) {
	seed := configMap("cm-1", "tenant-abc", "Running")
	seed.SetResourceVersion("12345")
	seed.SetGeneration(7)
	dc := dynfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), mbListKinds(), seed)
	k := NewKubeExecutorWithMapper(fake.NewSimpleClientset(), dc, mbMapper())

	snap, err := k.GetStatus(context.Background(), ResourceRef{APIVersion: "v1", Kind: "ConfigMap", Namespace: "tenant-abc", Name: "cm-1"})
	if err != nil {
		t.Fatalf("GetStatus: %v", err)
	}
	if !snap.Found {
		t.Fatal("GetStatus Found = false, want true")
	}
	if snap.ResourceVersion != "12345" {
		t.Errorf("ResourceVersion = %q, want 12345", snap.ResourceVersion)
	}
	if snap.Generation != 7 {
		t.Errorf("Generation = %d, want 7", snap.Generation)
	}
	var status struct {
		Phase string `json:"phase"`
	}
	if err := json.Unmarshal(snap.Status, &status); err != nil {
		t.Fatalf("unmarshal status: %v (raw=%s)", err, snap.Status)
	}
	if status.Phase != "Running" {
		t.Errorf("status.phase = %q, want Running (raw=%s)", status.Phase, snap.Status)
	}
}

// TestKubeExecutorGetStatus_NoStatus reads an object that has no .status and
// asserts Found:true with an omitted (nil) status — the verb still succeeds.
func TestKubeExecutorGetStatus_NoStatus(t *testing.T) {
	seed := configMap("cm-1", "tenant-abc", "") // no status subobject
	seed.SetResourceVersion("5")
	dc := dynfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), mbListKinds(), seed)
	k := NewKubeExecutorWithMapper(fake.NewSimpleClientset(), dc, mbMapper())

	snap, err := k.GetStatus(context.Background(), ResourceRef{APIVersion: "v1", Kind: "ConfigMap", Namespace: "tenant-abc", Name: "cm-1"})
	if err != nil {
		t.Fatalf("GetStatus: %v", err)
	}
	if !snap.Found {
		t.Error("Found = false, want true for a present object with no status")
	}
	if snap.Status != nil {
		t.Errorf("Status = %s, want nil for an object with no .status", snap.Status)
	}
}

// TestKubeExecutorGetStatus_Absent reads a missing object and asserts the
// not-found-is-success contract: Found:false, nil error (lets a poller ask
// "gone yet?" without treating 404 as failure).
func TestKubeExecutorGetStatus_Absent(t *testing.T) {
	dc := dynfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), mbListKinds())
	k := NewKubeExecutorWithMapper(fake.NewSimpleClientset(), dc, mbMapper())

	snap, err := k.GetStatus(context.Background(), ResourceRef{APIVersion: "v1", Kind: "ConfigMap", Namespace: "tenant-abc", Name: "ghost"})
	if err != nil {
		t.Fatalf("GetStatus of absent object returned error %v, want nil", err)
	}
	if snap.Found {
		t.Error("Found = true for an absent object, want false")
	}
	if snap.ResourceVersion != "" || snap.Status != nil {
		t.Errorf("absent snapshot carries data: %+v, want zero", snap)
	}
}

// TestKubeExecutorGetStatus_ClusterScoped reads a cluster-scoped object (namespace
// ignored) — proves the RESTMapper scope branch and the cluster-scoped dynamic
// client path.
func TestKubeExecutorGetStatus_ClusterScoped(t *testing.T) {
	vpc := &unstructured.Unstructured{}
	vpc.SetAPIVersion("kubeovn.io/v1")
	vpc.SetKind("Vpc")
	vpc.SetName("vpc-1")
	vpc.SetResourceVersion("9")
	_ = unstructured.SetNestedField(vpc.Object, "ready", "status", "phase")
	dc := dynfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), mbListKinds(), vpc)
	k := NewKubeExecutorWithMapper(fake.NewSimpleClientset(), dc, mbMapper())

	// Namespace is set on the ref but must be ignored for a cluster-scoped kind.
	snap, err := k.GetStatus(context.Background(), ResourceRef{APIVersion: "kubeovn.io/v1", Kind: "Vpc", Namespace: "should-be-ignored", Name: "vpc-1"})
	if err != nil {
		t.Fatalf("GetStatus cluster-scoped: %v", err)
	}
	if !snap.Found || snap.ResourceVersion != "9" {
		t.Errorf("cluster-scoped snapshot = %+v, want found rv=9", snap)
	}
}

// ── GVK resolution failures (shared by all four verbs) ──────────────────────────

// TestKubeExecutor_UnknownKind asserts an unregistered kind is a BAD_REQUEST-class
// fault on every verb that resolves a ref. The mapper has no "Widget" kind.
func TestKubeExecutor_UnknownKind(t *testing.T) {
	dc := dynfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), mbListKinds())
	k := NewKubeExecutorWithMapper(fake.NewSimpleClientset(), dc, mbMapper())
	ref := ResourceRef{APIVersion: "example.com/v1", Kind: "Widget", Namespace: "x", Name: "w-1"}

	t.Run("delete", func(t *testing.T) {
		if _, err := k.Delete(context.Background(), ref, ""); err == nil || !isBadRequestFault(err) {
			t.Errorf("Delete unknown kind err = %v, want BAD_REQUEST fault", err)
		}
	})
	t.Run("get_status", func(t *testing.T) {
		if _, err := k.GetStatus(context.Background(), ref); err == nil || !isBadRequestFault(err) {
			t.Errorf("GetStatus unknown kind err = %v, want BAD_REQUEST fault", err)
		}
	})
	t.Run("watch_status", func(t *testing.T) {
		if _, err := k.WatchStatus(context.Background(), ref, 1, func(string, StatusSnapshot) {}); err == nil || !isBadRequestFault(err) {
			t.Errorf("WatchStatus unknown kind err = %v, want BAD_REQUEST fault", err)
		}
	})
	t.Run("apply", func(t *testing.T) {
		manifest := []byte(`{"apiVersion":"example.com/v1","kind":"Widget","metadata":{"name":"w-1","namespace":"x"}}`)
		if _, err := k.Apply(context.Background(), manifest, "dc-api", false); err == nil || !isBadRequestFault(err) {
			t.Errorf("Apply unknown kind err = %v, want BAD_REQUEST fault", err)
		}
	})
}

// TestKubeExecutor_MissingName asserts an empty name is a BAD_REQUEST-class fault
// at resolve time, before any cluster call.
func TestKubeExecutor_MissingName(t *testing.T) {
	dc := dynfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), mbListKinds())
	k := NewKubeExecutorWithMapper(fake.NewSimpleClientset(), dc, mbMapper())
	ref := ResourceRef{APIVersion: "v1", Kind: "ConfigMap", Namespace: "x", Name: ""}
	if _, err := k.GetStatus(context.Background(), ref); err == nil || !isBadRequestFault(err) {
		t.Errorf("GetStatus missing name err = %v, want BAD_REQUEST fault", err)
	}
}

// TestKubeExecutor_NoMapper asserts an inventory-only executor (nil RESTMapper, as
// built by NewKubeExecutor) rejects a ref-resolving verb as BAD_REQUEST rather
// than panicking on the nil mapper.
func TestKubeExecutor_NoMapper(t *testing.T) {
	dc := dynfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), mbListKinds())
	k := NewKubeExecutor(fake.NewSimpleClientset(), dc) // no mapper
	if _, err := k.GetStatus(context.Background(), ResourceRef{APIVersion: "v1", Kind: "ConfigMap", Namespace: "x", Name: "cm-1"}); err == nil || !isBadRequestFault(err) {
		t.Errorf("GetStatus on inventory-only executor err = %v, want BAD_REQUEST fault", err)
	}
}

// ── WatchStatus ────────────────────────────────────────────────────────────────
//
// The watch verb tests inject a test-controlled fake watcher via a watch reactor:
// the test calls fw.Add/fw.Modify/fw.Stop directly and synchronizes on the emit
// channel, so the stream is fully deterministic with no sleeps. (The dynamic
// fake's default watcher streams tracker events but racing a Create against the
// executor opening the watch would be timing-dependent.)

// newWatchHarness returns a KubeExecutor whose ConfigMap watch is driven by the
// returned controllable watcher.
func newWatchHarness(t *testing.T) (*KubeExecutor, *watch.RaceFreeFakeWatcher) {
	t.Helper()
	dc := dynfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), mbListKinds())
	fw := watch.NewRaceFreeFake()
	dc.PrependWatchReactor("configmaps", func(ktesting.Action) (bool, watch.Interface, error) {
		return true, fw, nil
	})
	return NewKubeExecutorWithMapper(fake.NewSimpleClientset(), dc, mbMapper()), fw
}

// TestKubeExecutorWatchStatus_EmitsAndCapsAtMax drives an ADDED then a MODIFIED
// event and asserts the stages/snapshots emitted in order, plus termination at
// max_snapshots with the right reason.
func TestKubeExecutorWatchStatus_EmitsAndCapsAtMax(t *testing.T) {
	k, fw := newWatchHarness(t)
	ref := ResourceRef{APIVersion: "v1", Kind: "ConfigMap", Namespace: "tenant-abc", Name: "cm-1"}

	type emitted struct {
		stage string
		phase string
	}
	emits := make(chan emitted, 8)
	done := make(chan WatchResult, 1)
	go func() {
		res, err := k.WatchStatus(context.Background(), ref, 2, func(stage string, snap StatusSnapshot) {
			var s struct {
				Phase string `json:"phase"`
			}
			_ = json.Unmarshal(snap.Status, &s)
			emits <- emitted{stage: stage, phase: s.Phase}
		})
		if err != nil {
			t.Errorf("WatchStatus: %v", err)
		}
		done <- res
	}()

	fw.Add(configMap("cm-1", "tenant-abc", "Pending"))
	if e := <-emits; e.stage != "added" || e.phase != "Pending" {
		t.Errorf("event 1 = %+v, want {added Pending}", e)
	}
	fw.Modify(configMap("cm-1", "tenant-abc", "Running"))
	if e := <-emits; e.stage != "modified" || e.phase != "Running" {
		t.Errorf("event 2 = %+v, want {modified Running}", e)
	}

	select {
	case res := <-done:
		if res.SnapshotsSent != 2 {
			t.Errorf("SnapshotsSent = %d, want 2", res.SnapshotsSent)
		}
		if res.Reason != WatchReasonMaxSnapshots {
			t.Errorf("Reason = %q, want %q", res.Reason, WatchReasonMaxSnapshots)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("WatchStatus did not terminate after max_snapshots reached")
	}
}

// TestKubeExecutorWatchStatus_DefaultMax asserts an omitted max_snapshots (0) uses
// the documented default of 10 (not unbounded, not 0). We assert the default by
// emitting one event under the default and confirming the stream does NOT
// terminate at it, then closing the watch.
func TestKubeExecutorWatchStatus_DefaultMax(t *testing.T) {
	k, fw := newWatchHarness(t)
	ref := ResourceRef{APIVersion: "v1", Kind: "ConfigMap", Namespace: "tenant-abc", Name: "cm-1"}

	emits := make(chan struct{}, 16)
	done := make(chan WatchResult, 1)
	go func() {
		res, _ := k.WatchStatus(context.Background(), ref, 0, func(string, StatusSnapshot) { emits <- struct{}{} })
		done <- res
	}()

	// One event: with default=10 the stream stays open (a 0 default would have
	// terminated immediately; a 1 default would terminate here).
	fw.Add(configMap("cm-1", "tenant-abc", "Pending"))
	<-emits
	select {
	case res := <-done:
		t.Fatalf("stream terminated after one event (sent=%d reason=%s); default max_snapshots should be %d",
			res.SnapshotsSent, res.Reason, defaultWatchSnapshots)
	case <-time.After(100 * time.Millisecond):
		// Still open, as expected under the default cap.
	}

	fw.Stop()
	select {
	case res := <-done:
		if res.SnapshotsSent != 1 || res.Reason != WatchReasonClosed {
			t.Errorf("after close: sent=%d reason=%s, want sent=1 reason=%s", res.SnapshotsSent, res.Reason, WatchReasonClosed)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("WatchStatus did not terminate after the watch closed")
	}
}

// TestKubeExecutorWatchStatus_CtxCancel asserts a cancelled context terminates the
// stream with reason "deadline" and whatever snapshots were sent so far (here 0).
func TestKubeExecutorWatchStatus_CtxCancel(t *testing.T) {
	k, fw := newWatchHarness(t)
	_ = fw // watcher stays idle; cancellation drives termination
	ref := ResourceRef{APIVersion: "v1", Kind: "ConfigMap", Namespace: "tenant-abc", Name: "cm-1"}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan WatchResult, 1)
	go func() {
		res, err := k.WatchStatus(ctx, ref, 10, func(string, StatusSnapshot) {})
		if err != nil {
			t.Errorf("WatchStatus on ctx-cancel returned error %v, want nil", err)
		}
		done <- res
	}()

	cancel()
	select {
	case res := <-done:
		if res.Reason != WatchReasonDeadline {
			t.Errorf("Reason = %q, want %q on ctx cancel", res.Reason, WatchReasonDeadline)
		}
		if res.SnapshotsSent != 0 {
			t.Errorf("SnapshotsSent = %d, want 0 (no events before cancel)", res.SnapshotsSent)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("WatchStatus did not terminate on context cancellation")
	}
}

// TestKubeExecutorWatchStatus_WatchClosed asserts that a closed watch channel
// terminates the stream with reason "watch_closed".
func TestKubeExecutorWatchStatus_WatchClosed(t *testing.T) {
	k, fw := newWatchHarness(t)
	ref := ResourceRef{APIVersion: "v1", Kind: "ConfigMap", Namespace: "tenant-abc", Name: "cm-1"}

	done := make(chan WatchResult, 1)
	go func() {
		res, err := k.WatchStatus(context.Background(), ref, 10, func(string, StatusSnapshot) {})
		if err != nil {
			t.Errorf("WatchStatus on watch-close returned error %v, want nil", err)
		}
		done <- res
	}()

	fw.Stop()
	select {
	case res := <-done:
		if res.Reason != WatchReasonClosed {
			t.Errorf("Reason = %q, want %q on watch close", res.Reason, WatchReasonClosed)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("WatchStatus did not terminate after the watch closed")
	}
}

// TestKubeExecutorWatchStatus_NegativeMax asserts a negative max_snapshots is a
// BAD_REQUEST-class fault, with no watch opened.
func TestKubeExecutorWatchStatus_NegativeMax(t *testing.T) {
	k, _ := newWatchHarness(t)
	ref := ResourceRef{APIVersion: "v1", Kind: "ConfigMap", Namespace: "tenant-abc", Name: "cm-1"}
	if _, err := k.WatchStatus(context.Background(), ref, -1, func(string, StatusSnapshot) {}); err == nil || !isBadRequestFault(err) {
		t.Errorf("WatchStatus(max=-1) err = %v, want BAD_REQUEST fault", err)
	}
}

// TestKubeExecutorWatchStatus_SkipsNonAddModify asserts the watch ignores event
// types that aren't Added/Modified (e.g. Deleted) — only status snapshots count
// toward the cap.
func TestKubeExecutorWatchStatus_SkipsNonAddModify(t *testing.T) {
	k, fw := newWatchHarness(t)
	ref := ResourceRef{APIVersion: "v1", Kind: "ConfigMap", Namespace: "tenant-abc", Name: "cm-1"}

	emits := make(chan string, 8)
	done := make(chan WatchResult, 1)
	go func() {
		res, _ := k.WatchStatus(context.Background(), ref, 1, func(stage string, _ StatusSnapshot) { emits <- stage })
		done <- res
	}()

	// A Deleted event must NOT be emitted nor counted.
	fw.Delete(configMap("cm-1", "tenant-abc", ""))
	// Then a real Added event reaches the cap of 1.
	fw.Add(configMap("cm-1", "tenant-abc", "Pending"))

	if s := <-emits; s != "added" {
		t.Errorf("first emitted stage = %q, want added (Deleted must be skipped)", s)
	}
	select {
	case res := <-done:
		if res.SnapshotsSent != 1 || res.Reason != WatchReasonMaxSnapshots {
			t.Errorf("result = %+v, want sent=1 reason=%s", res, WatchReasonMaxSnapshots)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("WatchStatus did not terminate")
	}
}
