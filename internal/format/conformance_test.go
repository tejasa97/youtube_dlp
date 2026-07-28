package format

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/ytdlp-go/ytdlp/internal/value"
)

const selectorConformanceCommit = "aefce1eea4d0b6bab1ec2bd3beff09bff91a39c8"

type selectorCorpus struct {
	SchemaVersion  int                             `json:"schema_version"`
	Reference      selectorCorpusReference         `json:"reference"`
	Limits         selectorCorpusLimits            `json:"limits"`
	FormatSets     map[string][]json.RawMessage    `json:"format_sets"`
	NormalizedSets map[string][]expectedNormalized `json:"normalized_sets"`
	Cases          []selectorCorpusCase            `json:"cases"`
}

type selectorCorpusReference struct {
	Repository    string   `json:"repository"`
	Commit        string   `json:"commit"`
	PythonVersion string   `json:"python_version"`
	Sources       []string `json:"sources"`
}

type selectorCorpusLimits struct {
	Formats    int `json:"formats"`
	IDBytes    int `json:"id_bytes"`
	TotalBytes int `json:"total_id_bytes"`
}

type selectorCorpusCase struct {
	ID            string                 `json:"id"`
	Features      []string               `json:"features"`
	FormatSet     string                 `json:"format_set,omitempty"`
	Input         *selectorCorpusInput   `json:"input,omitempty"`
	Selector      string                 `json:"selector"`
	Options       selectorCorpusOptions  `json:"options"`
	NormalizedSet string                 `json:"expected_normalized_set,omitempty"`
	Expected      selectorCorpusExpected `json:"expected"`
	Parity        selectorCorpusParity   `json:"parity"`
}

type selectorCorpusInput struct {
	Kind    string          `json:"kind"`
	Value   json.RawMessage `json:"value,omitempty"`
	Count   int             `json:"count,omitempty"`
	IDBytes int             `json:"id_bytes,omitempty"`
}

type selectorCorpusOptions struct {
	AllowDRM          bool     `json:"allow_drm,omitempty"`
	PreferExtensions  []string `json:"prefer_extensions,omitempty"`
	PreferFreeFormats bool     `json:"prefer_free_formats,omitempty"`
	Sort              []string `json:"sort,omitempty"`
}

type selectorCorpusExpected struct {
	Plans []expectedPlan `json:"plans,omitempty"`
	Error string         `json:"error,omitempty"`
}

type selectorCorpusParity struct {
	Status    string          `json:"status"`
	Reason    string          `json:"reason,omitempty"`
	Reference json.RawMessage `json:"reference_expected,omitempty"`
}

type expectedNormalized struct {
	Source int             `json:"source"`
	Format json.RawMessage `json:"format"`
}

type expectedPlan struct {
	Tracks []expectedTrack `json:"tracks"`
}

type expectedTrack struct {
	Source  int               `json:"source"`
	ID      string            `json:"id"`
	URL     string            `json:"url,omitempty"`
	Ext     string            `json:"ext,omitempty"`
	VCodec  string            `json:"vcodec,omitempty"`
	ACodec  string            `json:"acodec,omitempty"`
	Headers map[string]string `json:"headers,omitempty"`
}

func loadSelectorCorpus(t *testing.T) selectorCorpus {
	t.Helper()
	body, err := os.ReadFile(filepath.Join("testdata", "selector_conformance.json"))
	if err != nil {
		t.Fatal(err)
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	var corpus selectorCorpus
	if err := decoder.Decode(&corpus); err != nil {
		t.Fatalf("decode selector corpus: %v", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		t.Fatalf("selector corpus has trailing JSON: %v", err)
	}
	validateSelectorCorpus(t, corpus)
	return corpus
}

func validateSelectorCorpus(t *testing.T, corpus selectorCorpus) {
	t.Helper()
	if corpus.SchemaVersion != 1 || corpus.Reference.Commit != selectorConformanceCommit || corpus.Reference.Repository == "" || corpus.Reference.PythonVersion != "CPython 3.12.13" || len(corpus.Reference.Sources) == 0 {
		t.Fatalf("invalid corpus provenance: %+v", corpus.Reference)
	}
	if corpus.Limits != (selectorCorpusLimits{Formats: maxNormalizedEntries, IDBytes: maxNormalizedIDBytes, TotalBytes: maxNormalizedTotal}) {
		t.Fatalf("fixture limits = %+v", corpus.Limits)
	}
	seen := make(map[string]struct{}, len(corpus.Cases))
	for _, c := range corpus.Cases {
		if c.ID == "" || c.Selector == "" || len(c.Features) == 0 {
			t.Fatalf("incomplete case: %+v", c)
		}
		if _, exists := seen[c.ID]; exists {
			t.Fatalf("duplicate case id %q", c.ID)
		}
		seen[c.ID] = struct{}{}
		if c.FormatSet == "" && c.Input == nil {
			t.Fatalf("case %q has no input", c.ID)
		}
		if c.FormatSet != "" {
			if _, ok := corpus.FormatSets[c.FormatSet]; !ok {
				t.Fatalf("case %q references unknown format set %q", c.ID, c.FormatSet)
			}
		}
		if c.NormalizedSet != "" {
			if _, ok := corpus.NormalizedSets[c.NormalizedSet]; !ok {
				t.Fatalf("case %q references unknown normalized set %q", c.ID, c.NormalizedSet)
			}
		}
		if (len(c.Expected.Plans) == 0) == (c.Expected.Error == "") {
			t.Fatalf("case %q must specify exactly plans or error", c.ID)
		}
		switch c.Parity.Status {
		case "passing":
			if c.Parity.Reason != "" || len(c.Parity.Reference) != 0 {
				t.Fatalf("passing case %q carries gap metadata", c.ID)
			}
		case "known_gap", "deliberate_safety_gap":
			if c.Parity.Reason == "" || len(c.Parity.Reference) == 0 {
				t.Fatalf("gap case %q lacks reason/reference expectation", c.ID)
			}
		default:
			t.Fatalf("case %q has invalid parity status %q", c.ID, c.Parity.Status)
		}
	}
	for _, required := range []string{
		"drm.all-excludes-disallowed",
		"drm.direct-id-excludes-disallowed",
		"url-filter.all",
		"url-filter.direct-missing",
		"url-filter.direct-empty",
		"coercion.numeric-id",
	} {
		if _, ok := seen[required]; !ok {
			t.Fatalf("required normalization case %q is missing", required)
		}
	}
}

func TestSelectorConformanceCorpus(t *testing.T) {
	corpus := loadSelectorCorpus(t)
	for _, fixtureCase := range corpus.Cases {
		fixtureCase := fixtureCase
		t.Run(fixtureCase.ID, func(t *testing.T) {
			info := corpusCaseInfo(t, corpus, fixtureCase)
			options := corpusCaseOptions(t, fixtureCase.Options)
			if fixtureCase.NormalizedSet != "" {
				prepared, err := prepareFormats(info, options)
				if err != nil {
					t.Fatalf("prepare formats: %v", err)
				}
				assertExpectedNormalized(t, prepared.formats, corpus.NormalizedSets[fixtureCase.NormalizedSet])
			}
			selector, err := ParseSelector(fixtureCase.Selector)
			if err == nil {
				var plans []OutputPlan
				plans, err = PlanSelectWithOptions(info, selector, options)
				if err == nil {
					assertExpectedPlans(t, plans, fixtureCase.Expected.Plans)
				}
			}
			if fixtureCase.Expected.Error != "" {
				assertCorpusError(t, err, fixtureCase.Expected.Error)
			} else if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

func corpusCaseInfo(t *testing.T, corpus selectorCorpus, c selectorCorpusCase) value.Info {
	t.Helper()
	if c.FormatSet != "" {
		items := make([]value.Value, len(corpus.FormatSets[c.FormatSet]))
		for index, raw := range corpus.FormatSets[c.FormatSet] {
			items[index] = corpusJSONValue(t, raw)
		}
		return value.NewInfo(value.NewObject(value.Field{Key: "formats", Value: value.List(items...)}))
	}
	input := c.Input
	switch input.Kind {
	case "non_list":
		return value.NewInfo(value.NewObject(value.Field{Key: "formats", Value: corpusJSONValue(t, input.Value)}))
	case "raw_list":
		var rawItems []json.RawMessage
		if err := json.Unmarshal(input.Value, &rawItems); err != nil {
			t.Fatal(err)
		}
		items := make([]value.Value, len(rawItems))
		for index, raw := range rawItems {
			items[index] = corpusJSONValue(t, raw)
		}
		return value.NewInfo(value.NewObject(value.Field{Key: "formats", Value: value.List(items...)}))
	case "generated":
		items := make([]value.Value, input.Count)
		id := strings.Repeat("x", input.IDBytes)
		for index := range items {
			items[index] = normalizationFormat(
				value.Field{Key: "format_id", Value: value.String(id)},
				value.Field{Key: "url", Value: value.String(fmt.Sprintf("https://example.invalid/%d", index))},
				value.Field{Key: "ext", Value: value.String("webm")},
			)
		}
		return normalizationInfo(items...)
	default:
		t.Fatalf("case %q has unknown input kind %q", c.ID, input.Kind)
		return value.Info{}
	}
}

func corpusJSONValue(t *testing.T, raw json.RawMessage) value.Value {
	t.Helper()
	var decoded any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	return corpusAnyValue(t, decoded)
}

func corpusAnyValue(t *testing.T, decoded any) value.Value {
	t.Helper()
	switch item := decoded.(type) {
	case nil:
		return value.Null()
	case bool:
		return value.Bool(item)
	case string:
		return value.String(item)
	case float64:
		if item == float64(int64(item)) {
			return value.Int(int64(item))
		}
		return value.Float(item)
	case []any:
		values := make([]value.Value, len(item))
		for index := range item {
			values[index] = corpusAnyValue(t, item[index])
		}
		return value.List(values...)
	case map[string]any:
		object := value.NewObject()
		for key, child := range item {
			object.Set(key, corpusAnyValue(t, child))
		}
		return value.ObjectValue(object)
	default:
		t.Fatalf("unsupported fixture JSON value %T", decoded)
		return value.Missing()
	}
}

func corpusCaseOptions(t *testing.T, raw selectorCorpusOptions) Options {
	t.Helper()
	options := Options{
		AllowDRM:          raw.AllowDRM,
		PreferExtensions:  append([]string(nil), raw.PreferExtensions...),
		PreferFreeFormats: raw.PreferFreeFormats,
	}
	if len(raw.Sort) != 0 {
		fields, err := ParseSortFields(raw.Sort)
		if err != nil {
			t.Fatal(err)
		}
		options.Sort = fields
	}
	return options
}

func assertExpectedNormalized(t *testing.T, got []normalizedFormat, want []expectedNormalized) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("normalized count = %d, want %d", len(got), len(want))
	}
	for index := range want {
		if got[index].Source != want[index].Source {
			t.Fatalf("normalized[%d].source = %d, want %d", index, got[index].Source, want[index].Source)
		}
		actual, err := json.Marshal(got[index].Object)
		if err != nil {
			t.Fatal(err)
		}
		if !jsonEqual(actual, want[index].Format) {
			t.Fatalf("normalized[%d] = %s, want %s", index, actual, want[index].Format)
		}
	}
}

func assertExpectedPlans(t *testing.T, got []OutputPlan, want []expectedPlan) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("plan count = %d, want %d", len(got), len(want))
	}
	for planIndex := range want {
		if len(got[planIndex].Tracks) != len(want[planIndex].Tracks) {
			t.Fatalf("plan[%d] track count = %d, want %d", planIndex, len(got[planIndex].Tracks), len(want[planIndex].Tracks))
		}
		for trackIndex, expected := range want[planIndex].Tracks {
			track := got[planIndex].Tracks[trackIndex]
			source, known := track.SourceFormatIndex()
			if !known || source != expected.Source || track.ID != expected.ID {
				t.Fatalf("plan[%d].track[%d] = source:%d known:%v id:%q, want source:%d id:%q", planIndex, trackIndex, source, known, track.ID, expected.Source, expected.ID)
			}
			if expected.URL != "" && track.URL != expected.URL || expected.Ext != "" && track.Ext != expected.Ext || expected.VCodec != "" && track.VCodec != expected.VCodec || expected.ACodec != "" && track.ACodec != expected.ACodec {
				t.Fatalf("plan[%d].track[%d] metadata = %#v", planIndex, trackIndex, track)
			}
			for key, expectedValue := range expected.Headers {
				if actual := track.Headers.Get(key); actual != expectedValue {
					t.Fatalf("plan[%d].track[%d] header %s = %q, want %q", planIndex, trackIndex, key, actual, expectedValue)
				}
			}
		}
	}
}

func assertCorpusError(t *testing.T, err error, name string) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected %s, got nil", name)
	}
	var target error
	switch name {
	case "ErrInvalidSelector":
		target = ErrInvalidSelector
	case "ErrSelectorLimit":
		target = ErrSelectorLimit
	case "ErrNoMatch":
		target = ErrNoMatch
	case "ErrNoFormats":
		target = ErrNoFormats
	case "ErrInvalidFormats":
		target = ErrInvalidFormats
	case "ErrFormatLimit":
		target = ErrFormatLimit
	default:
		t.Fatalf("unknown error name %q", name)
	}
	if !errors.Is(err, target) {
		t.Fatalf("error = %v, want %s", err, name)
	}
}

func jsonEqual(left, right []byte) bool {
	var l, r any
	return json.Unmarshal(left, &l) == nil && json.Unmarshal(right, &r) == nil && reflect.DeepEqual(l, r)
}
