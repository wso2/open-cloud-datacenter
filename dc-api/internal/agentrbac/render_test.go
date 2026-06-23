package agentrbac

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"testing"

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
