package format

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"testing"

	"github.com/ytdlp-go/ytdlp/internal/value"
)

func TestIntOrNoneOracleFixture(t *testing.T) {
	body, err := os.ReadFile(filepath.Join("testdata", "int_or_none_oracle.json"))
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		SchemaVersion int `json:"schema_version"`
		Reference     struct {
			Commit        string `json:"commit"`
			PythonVersion string `json:"python_version"`
		} `json:"reference"`
		Cases []struct {
			ID       string          `json:"id"`
			Input    json.RawMessage `json:"input"`
			Expected *int64          `json:"expected"`
		} `json:"cases"`
	}
	if err := json.Unmarshal(body, &fixture); err != nil {
		t.Fatal(err)
	}
	if fixture.SchemaVersion != 1 || fixture.Reference.Commit != selectorConformanceCommit {
		t.Fatalf("oracle provenance = %+v", fixture.Reference)
	}
	if len(fixture.Cases) == 0 {
		t.Fatal("empty int_or_none oracle")
	}
	for _, testCase := range fixture.Cases {
		raw := oracleInputValue(t, testCase.Input)
		got, ok := intOrNone(raw)
		if testCase.Expected == nil {
			if ok {
				t.Fatalf("%s: got %d, want absent", testCase.ID, got)
			}
			continue
		}
		if !ok || got != *testCase.Expected {
			t.Fatalf("%s: got %d ok=%v, want %d", testCase.ID, got, ok, *testCase.Expected)
		}
	}
}

func TestIntOrNoneUnitBounds(t *testing.T) {
	tests := []struct {
		name string
		raw  value.Value
		want int64
		ok   bool
	}{
		{"underscore", value.String("1_000"), 1000, true},
		{"unicode", value.String("١٢٣"), 123, true},
		{"fullwidth", value.String("１２３"), 123, true},
		{"spaces", value.String("  42  "), 42, true},
		{"plus", value.String("+5"), 5, true},
		{"minus", value.String("-5"), -5, true},
		{"double_underscore", value.String("1__0"), 0, false},
		{"float_string", value.String("42.5"), 0, false},
		{"float_trunc", value.Float(3.9), 3, true},
		{"nan", value.Float(math.NaN()), 0, false},
		{"int64_max", value.String("9223372036854775807"), math.MaxInt64, true},
		{"int64_overflow", value.String("9223372036854775808"), 0, false},
		{"int64_min", value.String("-9223372036854775808"), math.MinInt64, true},
		{"int64_underflow", value.String("-9223372036854775809"), 0, false},
		{"object", value.ObjectValue(value.NewObject()), 0, false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, ok := intOrNone(test.raw)
			if ok != test.ok || (ok && got != test.want) {
				t.Fatalf("got %d ok=%v, want %d ok=%v", got, ok, test.want, test.ok)
			}
		})
	}
}

func oracleInputValue(t *testing.T, raw json.RawMessage) value.Value {
	t.Helper()
	var payload struct {
		Type  string   `json:"type"`
		Value *int64   `json:"value"`
		Text  *string  `json:"text"`
		Float *float64 `json:"float"`
		Bool  *bool    `json:"bool"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatal(err)
	}
	switch payload.Type {
	case "null":
		return value.Null()
	case "bool":
		if payload.Bool == nil {
			t.Fatal("bool payload missing value")
		}
		return value.Bool(*payload.Bool)
	case "int":
		if payload.Value == nil {
			t.Fatal("int payload missing value")
		}
		return value.Int(*payload.Value)
	case "float":
		if payload.Float == nil {
			t.Fatal("float payload missing value")
		}
		return value.Float(*payload.Float)
	case "string":
		if payload.Text == nil {
			t.Fatal("string payload missing text")
		}
		return value.String(*payload.Text)
	default:
		t.Fatalf("unsupported oracle input type %q", payload.Type)
		return value.Null()
	}
}
