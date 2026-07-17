// Package conformance loads and validates the capability parity manifest.
package conformance

import (
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

var capabilityIDPattern = regexp.MustCompile(`^[a-z0-9_.-]+$`)

// Status records the implementation state of a capability.
type Status string

const (
	StatusNotStarted           Status = "not_started"
	StatusPartial              Status = "partial"
	StatusCompatible           Status = "compatible"
	StatusIntentionalDeviation Status = "intentional_deviation"
	StatusBlocked              Status = "blocked"
)

// Manifest is the machine-readable source of capability claims.
type Manifest struct {
	Version      int          `yaml:"version"`
	Capabilities []Capability `yaml:"capabilities"`
}

// Capability describes one independently verifiable compatibility target.
type Capability struct {
	ID                  string   `yaml:"id"`
	Name                string   `yaml:"name"`
	Phase               int      `yaml:"phase"`
	CompatibilityTarget string   `yaml:"compatibility_target"`
	Status              Status   `yaml:"status"`
	Evidence            []string `yaml:"evidence,omitempty"`
	Owner               string   `yaml:"owner"`
	DependsOn           []string `yaml:"depends_on,omitempty"`
	KnownDeviation      string   `yaml:"known_deviation,omitempty"`
}

// LoadFile loads and validates a manifest from path.
func LoadFile(path string) (*Manifest, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open manifest: %w", err)
	}
	defer file.Close()

	manifest, err := Load(file)
	if err != nil {
		return nil, fmt.Errorf("load %s: %w", path, err)
	}
	return manifest, nil
}

// Load decodes exactly one YAML document and validates it.
func Load(reader io.Reader) (*Manifest, error) {
	decoder := yaml.NewDecoder(reader)
	decoder.KnownFields(true)

	var manifest Manifest
	if err := decoder.Decode(&manifest); err != nil {
		return nil, fmt.Errorf("decode YAML: %w", err)
	}

	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return nil, errors.New("manifest must contain exactly one YAML document")
		}
		return nil, fmt.Errorf("decode trailing YAML: %w", err)
	}

	if err := manifest.Validate(); err != nil {
		return nil, err
	}
	return &manifest, nil
}

// Validate checks structural and claim-evidence invariants.
func (manifest *Manifest) Validate() error {
	if manifest == nil {
		return errors.New("manifest must not be nil")
	}
	if manifest.Version != 1 {
		return fmt.Errorf("version = %d, want 1", manifest.Version)
	}
	if len(manifest.Capabilities) == 0 {
		return errors.New("capabilities must not be empty")
	}

	byID := make(map[string]Capability, len(manifest.Capabilities))
	for index, capability := range manifest.Capabilities {
		prefix := fmt.Sprintf("capabilities[%d]", index)
		if strings.TrimSpace(capability.ID) == "" {
			return fmt.Errorf("%s.id must not be empty", prefix)
		}
		if !capabilityIDPattern.MatchString(capability.ID) {
			return fmt.Errorf("%s.id %q must match %s", prefix, capability.ID, capabilityIDPattern)
		}
		if _, exists := byID[capability.ID]; exists {
			return fmt.Errorf("duplicate capability id %q", capability.ID)
		}
		if strings.TrimSpace(capability.Name) == "" {
			return fmt.Errorf("%s.name must not be empty", prefix)
		}
		if strings.TrimSpace(capability.CompatibilityTarget) == "" {
			return fmt.Errorf("%s.compatibility_target must not be empty", prefix)
		}
		if strings.TrimSpace(capability.Owner) == "" {
			return fmt.Errorf("%s.owner must not be empty", prefix)
		}
		if capability.Phase < 0 {
			return fmt.Errorf("%s.phase must not be negative", prefix)
		}
		if !validStatus(capability.Status) {
			return fmt.Errorf("%s.status %q is invalid", prefix, capability.Status)
		}
		if capability.Status == StatusCompatible && len(capability.Evidence) == 0 {
			return fmt.Errorf("%s is compatible but has no evidence", capability.ID)
		}
		if capability.Status == StatusIntentionalDeviation && strings.TrimSpace(capability.KnownDeviation) == "" {
			return fmt.Errorf("%s is an intentional deviation but has no explanation", capability.ID)
		}
		seenEvidence := make(map[string]struct{}, len(capability.Evidence))
		for _, evidence := range capability.Evidence {
			if strings.TrimSpace(evidence) == "" {
				return fmt.Errorf("%s contains empty evidence", capability.ID)
			}
			if _, duplicate := seenEvidence[evidence]; duplicate {
				return fmt.Errorf("%s repeats evidence %q", capability.ID, evidence)
			}
			seenEvidence[evidence] = struct{}{}
		}
		byID[capability.ID] = capability
	}

	for _, capability := range manifest.Capabilities {
		seen := make(map[string]struct{}, len(capability.DependsOn))
		for _, dependency := range capability.DependsOn {
			if dependency == capability.ID {
				return fmt.Errorf("%s depends on itself", capability.ID)
			}
			if _, exists := byID[dependency]; !exists {
				return fmt.Errorf("%s depends on unknown capability %q", capability.ID, dependency)
			}
			if _, duplicate := seen[dependency]; duplicate {
				return fmt.Errorf("%s repeats dependency %q", capability.ID, dependency)
			}
			seen[dependency] = struct{}{}
		}
	}
	return nil
}

func validStatus(status Status) bool {
	switch status {
	case StatusNotStarted, StatusPartial, StatusCompatible, StatusIntentionalDeviation, StatusBlocked:
		return true
	default:
		return false
	}
}
