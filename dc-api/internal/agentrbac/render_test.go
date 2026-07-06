package agentrbac

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/wso2/dc-api/internal/providers/clusteraccess"
)

// repoRoot resolves the repository root from this test file's location so the
// drift test works under `go test ./...` in CI with no env setup. This file is
// dc-api/internal/agentrbac/render_test.go, so the repo root is four dirs up.
func repoRoot(t *testing.T) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	// .../dc-api/internal/agentrbac/render_test.go → repo root
	return filepath.Join(filepath.Dir(thisFile), "..", "..", "..")
}

// TestRBACDrift fails if the committed flux rbac.yaml is out of sync with the
// registry — i.e. someone edited capabilities.go without regenerating. The
// message tells the dev exactly how to fix it.
func TestRBACDrift(t *testing.T) {
	got := RenderAgentRBAC(clusteraccess.AgentCapabilities)

	path := filepath.Join(repoRoot(t), "flux", "platform", "dc-agent", "base", "rbac.yaml")
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read committed rbac.yaml at %s: %v", path, err)
	}

	if !bytes.Equal(got, want) {
		t.Fatalf("flux/platform/dc-agent/base/rbac.yaml is STALE — run `make gen-agent-rbac` and commit.\n\n--- want (committed) ---\n%s\n--- got (regenerated) ---\n%s", want, got)
	}
}

// TestRBACByteStable proves the generator is deterministic: rendering twice from
// the same registry yields identical bytes (guards against nondeterministic map
// iteration leaking into the output).
func TestRBACByteStable(t *testing.T) {
	a := RenderAgentRBAC(clusteraccess.AgentCapabilities)
	b := RenderAgentRBAC(clusteraccess.AgentCapabilities)
	if !bytes.Equal(a, b) {
		t.Fatalf("RenderAgentRBAC is not byte-stable:\n--- run 1 ---\n%s\n--- run 2 ---\n%s", a, b)
	}
}

// TestSubsumedCapabilityRuleNotEmitted pins the render-time subsumption rule:
// a capability whose k8s verbs the fixed base inventory rule already fully
// grants (pods get/list ⊂ nodes/pods get/list/watch) emits NO separate
// ClusterRole rule — RBAC is additive, so the duplicate would grant nothing and
// only reads as a failed narrowing. A capability with any verb OUTSIDE the base
// coverage still emits its full rule.
func TestSubsumedCapabilityRuleNotEmitted(t *testing.T) {
	podsReadOnly := clusteraccess.AgentCapability{
		GVR:        schema.GroupVersionResource{Group: "", Version: "v1", Resource: "pods"},
		APIVersion: "v1", Kind: "Pod", Namespaced: true,
		AgentVerbs: []clusteraccess.Verb{clusteraccess.VerbGet, clusteraccess.VerbList},
	}
	out := string(RenderAgentRBAC([]clusteraccess.AgentCapability{podsReadOnly}))
	if strings.Contains(out, `resources: ["pods"]`) {
		t.Errorf("fully-subsumed pods rule was emitted:\n%s", out)
	}
	if !strings.Contains(out, `resources: ["nodes", "pods"]`) {
		t.Errorf("base inventory rule missing:\n%s", out)
	}

	// Widen one verb beyond the base rule (create) → the rule MUST be emitted.
	podsWithCreate := podsReadOnly
	podsWithCreate.AgentVerbs = []clusteraccess.Verb{clusteraccess.VerbGet, clusteraccess.VerbList, clusteraccess.VerbCreate}
	out = string(RenderAgentRBAC([]clusteraccess.AgentCapability{podsWithCreate}))
	if !strings.Contains(out, `resources: ["pods"]`) {
		t.Errorf("partially-covered pods rule was NOT emitted:\n%s", out)
	}
	if !strings.Contains(out, `verbs: ["get", "list", "create"]`) {
		t.Errorf("partially-covered pods rule lost verbs:\n%s", out)
	}
}
