package format

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// pinnedClosureMatrix is an index, rather than another oracle format: the
// referenced fixtures remain the single source of expected selected formats,
// plans, and sorting order. This keeps closure verification Python-free and
// prevents PR-level checklists from silently losing a covered family.
type pinnedClosureMatrix struct {
	SchemaVersion          int                    `json:"schema_version"`
	Reference              pinnedClosureReference `json:"reference"`
	Families               []pinnedClosureFamily  `json:"families"`
	OfficialParserExamples []string               `json:"official_parser_examples"`
	AllowedDeviations      []string               `json:"allowed_deviations"`
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
	selectorAssignments := make(map[string]string, len(selectorIDs))
	sorterAssignments := make(map[string]string, len(sorterIDs))
	plannerAssignments := make(map[string]string, len(plannerIDs))
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
			assignClosureCase(t, selectorAssignments, family.ID, id, "selector")
		}
		for _, id := range family.SorterCases {
			if !sorterIDs[id] {
				t.Fatalf("closure family %q references unknown sorter case %q", family.ID, id)
			}
			assignClosureCase(t, sorterAssignments, family.ID, id, "sorter")
		}
		for _, id := range family.PlannerCases {
			if !plannerIDs[id] {
				t.Fatalf("closure family %q references unknown planner case %q", family.ID, id)
			}
			assignClosureCase(t, plannerAssignments, family.ID, id, "planner")
		}
	}
	for _, required := range []string{
		"quality-atoms-and-direct-selectors",
		"operators-and-filter-cross-products",
		"normalization-media-defaults-and-multistreams",
		"parser-syntax-and-resource-contract",
		"sorter-composition-and-ordering",
	} {
		if _, ok := seenFamilies[required]; !ok {
			t.Fatalf("closure matrix is missing required family %q", required)
		}
	}
	assertClosureCoverage(t, selectorIDs, selectorAssignments, "selector")
	assertClosureCoverage(t, sorterIDs, sorterAssignments, "sorter")
	assertClosureCoverage(t, plannerIDs, plannerAssignments, "planner")
	if !reflect.DeepEqual(matrix.OfficialParserExamples, pinnedOfficialSelectorExamples) {
		t.Fatalf("official parser examples = %#v, want %#v", matrix.OfficialParserExamples, pinnedOfficialSelectorExamples)
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
	if matrix.SchemaVersion != 1 || matrix.Reference.Commit != selectorConformanceCommit || matrix.Reference.Repository == "" || matrix.Reference.PythonVersion != "CPython 3.12.13" || matrix.Reference.Derivation == "" || len(matrix.Families) == 0 || len(matrix.OfficialParserExamples) == 0 {
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
			if ids[c.ID] {
				t.Fatalf("%s repeats case id %q", name, c.ID)
			}
			ids[c.ID] = true
		}
	}
	return ids
}

func assignClosureCase(t *testing.T, assignments map[string]string, family, id, kind string) {
	t.Helper()
	if previous, duplicate := assignments[id]; duplicate {
		t.Fatalf("%s case %q is assigned to both %q and %q", kind, id, previous, family)
	}
	assignments[id] = family
}

func assertClosureCoverage[T any](t *testing.T, authoritative map[string]T, assignments map[string]string, kind string) {
	t.Helper()
	if len(authoritative) != len(assignments) {
		t.Fatalf("%s closure coverage = %d/%d cases", kind, len(assignments), len(authoritative))
	}
	for id := range authoritative {
		if _, covered := assignments[id]; !covered {
			t.Fatalf("%s case %q is not assigned to a closure family", kind, id)
		}
	}
}
