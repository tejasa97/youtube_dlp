package differential

import (
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

// Mode selects comparison behavior for a path.
type Mode string

const (
	ModeExact       Mode = "exact"
	ModeOrdered     Mode = "ordered"
	ModeSet         Mode = "set"
	ModeIgnore      Mode = "ignore"
	ModeTolerance   Mode = "tolerance"
	ModeRedactedURL Mode = "redacted_url"
)

var policyPathPattern = regexp.MustCompile(`^\$(?:\.[A-Za-z0-9_-]+|\[(?:[0-9]+|\*)\])*$`)

// Rule is an attributable exception or explicit comparison choice.
type Rule struct {
	Path      string  `yaml:"path" json:"path"`
	Mode      Mode    `yaml:"mode" json:"mode"`
	Tolerance float64 `yaml:"tolerance,omitempty" json:"tolerance,omitempty"`
	Reason    string  `yaml:"reason,omitempty" json:"reason,omitempty"`
	Owner     string  `yaml:"owner,omitempty" json:"owner,omitempty"`
}

// Policy defines comparison modes. Unmatched paths use exact ordered
// comparison.
type Policy struct {
	Version int    `yaml:"version" json:"version"`
	Rules   []Rule `yaml:"rules,omitempty" json:"rules,omitempty"`
}

// DefaultPolicy returns strict ordered comparison with no exceptions.
func DefaultPolicy() Policy { return Policy{Version: SchemaVersion} }

// LoadPolicy decodes one strict YAML document.
func LoadPolicy(reader io.Reader) (Policy, error) {
	decoder := yaml.NewDecoder(reader)
	decoder.KnownFields(true)
	var policy Policy
	if err := decoder.Decode(&policy); err != nil {
		return Policy{}, fmt.Errorf("decode policy: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return Policy{}, errors.New("policy must contain exactly one YAML document")
		}
		return Policy{}, fmt.Errorf("decode trailing policy: %w", err)
	}
	if err := policy.Validate(); err != nil {
		return Policy{}, err
	}
	return policy, nil
}

// LoadPolicyFile reads a policy from disk.
func LoadPolicyFile(path string) (Policy, error) {
	file, err := os.Open(path)
	if err != nil {
		return Policy{}, fmt.Errorf("open policy: %w", err)
	}
	defer file.Close()
	policy, err := LoadPolicy(file)
	if err != nil {
		return Policy{}, fmt.Errorf("load %s: %w", path, err)
	}
	return policy, nil
}

// Validate rejects ambiguous or unattributed comparison exceptions.
func (policy Policy) Validate() error {
	if policy.Version != SchemaVersion {
		return fmt.Errorf("policy version = %d, want %d", policy.Version, SchemaVersion)
	}
	seen := make(map[string]struct{}, len(policy.Rules))
	for index, rule := range policy.Rules {
		prefix := fmt.Sprintf("rules[%d]", index)
		if !policyPathPattern.MatchString(rule.Path) {
			return fmt.Errorf("%s.path %q is invalid", prefix, rule.Path)
		}
		if _, exists := seen[rule.Path]; exists {
			return fmt.Errorf("duplicate rule path %q", rule.Path)
		}
		seen[rule.Path] = struct{}{}
		switch rule.Mode {
		case ModeExact, ModeOrdered, ModeSet, ModeIgnore, ModeTolerance, ModeRedactedURL:
		default:
			return fmt.Errorf("%s.mode %q is invalid", prefix, rule.Mode)
		}
		if rule.Mode == ModeTolerance && rule.Tolerance < 0 {
			return fmt.Errorf("%s.tolerance must not be negative", prefix)
		}
		if rule.Mode != ModeExact && rule.Mode != ModeOrdered {
			if strings.TrimSpace(rule.Reason) == "" {
				return fmt.Errorf("%s.reason is required for mode %s", prefix, rule.Mode)
			}
			if strings.TrimSpace(rule.Owner) == "" {
				return fmt.Errorf("%s.owner is required for mode %s", prefix, rule.Mode)
			}
		}
	}
	return nil
}

func (policy Policy) ruleFor(path string) Rule {
	best := Rule{Path: path, Mode: ModeExact}
	bestScore := -1
	for _, rule := range policy.Rules {
		if !pathMatches(rule.Path, path) {
			continue
		}
		score := len(strings.ReplaceAll(rule.Path, "*", ""))
		if rule.Path == path {
			score += 1 << 20
		}
		if score > bestScore {
			best, bestScore = rule, score
		}
	}
	return best
}

func pathMatches(pattern, path string) bool {
	quoted := regexp.QuoteMeta(pattern)
	quoted = strings.ReplaceAll(quoted, `\[\*\]`, `\[[0-9]+\]`)
	matched, _ := regexp.MatchString("^"+quoted+"$", path)
	return matched
}
