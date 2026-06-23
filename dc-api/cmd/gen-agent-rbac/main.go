// Command gen-agent-rbac renders the dc-agent Kubernetes RBAC manifest from the
// capability registry and writes it to the flux base. It is the generator behind
// the //go:generate directive in
// internal/providers/clusteraccess/capabilities.go and `make gen-agent-rbac`.
//
// The output is byte-stable: re-running it without a registry change yields an
// identical file (a drift-check unit test enforces this). Edit the registry, not
// the YAML.
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/wso2/dc-api/internal/agentrbac"
	"github.com/wso2/dc-api/internal/providers/clusteraccess"
)

func main() {
	out := flag.String("out", "", "path to write the generated rbac.yaml")
	flag.Parse()

	if *out == "" {
		fmt.Fprintln(os.Stderr, "gen-agent-rbac: -out is required")
		os.Exit(2)
	}

	data := agentrbac.RenderAgentRBAC(clusteraccess.AgentCapabilities)

	// Atomic write: temp file in the same dir, then rename.
	dir := filepath.Dir(*out)
	tmp, err := os.CreateTemp(dir, ".rbac-*.yaml.tmp")
	if err != nil {
		fmt.Fprintf(os.Stderr, "gen-agent-rbac: create temp: %v\n", err)
		os.Exit(1)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		fmt.Fprintf(os.Stderr, "gen-agent-rbac: write temp: %v\n", err)
		os.Exit(1)
	}
	if err := tmp.Close(); err != nil {
		fmt.Fprintf(os.Stderr, "gen-agent-rbac: close temp: %v\n", err)
		os.Exit(1)
	}
	if err := os.Rename(tmpName, *out); err != nil {
		fmt.Fprintf(os.Stderr, "gen-agent-rbac: rename to %s: %v\n", *out, err)
		os.Exit(1)
	}

	fmt.Fprintf(os.Stderr, "gen-agent-rbac: wrote %s\n", *out)
}
