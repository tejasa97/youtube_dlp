package conformance

import (
	"strings"
	"testing"
)

const validFallbackInventory = `version: 1
temporary_fallbacks:
  - id: native.bridge
    native_target: native implementation
    reason: bounded migration
    event: capability_decision
    owner: runtime
    removal_milestone: M3
permanent_alternatives:
  - id: syntax.choice
    surface: selector
    behavior: user-requested choice
    owner: compatibility
    milestone: permanent
prohibited:
  - python-backed
  - silent
`

func TestLoadFallbackInventory(t *testing.T) {
	inventory, err := LoadFallbackInventory(strings.NewReader(validFallbackInventory))
	if err != nil {
		t.Fatal(err)
	}
	if len(inventory.TemporaryFallbacks) != 1 || len(inventory.PermanentAlternatives) != 1 {
		t.Fatalf("unexpected inventory: %#v", inventory)
	}
}

func TestFallbackInventoryRejectsUnobservableTemporaryFallback(t *testing.T) {
	input := strings.Replace(validFallbackInventory, "    event: capability_decision\n", "", 1)
	_, err := LoadFallbackInventory(strings.NewReader(input))
	if err == nil || !strings.Contains(err.Error(), ".event must not be empty") {
		t.Fatalf("error = %v", err)
	}
}

func TestFallbackInventoryRequiresPythonAndSilentProhibitions(t *testing.T) {
	input := strings.Replace(validFallbackInventory, "  - python-backed\n", "", 1)
	_, err := LoadFallbackInventory(strings.NewReader(input))
	if err == nil || !strings.Contains(err.Error(), `"python-backed" is required`) {
		t.Fatalf("error = %v", err)
	}
}

func TestFallbackInventoryRejectsUnknownFieldsAndDuplicateIDs(t *testing.T) {
	unknown := strings.Replace(validFallbackInventory, "version: 1", "version: 1\nunknown: true", 1)
	if _, err := LoadFallbackInventory(strings.NewReader(unknown)); err == nil {
		t.Fatal("unknown field accepted")
	}
	duplicate := strings.Replace(validFallbackInventory, "id: syntax.choice", "id: native.bridge", 1)
	if _, err := LoadFallbackInventory(strings.NewReader(duplicate)); err == nil || !strings.Contains(err.Error(), "duplicate fallback id") {
		t.Fatalf("duplicate error = %v", err)
	}
}
