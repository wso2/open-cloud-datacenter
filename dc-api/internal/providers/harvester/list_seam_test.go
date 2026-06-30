package harvester

// list_seam_test.go proves the harvester collection reads route through the
// cluster-access seam (M-D, the list slice):
//
//   - Toggle OFF / local Direct client → ListVMs/ListImages/ListNetworks read via
//     the dynamic client, byte-identical to pre-list-slice behaviour (the agent is
//     never touched).
//   - Toggle ON + live session → the same three ops route to Session.List and the
//     field extraction (BackendUID, displayName, …) operates on the agent-returned
//     items exactly as on the Direct path.
//   - A REMOTE client (no dynamic) can now serve the list ops through the agent —
//     previously they returned localOnlyErr.

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/rs/zerolog"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"

	"github.com/wso2/dc-api/internal/agentgw"
	"github.com/wso2/dc-api/internal/providers/clusteraccess"
	"github.com/wso2/dc-api/internal/providers/common"
)

// listStubSession returns a canned ListResult for any List call and records the
// ref it saw. The non-list methods are inert.
type listStubSession struct {
	items   []json.RawMessage
	lastRef agentgw.ListRef
}

func (s *listStubSession) List(_ context.Context, ref agentgw.ListRef) (agentgw.ListResult, error) {
	s.lastRef = ref
	return agentgw.ListResult{Items: s.items}, nil
}
func (s *listStubSession) GetStatus(context.Context, agentgw.ResourceRef) (agentgw.StatusSnapshot, error) {
	return agentgw.StatusSnapshot{}, nil
}
func (s *listStubSession) Apply(context.Context, json.RawMessage, string, bool) (agentgw.ApplyResult, error) {
	return agentgw.ApplyResult{}, nil
}
func (s *listStubSession) Delete(context.Context, agentgw.ResourceRef, string) (agentgw.DeleteResult, error) {
	return agentgw.DeleteResult{}, nil
}
func (s *listStubSession) WatchStatus(context.Context, agentgw.ResourceRef, int, func(string, agentgw.StatusSnapshot)) (agentgw.WatchResult, error) {
	return agentgw.WatchResult{}, nil
}

var _ agentgw.Session = (*listStubSession)(nil)

// routeListToAgent builds a Routed accessor whose decision routes VerbList to the
// given agent session (for any GVR) and leaves every other verb on Direct.
func routeListToAgent(sess agentgw.Session, direct clusteraccess.Accessor) clusteraccess.Accessor {
	agentAccessor := clusteraccess.NewAgentBacked(sess, "lk", "zone-1", "dc-api", clusteraccess.DefaultGVKMapper(), zerolog.Nop())
	return clusteraccess.NewRouted(direct, func(v clusteraccess.Verb, _ schema.GroupVersionResource) (clusteraccess.Accessor, bool) {
		if v == clusteraccess.VerbList {
			return agentAccessor, true
		}
		return nil, false
	})
}

// rawVM builds a raw KubeVirt VirtualMachine JSON object the agent would return.
func rawVM(t *testing.T, name, ns, printable string) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(map[string]interface{}{
		"apiVersion": "kubevirt.io/v1",
		"kind":       "VirtualMachine",
		"metadata":   map[string]interface{}{"name": name, "namespace": ns},
		"status":     map[string]interface{}{"printableStatus": printable},
	})
	if err != nil {
		t.Fatalf("marshal vm: %v", err)
	}
	return b
}

// ── ListVMs ────────────────────────────────────────────────────────────────────

func TestListVMs_DirectAndAgentSeamsAgree(t *testing.T) {
	const tenantID, projectID = "tenant", "proj"
	ns := common.NamespaceForProject(tenantID, projectID)

	// Direct seam: two managed VMs seeded in the fake store.
	vm1 := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "kubevirt.io/v1", "kind": "VirtualMachine",
		"metadata": map[string]interface{}{
			"name": "web-01", "namespace": ns,
			"labels": map[string]interface{}{"dc-api/managed": "true"},
		},
		"status": map[string]interface{}{"printableStatus": "Running"},
	}}
	gvrToListKind := map[schema.GroupVersionResource]string{harvesterVMResource: "VirtualMachineList"}
	dynDirect := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), gvrToListKind, vm1)
	directClient := &Client{dynamic: dynDirect, access: clusteraccess.NewDirect(dynDirect)}

	directRes, err := directClient.ListVMs(context.Background(), tenantID, projectID)
	if err != nil {
		t.Fatalf("Direct ListVMs: %v", err)
	}
	if len(directRes) != 1 || directRes[0].Name != "web-01" {
		t.Fatalf("Direct ListVMs = %+v, want one web-01", directRes)
	}

	// Agent seam: a REMOTE client with NO dynamic; the list routes to the stub
	// session returning the same single VM.
	sess := &listStubSession{items: []json.RawMessage{rawVM(t, "web-01", ns, "Running")}}
	remoteClient := NewRemoteClient(routeListToAgent(sess, clusteraccess.NewNoCreds("lk", "zone-1")), "lk", "zone-1")

	agentRes, err := remoteClient.ListVMs(context.Background(), tenantID, projectID)
	if err != nil {
		t.Fatalf("Agent ListVMs: %v", err)
	}
	if len(agentRes) != 1 || agentRes[0].Name != "web-01" {
		t.Fatalf("Agent ListVMs = %+v, want one web-01", agentRes)
	}

	// The agent saw the right GVK + namespace + label selector.
	if sess.lastRef.APIVersion != "kubevirt.io/v1" || sess.lastRef.Kind != "VirtualMachine" {
		t.Errorf("agent list GVK = %s/%s, want kubevirt.io/v1/VirtualMachine", sess.lastRef.APIVersion, sess.lastRef.Kind)
	}
	if sess.lastRef.Namespace != ns {
		t.Errorf("agent list namespace = %q, want %q", sess.lastRef.Namespace, ns)
	}
	if sess.lastRef.LabelSelector != "dc-api/managed=true" {
		t.Errorf("agent list selector = %q, want dc-api/managed=true", sess.lastRef.LabelSelector)
	}

	// Equivalence: same BackendUID + status on both seams.
	if directRes[0].BackendUID != agentRes[0].BackendUID {
		t.Errorf("BackendUID differs: Direct=%q Agent=%q", directRes[0].BackendUID, agentRes[0].BackendUID)
	}
	if directRes[0].Status != agentRes[0].Status {
		t.Errorf("Status differs: Direct=%q Agent=%q", directRes[0].Status, agentRes[0].Status)
	}
}

// ── ListImages ───────────────────────────────────────────────────────────────

func TestListImages_RoutesThroughAgent(t *testing.T) {
	rawImg, _ := json.Marshal(map[string]interface{}{
		"apiVersion": "harvesterhci.io/v1beta1", "kind": "VirtualMachineImage",
		"metadata": map[string]interface{}{"name": "image-abc", "namespace": "default"},
		"spec":     map[string]interface{}{"displayName": "ubuntu-22.04"},
	})
	sess := &listStubSession{items: []json.RawMessage{rawImg}}
	remoteClient := NewRemoteClient(routeListToAgent(sess, clusteraccess.NewNoCreds("lk", "zone-1")), "lk", "zone-1")

	imgs, err := remoteClient.ListImages(context.Background())
	if err != nil {
		t.Fatalf("ListImages: %v", err)
	}
	if len(imgs) != 1 {
		t.Fatalf("images = %d, want 1", len(imgs))
	}
	if imgs[0].ID != "default/image-abc" || imgs[0].DisplayName != "ubuntu-22.04" {
		t.Errorf("image = %+v, want default/image-abc ubuntu-22.04", imgs[0])
	}
	// Cross-namespace list: namespace must be empty (all namespaces).
	if sess.lastRef.Namespace != "" {
		t.Errorf("image list namespace = %q, want empty (all namespaces)", sess.lastRef.Namespace)
	}
	if sess.lastRef.Kind != "VirtualMachineImage" {
		t.Errorf("image list kind = %q, want VirtualMachineImage", sess.lastRef.Kind)
	}
}

// ── ListNetworks ───────────────────────────────────────────────────────────────

func TestListNetworks_RoutesThroughAgent(t *testing.T) {
	rawNet, _ := json.Marshal(map[string]interface{}{
		"apiVersion": "k8s.cni.cncf.io/v1", "kind": "NetworkAttachmentDefinition",
		"metadata": map[string]interface{}{
			"name": "vlan100", "namespace": "default",
			"annotations": map[string]interface{}{"field.cattle.io/description": "VLAN 100"},
		},
	})
	sess := &listStubSession{items: []json.RawMessage{rawNet}}
	remoteClient := NewRemoteClient(routeListToAgent(sess, clusteraccess.NewNoCreds("lk", "zone-1")), "lk", "zone-1")

	nets, err := remoteClient.ListNetworks(context.Background())
	if err != nil {
		t.Fatalf("ListNetworks: %v", err)
	}
	if len(nets) != 1 {
		t.Fatalf("networks = %d, want 1", len(nets))
	}
	if nets[0].ID != "default/vlan100" || nets[0].DisplayName != "VLAN 100" {
		t.Errorf("network = %+v, want default/vlan100 'VLAN 100'", nets[0])
	}
	if sess.lastRef.Kind != "NetworkAttachmentDefinition" {
		t.Errorf("network list kind = %q, want NetworkAttachmentDefinition", sess.lastRef.Kind)
	}
}

// TestListVMs_DefaultClientUsesDirect proves the toggle-OFF path: a client with
// only a Direct accessor lists via the dynamic client (byte-identical to
// pre-list-slice) and the agent is never consulted.
func TestListVMs_DefaultClientUsesDirect(t *testing.T) {
	const tenantID, projectID = "tenant", "proj"
	ns := common.NamespaceForProject(tenantID, projectID)
	vm := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "kubevirt.io/v1", "kind": "VirtualMachine",
		"metadata": map[string]interface{}{
			"name": "web-01", "namespace": ns,
			"labels": map[string]interface{}{"dc-api/managed": "true"},
		},
		"status": map[string]interface{}{"printableStatus": "Running"},
	}}
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(
		runtime.NewScheme(),
		map[schema.GroupVersionResource]string{harvesterVMResource: "VirtualMachineList"},
		vm,
	)
	c := &Client{dynamic: dyn, access: clusteraccess.NewDirect(dyn)}

	res, err := c.ListVMs(context.Background(), tenantID, projectID)
	if err != nil {
		t.Fatalf("ListVMs: %v", err)
	}
	if len(res) != 1 || res[0].BackendUID != ns+":web-01" {
		t.Fatalf("ListVMs = %+v, want one web-01 with BackendUID %s:web-01", res, ns)
	}

	// Sanity: the object is really served by the direct dynamic path.
	if _, err := dyn.Resource(harvesterVMResource).Namespace(ns).List(context.Background(), metav1.ListOptions{}); err != nil {
		t.Fatalf("direct dynamic List failed: %v", err)
	}
}
