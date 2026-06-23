// Package agentrbac renders the dc-agent Kubernetes RBAC manifest from the
// capability registry (clusteraccess.AgentCapabilities). It is the single
// rendering function shared by the cmd/gen-agent-rbac generator and the drift
// test, so "what the generator writes" and "what the test compares against" can
// never diverge.
package agentrbac

import (
	"bytes"
	"sort"
	"strings"

	"github.com/wso2/dc-api/internal/providers/clusteraccess"
)

// header is the fixed DO-NOT-EDIT banner. Editing the YAML by hand is a no-op:
// regeneration (and the drift test) overwrite it.
const header = `# GENERATED FILE — DO NOT EDIT.
#
# Produced by dc-api/cmd/gen-agent-rbac from the capability registry
# (dc-api/internal/providers/clusteraccess/capabilities.go). To change the
# agent's permissions, edit the relevant AgentCapability.AgentVerbs and run
# 'make gen-agent-rbac', then commit the regenerated file. A drift-check unit
# test fails CI if this file is out of sync with the registry.
#
# The agent runs in-cluster and reads THIS datacenter's inventory (at the
# control plane's request) via this ServiceAccount — no kubeconfig travels
# (DCAGENT_KUBECONFIG is empty, so the executor uses in-cluster config). Tenant
# VMs live in dc-<id> namespaces not predictable at deploy time, so the grant is
# cluster-scoped (ClusterRole), exactly as dc-webhook does for NADs.
`

// preamble emits the ServiceAccount; binding emits the ClusterRoleBinding. Both
// are generator constants — onboarding/removing capabilities never disturbs them.
const serviceAccount = `apiVersion: v1
kind: ServiceAccount
metadata:
  name: dc-agent
  namespace: dc-agent-system
`

const clusterRoleBinding = `apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata:
  name: dc-agent
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: ClusterRole
  name: dc-agent
subjects:
  - kind: ServiceAccount
    name: dc-agent
    namespace: dc-agent-system
`

// rule is one ClusterRole policy rule, ready to render deterministically.
type rule struct {
	apiGroup  string
	resources []string
	verbs     []string
}

// RenderAgentRBAC renders the full RBAC manifest (ServiceAccount + ClusterRole +
// ClusterRoleBinding) for the given capabilities. Output is deterministic and
// byte-stable across runs regardless of map iteration order: rules are sorted by
// apiGroup then resource, and verbs follow the canonical k8s order.
func RenderAgentRBAC(caps []clusteraccess.AgentCapability) []byte {
	rules := capabilityRules(caps)

	var b bytes.Buffer
	b.WriteString(header)
	b.WriteString("---\n")
	b.WriteString(serviceAccount)
	b.WriteString("---\n")
	b.WriteString("apiVersion: rbac.authorization.k8s.io/v1\n")
	b.WriteString("kind: ClusterRole\n")
	b.WriteString("metadata:\n")
	b.WriteString("  name: dc-agent\n")
	b.WriteString("rules:\n")
	for _, r := range rules {
		b.WriteString("  - apiGroups: [" + quoteList([]string{r.apiGroup}) + "]\n")
		b.WriteString("    resources: [" + quoteList(r.resources) + "]\n")
		b.WriteString("    verbs: [" + quoteList(r.verbs) + "]\n")
	}
	b.WriteString("---\n")
	b.WriteString(clusterRoleBinding)
	return b.Bytes()
}

// capabilityRules builds the ordered ClusterRole rules: the fixed base inventory
// rule (nodes/pods get,list,watch — serves the agent's hard-coded get_inventory
// op) followed by one rule per capability that grants RBAC (non-empty
// AgentVerbs), grouped by apiGroup and sorted deterministically.
func capabilityRules(caps []clusteraccess.AgentCapability) []rule {
	// Base inventory rule is a generator constant, NOT registry-driven: the
	// agent's get_inventory reads nodes/pods via the typed clientset.
	rules := []rule{
		{apiGroup: "", resources: []string{"nodes", "pods"}, verbs: []string{"get", "list", "watch"}},
	}

	// Merge capability verbs per (apiGroup, resource).
	type key struct{ group, resource string }
	verbsByKey := map[key]map[string]bool{}
	for _, c := range caps {
		if len(c.AgentVerbs) == 0 {
			continue
		}
		k := key{group: c.GVR.Group, resource: c.GVR.Resource}
		if verbsByKey[k] == nil {
			verbsByKey[k] = map[string]bool{}
		}
		for _, v := range c.AgentVerbs {
			kv, ok := clusteraccess.K8sVerb(v)
			if !ok {
				continue
			}
			verbsByKey[k][kv] = true
		}
	}

	keys := make([]key, 0, len(verbsByKey))
	for k := range verbsByKey {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].group != keys[j].group {
			return keys[i].group < keys[j].group
		}
		return keys[i].resource < keys[j].resource
	})

	for _, k := range keys {
		rules = append(rules, rule{
			apiGroup:  k.group,
			resources: []string{k.resource},
			verbs:     canonicalVerbs(verbsByKey[k]),
		})
	}
	return rules
}

// canonicalVerbs returns the present verbs in canonical k8s order.
func canonicalVerbs(present map[string]bool) []string {
	out := make([]string, 0, len(present))
	for _, v := range clusteraccess.K8sVerbOrder() {
		if present[v] {
			out = append(out, v)
		}
	}
	return out
}

// quoteList renders a YAML inline array body like `"a", "b"` (the caller wraps
// it in [ ]). Empty group renders as "" to match k8s core-group convention.
func quoteList(items []string) string {
	qs := make([]string, len(items))
	for i, s := range items {
		qs[i] = "\"" + s + "\""
	}
	return strings.Join(qs, ", ")
}
