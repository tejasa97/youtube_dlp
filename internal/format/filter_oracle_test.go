package format

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/tejasa97/ytdlp-go/internal/value"
)

type filterOracleDoc struct {
	SchemaVersion int `json:"schema_version"`
	Reference     struct {
		Commit        string `json:"commit"`
		PythonVersion string `json:"python_version"`
	} `json:"reference"`
	Objects []struct {
		ID     string         `json:"id"`
		Fields map[string]any `json:"fields"`
	} `json:"objects"`
	Cases []struct {
		ID           string `json:"id"`
		Spec         string `json:"spec"`
		CompileError string `json:"compile_error"`
		Results      []struct {
			ObjectID string `json:"object_id"`
			Matched  *bool  `json:"matched"`
			Error    string `json:"error"`
		} `json:"results"`
	} `json:"cases"`
}

type regexOracleDoc struct {
	SchemaVersion int `json:"schema_version"`
	Reference     struct {
		Commit        string `json:"commit"`
		PythonVersion string `json:"python_version"`
	} `json:"reference"`
	Cases []struct {
		ID           string `json:"id"`
		Pattern      string `json:"pattern"`
		Input        string `json:"input"`
		Matched      *bool  `json:"matched"`
		CompileError string `json:"compile_error"`
	} `json:"cases"`
}

func TestFilterOracleFixture(t *testing.T) {
	doc := loadFilterOracle(t)
	if doc.SchemaVersion != 1 || doc.Reference.Commit != selectorConformanceCommit {
		t.Fatalf("unexpected provenance %#v", doc.Reference)
	}
	if doc.Reference.PythonVersion != "CPython 3.12.13" {
		t.Fatalf("python_version = %q", doc.Reference.PythonVersion)
	}
	objects := map[string]*value.Object{}
	for _, item := range doc.Objects {
		objects[item.ID] = oracleObject(t, item.Fields)
	}
	for _, test := range doc.Cases {
		predicate, err := compileFilterSpec(test.Spec, 0, len(test.Spec))
		if test.CompileError != "" {
			if err == nil {
				t.Fatalf("%s: expected compile error %s", test.ID, test.CompileError)
			}
			continue
		}
		if err != nil {
			t.Fatalf("%s: compile = %v", test.ID, err)
		}
		for _, result := range test.Results {
			object := objects[result.ObjectID]
			matched, matchErr := predicate.match(object, nil)
			if result.Error != "" {
				if matchErr == nil {
					t.Fatalf("%s/%s: expected error %s", test.ID, result.ObjectID, result.Error)
				}
				continue
			}
			if matchErr != nil {
				t.Fatalf("%s/%s: match = %v", test.ID, result.ObjectID, matchErr)
			}
			if result.Matched == nil || matched != *result.Matched {
				t.Fatalf("%s/%s: matched=%v want %v", test.ID, result.ObjectID, matched, result.Matched)
			}
		}
	}
}

func TestPythonRegexOracleFixture(t *testing.T) {
	doc := loadRegexOracle(t)
	if doc.SchemaVersion != 1 || doc.Reference.Commit != selectorConformanceCommit {
		t.Fatalf("unexpected provenance %#v", doc.Reference)
	}
	for _, test := range doc.Cases {
		expression, err := compilePythonRegex(test.Pattern, 0, len(test.Pattern))
		if test.CompileError != "" {
			if err == nil {
				t.Fatalf("%s: expected compile error", test.ID)
			}
			continue
		}
		if err != nil {
			t.Fatalf("%s: compile = %v", test.ID, err)
		}
		matched, matchErr := expression.search(test.Input, nil)
		if matchErr != nil {
			t.Fatalf("%s: search = %v", test.ID, matchErr)
		}
		if test.Matched == nil || matched != *test.Matched {
			t.Fatalf("%s: matched=%v want %v", test.ID, matched, test.Matched)
		}
	}
}

func TestFilterAdversarialAndFuzzSeeds(t *testing.T) {
	seeds := []string{
		"", "=", "a=", `a=""`, "a=~", "height=1..2", "height=1e3",
		`format_id~="(?<=a+)b"`, `format_id~="(?<x>a)"`, `format_id~="\p{L}"`,
		"field.with-dot=value", "1field=value", "é=1",
		`format_id="a]b"`, `format_id="a\\"b"`,
	}
	for _, seed := range seeds {
		_, _ = compileFilterSpec(seed, 0, len(seed))
		_, err := ParseSelector("best[" + seed + "]")
		if err != nil && !errors.Is(err, ErrInvalidSelector) && !errors.Is(err, ErrSelectorLimit) {
			t.Fatalf("unexpected error for %q: %v", seed, err)
		}
	}
}

func FuzzCompileFilterSpec(f *testing.F) {
	for _, seed := range []string{"height>1", "filesize<=?3000", `ext~="webm"`, "format_id!^=a"} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, raw string) {
		if len(raw) > 256 {
			return
		}
		_, _ = compileFilterSpec(raw, 0, len(raw))
	})
}

func loadFilterOracle(t *testing.T) filterOracleDoc {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "filter_oracle.json"))
	if err != nil {
		t.Fatal(err)
	}
	var doc filterOracleDoc
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err := decoder.Decode(&doc); err != nil {
		t.Fatal(err)
	}
	return doc
}

func loadRegexOracle(t *testing.T) regexOracleDoc {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "python_regex_oracle.json"))
	if err != nil {
		t.Fatal(err)
	}
	var doc regexOracleDoc
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}
	return doc
}

func oracleObject(t *testing.T, fields map[string]any) *value.Object {
	t.Helper()
	object := value.NewObject()
	for key, raw := range fields {
		switch typed := raw.(type) {
		case nil:
			object.Set(key, value.Null())
		case string:
			object.Set(key, value.String(typed))
		case float64:
			if typed == float64(int64(typed)) {
				object.Set(key, value.Int(int64(typed)))
			} else {
				object.Set(key, value.Float(typed))
			}
		case json.Number:
			if integer, err := typed.Int64(); err == nil {
				object.Set(key, value.Int(integer))
			} else if floating, err := typed.Float64(); err == nil {
				object.Set(key, value.Float(floating))
			} else {
				t.Fatalf("invalid oracle number %q for field %q", typed, key)
			}
		case bool:
			object.Set(key, value.Bool(typed))
		default:
			t.Fatalf("unsupported oracle field %q: %T", key, raw)
		}
	}
	return object
}
