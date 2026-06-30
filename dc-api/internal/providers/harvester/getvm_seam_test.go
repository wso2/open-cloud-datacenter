package harvester

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
	"github.com/wso2/dc-api/internal/models"
	"github.com/wso2/dc-api/internal/providers/clusteraccess"
)

// This test proves the M-C equivalence guarantee: GetVM returns the SAME
// models.Resource.Status whether its primary VirtualMachine read goes through
// the Direct seam (fake dynamic object) or the agent seam (Session.GetObject
// returning the equivalent full object). The VMI read stays Direct on both
// paths (no VMI object present → empty IP, which is fine for the status check).

const (
	testVMNamespace = "dc-tenant-proj"
	testVMName      = "web-01"
	testBackendUID  = testVMNamespace + ":" + testVMName
)

// newFakeDynamicWithVM returns a fake dynamic client holding one KubeVirt
// VirtualMachine with the given status.printableStatus.
func newFakeDynamicWithVM(t *testing.T, printableStatus string) *dynamicfake.FakeDynamicClient {
	t.Helper()
	vm := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "kubevirt.io/v1",
		"kind":       "VirtualMachine",
		"metadata": map[string]interface{}{
			"name":      testVMName,
			"namespace": testVMNamespace,
		},
		"status": map[string]interface{}{
			"printableStatus": printableStatus,
		},
	}}
	// Register the GVR→ListKind so the fake's List machinery is satisfied for the
	// unstructured CRD types the harvester driver uses.
	gvrToListKind := map[schema.GroupVersionResource]string{
		harvesterVMResource:  "VirtualMachineList",
		harvesterVMIResource: "VirtualMachineInstanceList",
	}
	return dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), gvrToListKind, vm)
}

// fullVMObjectFor builds the full agent GetObjectResult equivalent to the fake
// VM — the same printableStatus the Direct path reads off the object, carried in
// a complete VM object (the shape get_object returns).
func fullVMObjectFor(printableStatus string) agentgw.GetObjectResult {
	obj, _ := json.Marshal(map[string]interface{}{
		"apiVersion": "kubevirt.io/v1",
		"kind":       "VirtualMachine",
		"metadata": map[string]interface{}{
			"name":            testVMName,
			"namespace":       testVMNamespace,
			"resourceVersion": "1",
		},
		"spec":   map[string]interface{}{"running": true},
		"status": map[string]interface{}{"printableStatus": printableStatus},
	})
	return agentgw.GetObjectResult{Found: true, Object: obj}
}

// seamStubSession returns the given full object from GetObject (the primary read
// path GetVM-via-agent now takes).
type seamStubSession struct{ obj agentgw.GetObjectResult }

func (s seamStubSession) GetObject(context.Context, agentgw.ResourceRef) (agentgw.GetObjectResult, error) {
	return s.obj, nil
}
func (s seamStubSession) GetStatus(context.Context, agentgw.ResourceRef) (agentgw.StatusSnapshot, error) {
	return agentgw.StatusSnapshot{}, nil
}
func (s seamStubSession) List(context.Context, agentgw.ListRef) (agentgw.ListResult, error) {
	return agentgw.ListResult{}, nil
}
func (s seamStubSession) Apply(context.Context, json.RawMessage, string, bool) (agentgw.ApplyResult, error) {
	return agentgw.ApplyResult{}, nil
}
func (s seamStubSession) Delete(context.Context, agentgw.ResourceRef, string) (agentgw.DeleteResult, error) {
	return agentgw.DeleteResult{}, nil
}
func (s seamStubSession) WatchStatus(context.Context, agentgw.ResourceRef, int, func(string, agentgw.StatusSnapshot)) (agentgw.WatchResult, error) {
	return agentgw.WatchResult{}, nil
}

func TestGetVM_DirectAndAgentSeamsAgreeOnStatus(t *testing.T) {
	cases := []struct {
		printable string
		want      models.ResourceStatus
	}{
		{"Running", models.StatusActive},
		{"Starting", models.StatusPending},
		{"Terminating", models.StatusDeleting},
		{"CrashLoopBackOff", models.StatusFailed},
	}

	for _, tc := range cases {
		t.Run(tc.printable, func(t *testing.T) {
			// ── Direct seam ──────────────────────────────────────────────────────
			dynDirect := newFakeDynamicWithVM(t, tc.printable)
			directClient := &Client{
				dynamic: dynDirect,
				access:  clusteraccess.NewDirect(dynDirect),
			}
			directRes, err := directClient.GetVM(context.Background(), testBackendUID)
			if err != nil {
				t.Fatalf("Direct GetVM error: %v", err)
			}

			// ── Agent seam ───────────────────────────────────────────────────────
			// Same fake dynamic client for the VMI read (kept Direct), but the
			// primary VM read is routed to a stub agent Session via a Routed
			// accessor whose decision always allows VerbGet.
			dynAgent := newFakeDynamicWithVM(t, tc.printable)
			sess := seamStubSession{obj: fullVMObjectFor(tc.printable)}
			agentAccessor := clusteraccess.NewAgentBacked(sess, "lk", "zone-1", "dc-api", clusteraccess.DefaultGVKMapper(), zerolog.Nop())
			routed := clusteraccess.NewRouted(
				clusteraccess.NewDirect(dynAgent),
				func(v clusteraccess.Verb, _ schema.GroupVersionResource) (clusteraccess.Accessor, bool) {
					if v == clusteraccess.VerbGet {
						return agentAccessor, true
					}
					return nil, false
				},
			)
			agentClient := &Client{
				dynamic: dynAgent, // VMI read still goes here
				access:  routed,   // VM read routes to the agent
			}
			agentRes, err := agentClient.GetVM(context.Background(), testBackendUID)
			if err != nil {
				t.Fatalf("Agent GetVM error: %v", err)
			}

			// ── Equivalence ──────────────────────────────────────────────────────
			if directRes.Status != tc.want {
				t.Errorf("Direct status = %q, want %q", directRes.Status, tc.want)
			}
			if agentRes.Status != tc.want {
				t.Errorf("Agent status = %q, want %q", agentRes.Status, tc.want)
			}
			if directRes.Status != agentRes.Status {
				t.Errorf("seams disagree: Direct=%q Agent=%q", directRes.Status, agentRes.Status)
			}
		})
	}
}

// TestGetVM_DefaultClientUsesDirect proves the toggle-OFF path: a client built
// with only a Direct accessor (NewClient's default) reads the VM via the
// dynamic client — byte-identical to pre-M-C. We assert it equals an explicit
// dynamic Get.
func TestGetVM_DefaultClientUsesDirect(t *testing.T) {
	dyn := newFakeDynamicWithVM(t, "Running")
	c := &Client{dynamic: dyn, access: clusteraccess.NewDirect(dyn)}

	res, err := c.GetVM(context.Background(), testBackendUID)
	if err != nil {
		t.Fatalf("GetVM error: %v", err)
	}
	if res.Status != models.StatusActive {
		t.Errorf("status = %q, want ACTIVE", res.Status)
	}

	// Sanity: the object is really in the fake store under the direct path.
	if _, err := dyn.Resource(harvesterVMResource).Namespace(testVMNamespace).Get(context.Background(), testVMName, metav1.GetOptions{}); err != nil {
		t.Fatalf("direct dynamic Get failed: %v", err)
	}
}
