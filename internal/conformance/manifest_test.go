package conformance

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestRepositoryManifest(t *testing.T) {
	path := filepath.Join("..", "..", "conformance", "parity_manifest.yaml")
	manifest, err := LoadFile(path)
	if err != nil {
		t.Fatalf("LoadFile() error = %v", err)
	}
	if len(manifest.Capabilities) < 5 {
		t.Fatalf("capability count = %d, want at least 5", len(manifest.Capabilities))
	}
}

func TestLoadRejectsUnknownField(t *testing.T) {
	input := `
version: 1
unexpected: true
capabilities: []
`
	if _, err := Load(strings.NewReader(input)); err == nil || !strings.Contains(err.Error(), "field unexpected") {
		t.Fatalf("Load() error = %v, want unknown-field error", err)
	}
}

func TestValidateRequiresEvidenceForCompatibleClaim(t *testing.T) {
	manifest := Manifest{Version: 1, Capabilities: []Capability{{
		ID:                  "test.capability",
		Name:                "Test capability",
		CompatibilityTarget: "A test target",
		Status:              StatusCompatible,
		Owner:               "core",
	}}}
	if err := manifest.Validate(); err == nil || !strings.Contains(err.Error(), "no evidence") {
		t.Fatalf("Validate() error = %v, want evidence error", err)
	}
}

func TestValidateRejectsUnknownDependency(t *testing.T) {
	manifest := Manifest{Version: 1, Capabilities: []Capability{{
		ID:                  "test.capability",
		Name:                "Test capability",
		CompatibilityTarget: "A test target",
		Status:              StatusNotStarted,
		Owner:               "core",
		DependsOn:           []string{"missing"},
	}}}
	if err := manifest.Validate(); err == nil || !strings.Contains(err.Error(), "unknown capability") {
		t.Fatalf("Validate() error = %v, want dependency error", err)
	}
}

func TestValidateMatchesSchemaConstraints(t *testing.T) {
	tests := []struct {
		name       string
		capability Capability
		want       string
	}{
		{
			name: "invalid ID",
			capability: Capability{
				ID: "Invalid ID", Name: "Test", CompatibilityTarget: "Test", Status: StatusNotStarted, Owner: "core",
			},
			want: "must match",
		},
		{
			name: "negative phase",
			capability: Capability{
				ID: "test.id", Name: "Test", Phase: -1, CompatibilityTarget: "Test", Status: StatusNotStarted, Owner: "core",
			},
			want: "must not be negative",
		},
		{
			name: "duplicate evidence",
			capability: Capability{
				ID: "test.id", Name: "Test", CompatibilityTarget: "Test", Status: StatusPartial, Owner: "core", Evidence: []string{"TestOne", "TestOne"},
			},
			want: "repeats evidence",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			manifest := Manifest{Version: 1, Capabilities: []Capability{test.capability}}
			if err := manifest.Validate(); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Validate() error = %v, want %q", err, test.want)
			}
		})
	}
}
