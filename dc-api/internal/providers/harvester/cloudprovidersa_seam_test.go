package harvester

// cloudprovidersa_seam_test.go proves the cloud-provider ServiceAccount slice
// (F32) of the credential-locality work: EnsureCloudProviderSA routes its three
// creates (ServiceAccount, RoleBinding, token Secret) AND the token-population
// poll's read through the cluster-access seam (c.access), so it works for a
// REMOTE (agent-only) zone, while the local Direct path is unchanged.
//
//   - Remote (no dynamic) → the three creates route through c.access.Create with
//     the correct GVRs/namespace/names/shapes, and the token is read from the
//     Secret's .data.token via c.access.Get (NOT c.dynamic). Fixed names, no
//     generateName.
//   - Local (Direct over a fake dynamic) → the three creates land in the fake
//     store and the token is read back from the seeded Secret.
//   - The three GVRs are the ones the harvester client already declares, so the
//     seam addresses the same resources the direct path did.

import (
	"context"
	"encoding/base64"
	"sync"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"

	"github.com/wso2/dc-api/internal/providers/clusteraccess"
)

const (
	testSANamespace = "dc-teamalpha-web"
	// The SA/RoleBinding name and token-Secret name the bootstrap derives.
	wantSAName     = "harvester-cloud-provider-" + testSANamespace
	wantSecretName = wantSAName + "-token"
	// A real SA token would be a JWT; any non-empty bytes prove the decode+return
	// path. base64 of "sa-token-bytes".
	fakeTokenPlain = "sa-token-bytes"
)

// saRecordingAccessor records every Create (GVR, namespace, object) and serves a
// programmable Secret from Get so the token-population poll returns on the first
// iteration. It is deliberately independent of the other seam tests' accessors:
// only Create and Get are meaningful here; the rest satisfy the interface inertly.
type saRecordingAccessor struct {
	mu sync.Mutex

	creates []recordedCreate

	getCalls    int
	lastGetGVR  schema.GroupVersionResource
	lastGetNS   string
	lastGetName string
	// tokenB64 is what Get returns in the Secret's .data.token. Empty → the poll
	// sees an unpopulated Secret (used to prove the poll actually reads .data).
	tokenB64 string
}

type recordedCreate struct {
	gvr schema.GroupVersionResource
	ns  string
	obj *unstructured.Unstructured
}

var _ clusteraccess.Accessor = (*saRecordingAccessor)(nil)

func (a *saRecordingAccessor) Get(_ context.Context, gvr schema.GroupVersionResource, ns, name string, _ metav1.GetOptions) (*unstructured.Unstructured, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.getCalls++
	a.lastGetGVR = gvr
	a.lastGetNS = ns
	a.lastGetName = name
	// Return a Secret shell with .data.token populated (agent full-object Get shape).
	return &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "v1",
		"kind":       "Secret",
		"metadata": map[string]interface{}{
			"name":      name,
			"namespace": ns,
		},
		"data": map[string]interface{}{
			"token": a.tokenB64,
		},
	}}, nil
}

func (a *saRecordingAccessor) List(context.Context, schema.GroupVersionResource, string, metav1.ListOptions) (*unstructured.UnstructuredList, error) {
	return &unstructured.UnstructuredList{}, nil
}

func (a *saRecordingAccessor) Create(_ context.Context, gvr schema.GroupVersionResource, ns string, obj *unstructured.Unstructured, _ metav1.CreateOptions) (*unstructured.Unstructured, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.creates = append(a.creates, recordedCreate{gvr: gvr, ns: ns, obj: obj})
	return obj, nil
}

func (a *saRecordingAccessor) Apply(_ context.Context, _ schema.GroupVersionResource, _ string, obj *unstructured.Unstructured, _ string, _ bool) (*unstructured.Unstructured, error) {
	return obj, nil
}

func (a *saRecordingAccessor) Update(_ context.Context, _ schema.GroupVersionResource, _ string, obj *unstructured.Unstructured, _ metav1.UpdateOptions) (*unstructured.Unstructured, error) {
	return obj, nil
}

func (a *saRecordingAccessor) Delete(context.Context, schema.GroupVersionResource, string, string, metav1.DeleteOptions) error {
	return nil
}

// createFor returns the recorded create for the given GVR, or fails the test.
func (a *saRecordingAccessor) createFor(t *testing.T, gvr schema.GroupVersionResource) recordedCreate {
	t.Helper()
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, c := range a.creates {
		if c.gvr == gvr {
			return c
		}
	}
	t.Fatalf("no create recorded for GVR %v (recorded: %d creates)", gvr, len(a.creates))
	return recordedCreate{}
}

// ── Remote path: the 3 creates + the token Get route through the seam ────────

func TestEnsureCloudProviderSA_Remote_RoutesCreatesAndTokenGet(t *testing.T) {
	rec := &saRecordingAccessor{tokenB64: base64.StdEncoding.EncodeToString([]byte(fakeTokenPlain))}
	// A REMOTE client has no dynamic; every op MUST go through the accessor. If the
	// SA bootstrap still touched c.dynamic this would panic on a nil client.
	c := NewRemoteClient(rec, "lk", "zone-1")

	token, err := c.EnsureCloudProviderSA(context.Background(), testSANamespace)
	if err != nil {
		t.Fatalf("EnsureCloudProviderSA (remote): %v", err)
	}

	// The returned token is the DECODED .data.token (raw bytes, not base64).
	if string(token) != fakeTokenPlain {
		t.Errorf("returned token = %q, want %q (decoded from .data.token)", string(token), fakeTokenPlain)
	}

	// Exactly three creates routed through the seam.
	if len(rec.creates) != 3 {
		t.Fatalf("accessor Create called %d times, want 3 (SA, RoleBinding, Secret)", len(rec.creates))
	}

	// ── ServiceAccount create ────────────────────────────────────────────────
	sa := rec.createFor(t, serviceAccountsGVR)
	if sa.ns != testSANamespace {
		t.Errorf("SA create namespace = %q, want %q", sa.ns, testSANamespace)
	}
	assertNameNoGenerateName(t, sa.obj, wantSAName, "ServiceAccount")
	if kind, _, _ := unstructured.NestedString(sa.obj.Object, "kind"); kind != "ServiceAccount" {
		t.Errorf("SA create kind = %q, want ServiceAccount", kind)
	}

	// ── RoleBinding create ───────────────────────────────────────────────────
	rb := rec.createFor(t, roleBindingsGVR)
	if rb.ns != testSANamespace {
		t.Errorf("RoleBinding create namespace = %q, want %q", rb.ns, testSANamespace)
	}
	assertNameNoGenerateName(t, rb.obj, wantSAName, "RoleBinding")
	// The RoleBinding must bind the harvesterhci.io:cloudprovider ClusterRole to the SA.
	roleRefKind, _, _ := unstructured.NestedString(rb.obj.Object, "roleRef", "kind")
	roleRefName, _, _ := unstructured.NestedString(rb.obj.Object, "roleRef", "name")
	if roleRefKind != "ClusterRole" || roleRefName != "harvesterhci.io:cloudprovider" {
		t.Errorf("RoleBinding roleRef = {kind:%q name:%q}, want {ClusterRole harvesterhci.io:cloudprovider}", roleRefKind, roleRefName)
	}
	subjects, _, _ := unstructured.NestedSlice(rb.obj.Object, "subjects")
	if len(subjects) != 1 {
		t.Fatalf("RoleBinding subjects = %d, want 1", len(subjects))
	}
	subj, _ := subjects[0].(map[string]interface{})
	if subj["kind"] != "ServiceAccount" || subj["name"] != wantSAName || subj["namespace"] != testSANamespace {
		t.Errorf("RoleBinding subject = %v, want SA %q in %q", subj, wantSAName, testSANamespace)
	}

	// ── token Secret create ──────────────────────────────────────────────────
	sec := rec.createFor(t, secretsGVR)
	if sec.ns != testSANamespace {
		t.Errorf("Secret create namespace = %q, want %q", sec.ns, testSANamespace)
	}
	assertNameNoGenerateName(t, sec.obj, wantSecretName, "Secret")
	if typ, _, _ := unstructured.NestedString(sec.obj.Object, "type"); typ != "kubernetes.io/service-account-token" {
		t.Errorf("Secret type = %q, want kubernetes.io/service-account-token", typ)
	}
	annot, _, _ := unstructured.NestedString(sec.obj.Object, "metadata", "annotations", "kubernetes.io/service-account.name")
	if annot != wantSAName {
		t.Errorf("Secret service-account.name annotation = %q, want %q", annot, wantSAName)
	}

	// ── token-population Get routed through the seam ─────────────────────────
	if rec.getCalls != 1 {
		t.Errorf("accessor Get called %d times, want 1 (single poll iteration)", rec.getCalls)
	}
	if rec.lastGetGVR != secretsGVR {
		t.Errorf("token Get GVR = %v, want %v", rec.lastGetGVR, secretsGVR)
	}
	if rec.lastGetNS != testSANamespace || rec.lastGetName != wantSecretName {
		t.Errorf("token Get = %s/%s, want %s/%s", rec.lastGetNS, rec.lastGetName, testSANamespace, wantSecretName)
	}
}

// assertNameNoGenerateName asserts an object carries a fixed metadata.name (SSA
// needs it) and NO metadata.generateName (SSA cannot use it — same contract the
// image slice enforces).
func assertNameNoGenerateName(t *testing.T, obj *unstructured.Unstructured, wantName, what string) {
	t.Helper()
	name, hasName, _ := unstructured.NestedString(obj.Object, "metadata", "name")
	if !hasName || name != wantName {
		t.Errorf("%s metadata.name = %q, want %q", what, name, wantName)
	}
	if _, hasGen, _ := unstructured.NestedString(obj.Object, "metadata", "generateName"); hasGen {
		t.Errorf("%s sets metadata.generateName; SSA (the agent create path) cannot use it", what)
	}
}

// TestEnsureCloudProviderSA_Remote_TokenReadFromData proves the returned token is
// what the SEAM's Get supplies in .data.token — swap the seeded token and the
// return value follows, i.e. the read is genuinely routed (not a constant).
func TestEnsureCloudProviderSA_Remote_TokenReadFromData(t *testing.T) {
	const other = "a-different-token"
	rec := &saRecordingAccessor{tokenB64: base64.StdEncoding.EncodeToString([]byte(other))}
	c := NewRemoteClient(rec, "lk", "zone-1")

	token, err := c.EnsureCloudProviderSA(context.Background(), testSANamespace)
	if err != nil {
		t.Fatalf("EnsureCloudProviderSA: %v", err)
	}
	if string(token) != other {
		t.Errorf("returned token = %q, want %q — the token must come from the seam Get's .data.token", string(token), other)
	}
}

// ── Routing: the SA-bootstrap verbs toggle between agent and Direct ──────────

// TestEnsureCloudProviderSA_Routed_TogglesBetweenAgentAndDirect proves the Routed
// decision gates the bootstrap per-verb/per-family: with the SA/RoleBinding/Secret
// creates (VerbCreate) and the Secret read (VerbGet) activated for their GVRs, the
// ops route to the agent accessor; with the decision de-activated they fall to
// Direct. This mirrors the per-family RouteVerbs allow-set the registry enforces
// (SA/RoleBinding route VerbCreate; Secret routes VerbGet+VerbCreate).
func TestEnsureCloudProviderSA_Routed_TogglesBetweenAgentAndDirect(t *testing.T) {
	// Toggle ON → the agent accessor gets the creates + the token Get; Direct is
	// untouched.
	t.Run("on routes to agent", func(t *testing.T) {
		agent := &saRecordingAccessor{tokenB64: base64.StdEncoding.EncodeToString([]byte(fakeTokenPlain))}
		direct := &saRecordingAccessor{tokenB64: base64.StdEncoding.EncodeToString([]byte(fakeTokenPlain))}
		routed := clusteraccess.NewRouted(direct, func(v clusteraccess.Verb, gvr schema.GroupVersionResource) (clusteraccess.Accessor, bool) {
			switch gvr {
			case serviceAccountsGVR, roleBindingsGVR:
				if v == clusteraccess.VerbCreate {
					return agent, true
				}
			case secretsGVR:
				if v == clusteraccess.VerbCreate || v == clusteraccess.VerbGet {
					return agent, true
				}
			}
			return nil, false
		})
		c := &Client{access: routed}
		if _, err := c.EnsureCloudProviderSA(context.Background(), testSANamespace); err != nil {
			t.Fatalf("EnsureCloudProviderSA: %v", err)
		}
		if len(agent.creates) != 3 {
			t.Errorf("agent Create called %d times, want 3", len(agent.creates))
		}
		if len(direct.creates) != 0 {
			t.Errorf("direct Create called %d times with toggle ON, want 0", len(direct.creates))
		}
		if agent.getCalls != 1 {
			t.Errorf("agent Get called %d times, want 1", agent.getCalls)
		}
		if direct.getCalls != 0 {
			t.Errorf("direct Get called %d times with toggle ON, want 0", direct.getCalls)
		}
	})

	// Toggle OFF → Direct gets the creates + the token Get; the agent is untouched.
	t.Run("off uses direct", func(t *testing.T) {
		agent := &saRecordingAccessor{tokenB64: base64.StdEncoding.EncodeToString([]byte(fakeTokenPlain))}
		direct := &saRecordingAccessor{tokenB64: base64.StdEncoding.EncodeToString([]byte(fakeTokenPlain))}
		routed := clusteraccess.NewRouted(direct, func(clusteraccess.Verb, schema.GroupVersionResource) (clusteraccess.Accessor, bool) {
			return nil, false
		})
		c := &Client{access: routed}
		if _, err := c.EnsureCloudProviderSA(context.Background(), testSANamespace); err != nil {
			t.Fatalf("EnsureCloudProviderSA: %v", err)
		}
		if len(direct.creates) != 3 {
			t.Errorf("direct Create called %d times, want 3", len(direct.creates))
		}
		if len(agent.creates) != 0 {
			t.Errorf("agent Create called %d times with toggle OFF, want 0", len(agent.creates))
		}
		if direct.getCalls != 1 {
			t.Errorf("direct Get called %d times, want 1", direct.getCalls)
		}
		if agent.getCalls != 0 {
			t.Errorf("agent Get called %d times with toggle OFF, want 0", agent.getCalls)
		}
	})
}

// ── Local Direct path: the bootstrap still works against a fake dynamic ──────

// newFakeDynamicForSA returns a fake dynamic client that already holds a token
// Secret with .data.token populated, so the local Direct EnsureCloudProviderSA
// finds the token on the first poll (the fake has no token controller). The SA
// and RoleBinding creates land freshly.
func newFakeDynamicForSA(t *testing.T) *dynamicfake.FakeDynamicClient {
	t.Helper()
	seededSecret := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "v1",
		"kind":       "Secret",
		"metadata": map[string]interface{}{
			"name":      wantSecretName,
			"namespace": testSANamespace,
		},
		"type": "kubernetes.io/service-account-token",
		"data": map[string]interface{}{
			"token": base64.StdEncoding.EncodeToString([]byte(fakeTokenPlain)),
		},
	}}
	gvrToListKind := map[schema.GroupVersionResource]string{
		serviceAccountsGVR: "ServiceAccountList",
		roleBindingsGVR:    "RoleBindingList",
		secretsGVR:         "SecretList",
	}
	return dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), gvrToListKind, seededSecret)
}

func TestEnsureCloudProviderSA_Local_UsesDirect(t *testing.T) {
	dyn := newFakeDynamicForSA(t)
	// The Secret create in the bootstrap will hit an already-existing seeded Secret;
	// IsAlreadyExists is treated as success (idempotent), exactly the local POST
	// contract the code keeps.
	c := &Client{dynamic: dyn, access: clusteraccess.NewDirect(dyn)}

	token, err := c.EnsureCloudProviderSA(context.Background(), testSANamespace)
	if err != nil {
		t.Fatalf("EnsureCloudProviderSA (local): %v", err)
	}
	if string(token) != fakeTokenPlain {
		t.Errorf("returned token = %q, want %q", string(token), fakeTokenPlain)
	}

	// The SA and RoleBinding really landed in the fake store under the fixed names.
	if _, err := dyn.Resource(serviceAccountsGVR).Namespace(testSANamespace).Get(context.Background(), wantSAName, metav1.GetOptions{}); err != nil {
		t.Errorf("ServiceAccount not found in direct store: %v", err)
	}
	if _, err := dyn.Resource(roleBindingsGVR).Namespace(testSANamespace).Get(context.Background(), wantSAName, metav1.GetOptions{}); err != nil {
		t.Errorf("RoleBinding not found in direct store: %v", err)
	}
}
