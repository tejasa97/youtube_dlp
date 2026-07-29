package format

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
)

// pinnedClosureMatrix is an index, rather than another oracle format: the
// referenced fixtures remain the single source of expected selected formats,
// plans, and sorting order. This keeps closure verification Python-free and
// prevents PR-level checklists from silently losing a covered family.
type pinnedClosureMatrix struct {
	SchemaVersion     int                    `json:"schema_version"`
	Reference         pinnedClosureReference `json:"reference"`
	Families          []pinnedClosureFamily  `json:"families"`
	AllowedDeviations []string               `json:"allowed_deviations"`
}

type pinnedClosureReference struct {
	Repository    string `json:"repository"`
	Commit        string `json:"commit"`
	PythonVersion string `json:"python_version"`
	Derivation    string `json:"derivation"`
}

type pinnedClosureFamily struct {
	ID            string   `json:"id"`
	SelectorCases []string `json:"selector_cases"`
	SorterCases   []string `json:"sorter_cases"`
	PlannerCases  []string `json:"planner_cases"`
}

func TestPinnedClosureMatrix(t *testing.T) {
	matrix := loadPinnedClosureMatrix(t)
	selector := loadSelectorCorpus(t)
	selectorIDs := make(map[string]selectorCorpusCase, len(selector.Cases))
	for _, c := range selector.Cases {
		selectorIDs[c.ID] = c
	}
	sorterIDs := loadClosureFixtureIDs(t, "format_sorter_conformance.json")
	plannerIDs := loadClosureFixtureIDs(t, "planner_conformance.json")
	seenFamilies := make(map[string]struct{}, len(matrix.Families))
	for _, family := range matrix.Families {
		if family.ID == "" {
			t.Fatal("closure matrix has an unnamed family")
		}
		if _, duplicate := seenFamilies[family.ID]; duplicate {
			t.Fatalf("closure matrix repeats family %q", family.ID)
		}
		seenFamilies[family.ID] = struct{}{}
		if len(family.SelectorCases)+len(family.SorterCases)+len(family.PlannerCases) == 0 {
			t.Fatalf("closure family %q has no executable fixtures", family.ID)
		}
		for _, id := range family.SelectorCases {
			if _, ok := selectorIDs[id]; !ok {
				t.Fatalf("closure family %q references unknown selector case %q", family.ID, id)
			}
		}
		for _, id := range family.SorterCases {
			if !sorterIDs[id] {
				t.Fatalf("closure family %q references unknown sorter case %q", family.ID, id)
			}
		}
		for _, id := range family.PlannerCases {
			if !plannerIDs[id] {
				t.Fatalf("closure family %q references unknown planner case %q", family.ID, id)
			}
		}
	}
	for _, required := range []string{
		"selector-atoms-operators-filters",
		"sorter-normalization-stability",
		"media-defaults-and-multistreams",
		"syntax-and-resource-contract",
		"readme-selector-examples",
	} {
		if _, ok := seenFamilies[required]; !ok {
			t.Fatalf("closure matrix is missing required family %q", required)
		}
	}

	allowed := make(map[string]struct{}, len(matrix.AllowedDeviations))
	for _, id := range matrix.AllowedDeviations {
		if _, duplicate := allowed[id]; duplicate {
			t.Fatalf("closure matrix repeats allowed deviation %q", id)
		}
		allowed[id] = struct{}{}
	}
	for id, c := range selectorIDs {
		if c.Parity.Status == "passing" {
			continue
		}
		if c.Parity.Status != "deliberate_safety_gap" {
			t.Fatalf("selector case %q has unresolved in-contract parity status %q", id, c.Parity.Status)
		}
		if _, ok := allowed[id]; !ok {
			t.Fatalf("selector safety deviation %q is absent from closure matrix", id)
		}
	}
	for id := range allowed {
		if _, ok := selectorIDs[id]; !ok {
			t.Fatalf("closure matrix allows unknown selector deviation %q", id)
		}
	}
}

func loadPinnedClosureMatrix(t *testing.T) pinnedClosureMatrix {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "pinned_closure_matrix.json"))
	if err != nil {
		t.Fatal(err)
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var matrix pinnedClosureMatrix
	if err := decoder.Decode(&matrix); err != nil {
		t.Fatalf("decode closure matrix: %v", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		t.Fatalf("closure matrix has trailing JSON: %v", err)
	}
	if matrix.SchemaVersion != 1 || matrix.Reference.Commit != selectorConformanceCommit || matrix.Reference.Repository == "" || matrix.Reference.PythonVersion != "CPython 3.12.13" || matrix.Reference.Derivation == "" || len(matrix.Families) == 0 {
		t.Fatalf("invalid closure matrix provenance: %+v", matrix)
	}
	return matrix
}

func loadClosureFixtureIDs(t *testing.T, name string) map[string]bool {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	var document struct {
		Cases []json.RawMessage `json:"cases"`
	}
	if err := json.Unmarshal(raw, &document); err != nil {
		t.Fatalf("decode %s: %v", name, err)
	}
	ids := make(map[string]bool, len(document.Cases))
	for _, rawCase := range document.Cases {
		var c struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(rawCase, &c); err != nil {
			t.Fatalf("decode %s case: %v", name, err)
		}
		if c.ID != "" {
			ids[c.ID] = true
		}
	}
	return ids
}
