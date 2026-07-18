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

var fallbackIDPattern = regexp.MustCompile(`^[a-z0-9_.-]+$`)

// FallbackInventory is the machine-readable source of temporary execution
// fallbacks and permanent alternatives that can otherwise be mistaken for
// fallbacks. An empty TemporaryFallbacks list is a meaningful assertion.
type FallbackInventory struct {
	Version               int                    `yaml:"version"`
	TemporaryFallbacks    []TemporaryFallback    `yaml:"temporary_fallbacks"`
	PermanentAlternatives []PermanentAlternative `yaml:"permanent_alternatives"`
	Prohibited            []string               `yaml:"prohibited"`
}

// TemporaryFallback describes a capability bridge that must be observable and
// scheduled for removal. Python-backed and silent fallbacks are never valid.
type TemporaryFallback struct {
	ID               string `yaml:"id"`
	NativeTarget     string `yaml:"native_target"`
	Reason           string `yaml:"reason"`
	Event            string `yaml:"event"`
	Owner            string `yaml:"owner"`
	RemovalMilestone string `yaml:"removal_milestone"`
}

// PermanentAlternative records a deliberate native behavior whose name or
// semantics might otherwise cause it to be counted as a temporary fallback.
type PermanentAlternative struct {
	ID        string `yaml:"id"`
	Surface   string `yaml:"surface"`
	Behavior  string `yaml:"behavior"`
	Owner     string `yaml:"owner"`
	Milestone string `yaml:"milestone"`
}

func LoadFallbackInventoryFile(path string) (*FallbackInventory, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open fallback inventory: %w", err)
	}
	defer file.Close()
	inventory, err := LoadFallbackInventory(file)
	if err != nil {
		return nil, fmt.Errorf("load %s: %w", path, err)
	}
	return inventory, nil
}

func LoadFallbackInventory(reader io.Reader) (*FallbackInventory, error) {
	decoder := yaml.NewDecoder(reader)
	decoder.KnownFields(true)
	var inventory FallbackInventory
	if err := decoder.Decode(&inventory); err != nil {
		return nil, fmt.Errorf("decode YAML: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return nil, errors.New("fallback inventory must contain exactly one YAML document")
		}
		return nil, fmt.Errorf("decode trailing YAML: %w", err)
	}
	if err := inventory.Validate(); err != nil {
		return nil, err
	}
	return &inventory, nil
}

func (inventory *FallbackInventory) Validate() error {
	if inventory == nil {
		return errors.New("fallback inventory must not be nil")
	}
	if inventory.Version != 1 {
		return fmt.Errorf("fallback inventory version = %d, want 1", inventory.Version)
	}
	prohibited := make(map[string]struct{}, len(inventory.Prohibited))
	for index, item := range inventory.Prohibited {
		item = strings.TrimSpace(item)
		if item == "" {
			return fmt.Errorf("prohibited[%d] must not be empty", index)
		}
		if _, exists := prohibited[item]; exists {
			return fmt.Errorf("duplicate prohibited fallback policy %q", item)
		}
		prohibited[item] = struct{}{}
	}
	for _, required := range []string{"python-backed", "silent"} {
		if _, exists := prohibited[required]; !exists {
			return fmt.Errorf("prohibited fallback policy %q is required", required)
		}
	}

	seen := make(map[string]struct{}, len(inventory.TemporaryFallbacks)+len(inventory.PermanentAlternatives))
	for index, fallback := range inventory.TemporaryFallbacks {
		prefix := fmt.Sprintf("temporary_fallbacks[%d]", index)
		if err := validateFallbackID(prefix, fallback.ID, seen); err != nil {
			return err
		}
		for field, value := range map[string]string{
			"native_target":     fallback.NativeTarget,
			"reason":            fallback.Reason,
			"event":             fallback.Event,
			"owner":             fallback.Owner,
			"removal_milestone": fallback.RemovalMilestone,
		} {
			if strings.TrimSpace(value) == "" {
				return fmt.Errorf("%s.%s must not be empty", prefix, field)
			}
		}
	}
	for index, alternative := range inventory.PermanentAlternatives {
		prefix := fmt.Sprintf("permanent_alternatives[%d]", index)
		if err := validateFallbackID(prefix, alternative.ID, seen); err != nil {
			return err
		}
		for field, value := range map[string]string{
			"surface":   alternative.Surface,
			"behavior":  alternative.Behavior,
			"owner":     alternative.Owner,
			"milestone": alternative.Milestone,
		} {
			if strings.TrimSpace(value) == "" {
				return fmt.Errorf("%s.%s must not be empty", prefix, field)
			}
		}
	}
	return nil
}

func validateFallbackID(prefix, id string, seen map[string]struct{}) error {
	if !fallbackIDPattern.MatchString(id) {
		return fmt.Errorf("%s.id %q must match %s", prefix, id, fallbackIDPattern)
	}
	if _, exists := seen[id]; exists {
		return fmt.Errorf("duplicate fallback id %q", id)
	}
	seen[id] = struct{}{}
	return nil
}
