package main_test

import (
	"path/filepath"
	"testing"

	"github.com/wso2/rca/pkg/plugin"
)

func TestLoadShippedRuleset(t *testing.T) {
	rules, err := plugin.LoadRuleset(filepath.Join("..", "..", "rules", "rca-rules.yaml"))
	if err != nil {
		t.Fatalf("load shipped ruleset: %v", err)
	}
	if len(rules.CorrelationOrder) == 0 {
		t.Fatal("expected correlation namespaces in shipped ruleset")
	}
	if len(rules.NodePatterns) == 0 {
		t.Fatal("expected node patterns in shipped ruleset")
	}
}

func TestMatchLinesReturnsAllMatchingPatterns(t *testing.T) {
	rules := plugin.Ruleset{
		NamespacesRaw: []plugin.NamespaceRules{{
			Name: "test-system",
			Patterns: []plugin.Pattern{
				{ID: "broad-error", Regex: `(?i)error`},
				{ID: "connection-error", Regex: `(?i)connection.*error`},
				{ID: "unrelated", Regex: `timeout`},
			},
		}},
	}

	matches := rules.MatchLines("test-system", "connection error while contacting the API")
	if len(matches) != 2 {
		t.Fatalf("expected 2 matching patterns, got %d", len(matches))
	}
	if matches[0].ID != "broad-error" || matches[1].ID != "connection-error" {
		t.Fatalf("unexpected matching patterns: %q, %q", matches[0].ID, matches[1].ID)
	}
}

func TestMatchNodeLinesReturnsNodePatterns(t *testing.T) {
	rules := plugin.Ruleset{
		NodePatterns: []plugin.Pattern{
			{ID: "node-disk-pressure", Regex: `(?i)(No space left on device|DiskPressure)`},
			{ID: "node-memory-pressure", Regex: `(?i)Out of memory`},
		},
	}

	matches := rules.MatchNodeLines("node worker-1: No space left on device")
	if len(matches) != 1 {
		t.Fatalf("expected 1 matching node pattern, got %d", len(matches))
	}
	if matches[0].ID != "node-disk-pressure" {
		t.Fatalf("unexpected node matching pattern: %q", matches[0].ID)
	}
}
