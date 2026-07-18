package conformance

import (
	"bytes"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
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

func TestRepositoryManifestEvidenceExists(t *testing.T) {
	root := filepath.Join("..", "..")
	manifest, err := LoadFile(filepath.Join(root, "conformance", "parity_manifest.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	functions := make(map[string]map[string]bool)
	for _, capability := range manifest.Capabilities {
		for _, evidence := range capability.Evidence {
			separator := strings.LastIndexByte(evidence, '.')
			if separator > 0 && (strings.HasPrefix(evidence[separator+1:], "Test") || strings.HasPrefix(evidence[separator+1:], "Fuzz")) {
				packagePath, function := evidence[:separator], evidence[separator+1:]
				known, ok := functions[packagePath]
				if !ok {
					known = testFunctions(t, filepath.Join(root, filepath.FromSlash(packagePath)))
					functions[packagePath] = known
				}
				if !known[function] {
					t.Errorf("capability %s has stale test evidence %s", capability.ID, evidence)
				}
				continue
			}
			if strings.Contains(evidence, "/") {
				fileEvidence, _, _ := strings.Cut(evidence, "#")
				if info, err := os.Stat(filepath.Join(root, filepath.FromSlash(fileEvidence))); err != nil || info.IsDir() {
					t.Errorf("capability %s has missing file evidence %s", capability.ID, evidence)
				}
			}
		}
	}
}

func testFunctions(t *testing.T, directory string) map[string]bool {
	t.Helper()
	entries, err := os.ReadDir(directory)
	if err != nil {
		return map[string]bool{}
	}
	result := make(map[string]bool)
	files := token.NewFileSet()
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), "_test.go") {
			continue
		}
		parsed, err := parser.ParseFile(files, filepath.Join(directory, entry.Name()), nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", entry.Name(), err)
		}
		for _, declaration := range parsed.Decls {
			if function, ok := declaration.(*ast.FuncDecl); ok && function.Recv == nil {
				result[function.Name.Name] = true
			}
		}
	}
	return result
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

func TestWriteSummary(t *testing.T) {
	manifest := Manifest{Version: 1, Capabilities: []Capability{{
		ID: "phase.zero", Name: "Phase zero", CompatibilityTarget: "Works | safely", Status: StatusCompatible, Owner: "core", Evidence: []string{"TestEvidence"},
	}, {
		ID: "phase.one", Name: "Phase one", Phase: 1, CompatibilityTarget: "Later", Status: StatusNotStarted, Owner: "core",
	}}}
	var output bytes.Buffer
	if err := WriteSummary(&output, &manifest); err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{"Total: 2", "## Phase 0", "`phase.zero`", "Works \\| safely", "## Phase 1"} {
		if !strings.Contains(output.String(), expected) {
			t.Fatalf("summary lacks %q:\n%s", expected, output.String())
		}
	}
}
