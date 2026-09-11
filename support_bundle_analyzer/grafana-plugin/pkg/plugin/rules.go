package plugin

import (
	"bytes"
	_ "embed"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"time"

	"github.com/grafana/grafana-plugin-sdk-go/backend/log"
	"gopkg.in/yaml.v3"
)

var embeddedRules []byte

type Severity string

const (
	SeverityLow    Severity = "low"
	SeverityMedium Severity = "medium"
	SeverityHigh   Severity = "high"
)

type Pattern struct {
	ID          string   `yaml:"id" json:"id"`
	Regex       string   `yaml:"regex" json:"regex"`
	Severity    Severity `yaml:"severity" json:"severity"`
	Category    string   `yaml:"category" json:"category"`
	Description string   `yaml:"description" json:"description"`

	compiled *regexp.Regexp
}

type NamespaceRules struct {
	Name     string    `yaml:"name" json:"name"`
	Patterns []Pattern `yaml:"patterns" json:"patterns"`
}

type Ruleset struct {
	CorrelationOrder []string         `yaml:"correlation_order" json:"correlationOrder"`
	NamespacesRaw    []NamespaceRules `yaml:"namespaces" json:"namespaces"`
	NodePatterns     []Pattern        `yaml:"node_patterns" json:"nodePatterns"`
}

func (r *Ruleset) warmRegexCache() error {
	for i := range r.NamespacesRaw {
		for j := range r.NamespacesRaw[i].Patterns {
			p := &r.NamespacesRaw[i].Patterns[j]
			if p.compiled == nil {
				compiled, err := regexp.Compile(p.Regex)
				if err != nil {
					return fmt.Errorf("invalid regex for namespace %q pattern %q: %w", r.NamespacesRaw[i].Name, p.ID, err)
				}
				p.compiled = compiled
			}
		}
	}
	for i := range r.NodePatterns {
		p := &r.NodePatterns[i]
		if p.compiled == nil {
			compiled, err := regexp.Compile(p.Regex)
			if err != nil {
				return fmt.Errorf("invalid regex for namespace %q pattern %q: %w", "node", p.ID, err)
			}
			p.compiled = compiled
		}
	}
	return nil
}

// Namespaces returns the raw per-namespace pattern config, compiling regexes
// lazily on first access.
func (r *Ruleset) Namespaces() []NamespaceRules {
	_ = r.warmRegexCache()
	return r.NamespacesRaw
}

func (r *Ruleset) PatternsFor(namespace string) []Pattern {
	for _, ns := range r.Namespaces() {
		if ns.Name == namespace {
			return ns.Patterns
		}
	}
	return nil
}

// MatchLines returns every pattern (in config order) that matches a log line
// for the given namespace.
func (r *Ruleset) MatchLines(namespace, line string) []Pattern {
	matches := make([]Pattern, 0)
	for _, p := range r.PatternsFor(namespace) {
		if p.compiled != nil && p.compiled.MatchString(line) {
			matches = append(matches, p)
		}
	}
	return matches
}

func (r *Ruleset) MatchNodeLines(line string) []Pattern {
	matches := make([]Pattern, 0)
	_ = r.warmRegexCache()
	for _, p := range r.NodePatterns {
		if p.compiled != nil && p.compiled.MatchString(line) {
			matches = append(matches, p)
		}
	}
	return matches
}

func LoadRuleset(path string) (*Ruleset, error) {
	info, statErr := os.Stat(path)
	if statErr != nil {
		log.DefaultLogger.Warn("rca rules file is not available; embedded rules will be used", "path", path, "error", statErr)
	} else {
		log.DefaultLogger.Debug("loading rca rules file", "path", path, "size", info.Size(), "modifiedAt", info.ModTime().UTC().Format(time.RFC3339Nano))
	}

	data, err := os.ReadFile(path)
	if err != nil {
		if len(embeddedRules) == 0 {
			return nil, err
		}
		log.DefaultLogger.Warn("failed to read rca rules file; embedded rules will be used", "path", path, "error", err)
		data = embeddedRules
	}
	if len(data) == 0 {
		return nil, fmt.Errorf("rca rules file %q is empty", path)
	}
	var rs Ruleset
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	if err := decoder.Decode(&rs); err != nil {
		return nil, err
	}
	if err := rs.warmRegexCache(); err != nil {
		return nil, err
	}
	namespaceCount := len(rs.Namespaces())
	patternCount := 0
	for _, namespace := range rs.NamespacesRaw {
		patternCount += len(namespace.Patterns)
	}
	patternCount += len(rs.NodePatterns)
	log.DefaultLogger.Debug("loaded rca rules", "path", path, "namespaces", namespaceCount, "patterns", patternCount)
	return &rs, nil
}

// rulesFilePath resolves rca-rules.yaml relative to the plugin binary so it
// works both in `mage -v run` (repo checkout) and in an installed plugin
// directory. Override with the RCA_RULES_PATH env var if you want to point
// at a mounted ConfigMap instead of the file shipped in the plugin archive.
func rulesFilePath() string {
	if p := os.Getenv("RCA_RULES_PATH"); p != "" {
		return p
	}
	exe, err := os.Executable()
	if err == nil {
		candidate := filepath.Join(filepath.Dir(exe), "rules", "rca-rules.yaml")
		if _, statErr := os.Stat(candidate); statErr == nil {
			return candidate
		}
	}
	return filepath.Join("rules", "rca-rules.yaml")
}
