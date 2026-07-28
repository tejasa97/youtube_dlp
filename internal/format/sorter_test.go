package format

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/ytdlp-go/ytdlp/internal/value"
)

// formatSorterFixture mirrors the JSON schema of format_sorter_conformance.json.
type formatSorterFixture struct {
	SchemaVersion int                       `json:"schema_version"`
	Reference     formatSorterFixtureRef    `json:"reference"`
	Cases         []formatSorterFixtureCase `json:"cases"`
}

type formatSorterFixtureRef struct {
	Repository    string   `json:"repository"`
	Commit        string   `json:"commit"`
	PythonVersion string   `json:"python_version"`
	Source        []string `json:"source"`
}

type formatSorterFixtureCase struct {
	ID       string                      `json:"id"`
	Info     formatSorterFixtureInfo     `json:"info"`
	Options  formatSorterFixtureOptions  `json:"options"`
	Expected formatSorterFixtureExpected `json:"expected"`
}

type formatSorterFixtureInfo struct {
	Formats          []json.RawMessage `json:"formats"`
	FormatSortFields []json.RawMessage `json:"_format_sort_fields,omitempty"`
}

type formatSorterFixtureOptions struct {
	Sort              []string `json:"sort"`
	SortForce         bool     `json:"sort_force"`
	PreferFreeFormats bool     `json:"prefer_free_formats"`
}

type formatSorterFixtureExpected struct {
	EffectiveFields []string       `json:"effective_fields,omitempty"`
	SourceIndexes   []int          `json:"worst_to_best_source_indexes,omitempty"`
	FormatIDs       []string       `json:"worst_to_best_format_ids,omitempty"`
	DerivedFields   map[string]any `json:"derived_fields,omitempty"`
	Error           string         `json:"error,omitempty"`
	Reason          string         `json:"reason,omitempty"`
}

const formatSorterConformanceCommit = "aefce1eea4d0b6bab1ec2bd3beff09bff91a39c8"

func loadFormatSorterCorpus(t *testing.T) formatSorterFixture {
	t.Helper()
	path := filepath.Join("testdata", "format_sorter_conformance.json")
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var fixture formatSorterFixture
	if err := json.Unmarshal(body, &fixture); err != nil {
		t.Fatal(err)
	}
	if fixture.SchemaVersion != 1 {
		t.Fatalf("schema_version = %d, want 1", fixture.SchemaVersion)
	}
	if fixture.Reference.Commit != formatSorterConformanceCommit {
		t.Fatalf("reference commit = %q, want %q", fixture.Reference.Commit, formatSorterConformanceCommit)
	}
	if fixture.Reference.PythonVersion != "CPython 3.12.13" {
		t.Fatalf("reference python_version = %q, want CPython 3.12.13", fixture.Reference.PythonVersion)
	}
	if len(fixture.Reference.Source) == 0 {
		t.Fatal("reference.source is empty")
	}
	return fixture
}

// applyFixtureOptions converts the JSON-encoded option fields into Go Options.
func applyFixtureOptions(t *testing.T, raw formatSorterFixtureOptions) Options {
	t.Helper()
	opts, err := fixtureOptions(raw)
	if err != nil {
		t.Fatalf("parse sort %v: %v", raw.Sort, err)
	}
	return opts
}

func fixtureOptions(raw formatSorterFixtureOptions) (Options, error) {
	opts := Options{
		SortForce:         raw.SortForce,
		PreferFreeFormats: raw.PreferFreeFormats,
	}
	if len(raw.Sort) > 0 {
		fields, err := ParseSortFields(raw.Sort)
		if err != nil {
			return Options{}, err
		}
		opts.Sort = fields
	}
	return opts, nil
}

// buildFixtureInfo rebuilds a value.Info from the JSON fixture info.
func buildFixtureInfo(t *testing.T, raw formatSorterFixtureInfo) value.Info {
	t.Helper()
	formats := make([]value.Value, len(raw.Formats))
	for i, formatRaw := range raw.Formats {
		var anyFormat map[string]any
		if err := json.Unmarshal(formatRaw, &anyFormat); err != nil {
			t.Fatalf("decode format %d: %v", i, err)
		}
		obj := value.NewObject()
		for key, val := range anyFormat {
			obj.Set(key, jsonAnyToValue(val))
		}
		formats[i] = value.ObjectValue(obj)
	}
	fields := value.NewObject()
	fields.Set("formats", value.List(formats...))
	if len(raw.FormatSortFields) > 0 {
		list := make([]value.Value, len(raw.FormatSortFields))
		for i, entry := range raw.FormatSortFields {
			var anyVal any
			if err := json.Unmarshal(entry, &anyVal); err != nil {
				t.Fatalf("decode format sort fields[%d]: %v", i, err)
			}
			list[i] = jsonAnyToValue(anyVal)
		}
		fields.Set("_format_sort_fields", value.List(list...))
	}
	return value.NewInfo(fields)
}

func jsonAnyToValue(raw any) value.Value {
	switch typed := raw.(type) {
	case nil:
		return value.Null()
	case bool:
		return value.Bool(typed)
	case float64:
		if typed == float64(int64(typed)) {
			return value.Int(int64(typed))
		}
		return value.Float(typed)
	case string:
		return value.String(typed)
	case map[string]any:
		obj := value.NewObject()
		for key, val := range typed {
			obj.Set(key, jsonAnyToValue(val))
		}
		return value.ObjectValue(obj)
	case []any:
		list := make([]value.Value, len(typed))
		for i, item := range typed {
			list[i] = jsonAnyToValue(item)
		}
		return value.List(list...)
	default:
		return value.Missing()
	}
}

// TestFormatSorterConformance runs every fixture case through the Go port and
// validates effective field order, canonical order, and derived fields.
func TestFormatSorterConformance(t *testing.T) {
	fixture := loadFormatSorterCorpus(t)
	for _, tc := range fixture.Cases {
		tc := tc
		t.Run(tc.ID, func(t *testing.T) {
			info := buildFixtureInfo(t, tc.Info)
			opts, optionsErr := fixtureOptions(tc.Options)
			if optionsErr != nil {
				if tc.Expected.Error != "" {
					return
				}
				t.Fatalf("parse sort %v: %v", tc.Options.Sort, optionsErr)
			}
			prepared, err := Prepare(info, opts)
			if tc.Expected.Error != "" {
				if err == nil {
					t.Fatalf("expected error %s, got nil", tc.Expected.Error)
				}
				return
			}
			if err != nil {
				t.Fatalf("prepare: %v", err)
			}
			if len(tc.Expected.SourceIndexes) > 0 {
				if len(prepared.formats) != len(tc.Expected.SourceIndexes) {
					t.Fatalf("prepared count = %d, want %d", len(prepared.formats), len(tc.Expected.SourceIndexes))
				}
				for i, wantSource := range tc.Expected.SourceIndexes {
					if gotSource := prepared.formats[i].Source; gotSource != wantSource {
						t.Fatalf("prepared[%d].source = %d, want %d", i, gotSource, wantSource)
					}
				}
			}
			if len(tc.Expected.FormatIDs) > 0 {
				for i, wantID := range tc.Expected.FormatIDs {
					if gotID := prepared.formats[i].ID; gotID != wantID {
						t.Fatalf("prepared[%d].id = %q, want %q", i, gotID, wantID)
					}
				}
			}
			if len(tc.Expected.EffectiveFields) > 0 {
				extractor, err := extractExtractorSortFields(prepared.Info())
				if err != nil {
					t.Fatalf("extract extractor sort fields: %v", err)
				}
				sorter, err := newSorter(opts, prepared.Info(), extractor)
				if err != nil {
					t.Fatalf("newSorter: %v", err)
				}
				gotFields := make([]string, len(sorter.fields))
				for i, token := range sorter.fields {
					gotFields[i] = token.canonical
					if token.reverse {
						gotFields[i] = "+" + gotFields[i]
					}
				}
				if !reflect.DeepEqual(gotFields, tc.Expected.EffectiveFields) {
					t.Fatalf("effective fields = %v, want %v", gotFields, tc.Expected.EffectiveFields)
				}
			}
			if len(tc.Expected.DerivedFields) > 0 {
				assertDerivedFields(t, prepared.formats, tc.Expected.DerivedFields)
			}
		})
	}
}

func assertDerivedFields(t *testing.T, formats []normalizedFormat, want map[string]any) {
	t.Helper()
	// Map suffix names like "v_video_ext" -> source index 0.
	indexed := map[string]int{}
	_ = indexed
	for key, expected := range want {
		// Source-index prefixed keys: "0_video_ext", "1_audio_ext", ...
		var sourceIndex int
		var rawKey string
		if underscore := strings.IndexByte(key, '_'); underscore > 0 {
			if idx, err := parseIntPrefix(key[:underscore]); err == nil {
				sourceIndex = idx
				rawKey = key[underscore+1:]
			}
		}
		if rawKey == "" {
			rawKey = key
		}
		var target *value.Object
		for index := range formats {
			if formats[index].Source == sourceIndex {
				target = formats[index].Object
				break
			}
		}
		if target == nil {
			t.Fatalf("derived field %q: source index %d out of range", key, sourceIndex)
		}
		raw := target.Lookup(rawKey)
		got, ok := jsonEqualAny(raw, expected)
		if !ok || !reflect.DeepEqual(got, expected) {
			t.Fatalf("derived %q = %v (raw %#v), want %v", key, got, raw, expected)
		}
		_ = indexed
	}
}

func parseIntPrefix(text string) (int, error) {
	var v int
	for _, r := range text {
		if r < '0' || r > '9' {
			return 0, fmt.Errorf("not an integer: %s", text)
		}
		v = v*10 + int(r-'0')
	}
	return v, nil
}

func jsonEqualAny(raw value.Value, expected any) (any, bool) {
	switch typed := expected.(type) {
	case nil:
		if raw.IsNull() {
			return nil, true
		}
		if raw.IsMissing() {
			return nil, true
		}
	case string:
		if text, ok := raw.StringValue(); ok {
			return text, true
		}
	case float64:
		if integer, ok := raw.Int(); ok {
			if float64(integer) == typed {
				return typed, true
			}
		}
		if floating, ok := raw.Float(); ok {
			if floating == typed {
				return typed, true
			}
		}
	}
	return nil, false
}

// TestFormatSorterFieldComposition validates that the sorter composes the
// effective field list with the pinned first-occurrence-wins rule.
func TestFormatSorterFieldComposition(t *testing.T) {
	cases := []struct {
		name   string
		sort   []string
		force  bool
		ext    []string
		expect []string
	}{
		{
			name:   "default-only",
			sort:   nil,
			force:  false,
			expect: []string{"hidden", "aud_or_vid", "hasvid", "ie_pref", "lang", "quality", "res", "fps", "hdr", "vcodec", "channels", "acodec", "size", "br", "asr", "proto", "vext", "aext", "hasaud", "source", "id"},
		},
		{
			name:   "user-before-extractor",
			sort:   []string{"+res"},
			force:  false,
			ext:    []string{"lang"},
			expect: []string{"hidden", "aud_or_vid", "hasvid", "ie_pref", "+res", "lang", "quality", "fps", "hdr", "vcodec", "channels", "acodec", "size", "br", "asr", "proto", "vext", "aext", "hasaud", "source", "id"},
		},
		{
			name:   "sort-force-skips-priority-defaults",
			sort:   []string{"+quality"},
			force:  true,
			expect: []string{"hidden", "aud_or_vid", "+quality", "hasvid", "ie_pref", "lang", "res", "fps", "hdr", "vcodec", "channels", "acodec", "size", "br", "asr", "proto", "vext", "aext", "hasaud", "source", "id"},
		},
		{
			name:   "duplicate-user-fields-keep-first",
			sort:   []string{"quality", "res", "quality"},
			force:  false,
			expect: []string{"hidden", "aud_or_vid", "hasvid", "ie_pref", "quality", "res", "lang", "fps", "hdr", "vcodec", "channels", "acodec", "size", "br", "asr", "proto", "vext", "aext", "hasaud", "source", "id"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			opts := Options{SortForce: tc.force}
			if len(tc.sort) > 0 {
				fields, err := ParseSortFields(tc.sort)
				if err != nil {
					t.Fatal(err)
				}
				opts.Sort = fields
			}
			sorter, err := newSorter(opts, value.NewInfo(value.NewObject()), tc.ext)
			if err != nil {
				t.Fatal(err)
			}
			got := make([]string, len(sorter.fields))
			for i, token := range sorter.fields {
				got[i] = token.canonical
				if token.reverse {
					got[i] = "+" + got[i]
				}
			}
			if !reflect.DeepEqual(got, tc.expect) {
				t.Fatalf("effective fields = %v, want %v", got, tc.expect)
			}
		})
	}
}

// TestFormatSorterAliasesAndLimits validates alias expansion, combined-field
// expansion, and parser bounds.
func TestFormatSorterAliasesAndLimits(t *testing.T) {
	t.Run("alias-canonicalizes", func(t *testing.T) {
		setting, target, ok := lookupFieldSetting("format_id")
		if !ok {
			t.Fatal("format_id alias missing")
		}
		if target != "id" || setting.canonical != "id" {
			t.Fatalf("format_id -> %q, %q; want id, id", target, setting.canonical)
		}
	})
	t.Run("deprecated-alias-flagged", func(t *testing.T) {
		alias, ok := sorterAliasTable["dimension"]
		if !ok || alias.target != "res" || !alias.deprecated {
			t.Fatalf("dimension alias = %+v; want res/deprecated", alias)
		}
	})
	t.Run("combined-field-expansion", func(t *testing.T) {
		sorter, err := newSorter(Options{Sort: mustParseSortFields(t, "ext:mp4:m4a")},
			value.NewInfo(value.NewObject()), nil)
		if err != nil {
			t.Fatal(err)
		}
		// ext must be deduplicated: first occurrence is the expanded vext/aext
		// pair, the trailing default `ext` must be ignored.
		canonical := make([]string, 0, len(sorter.fields))
		for _, token := range sorter.fields {
			canonical = append(canonical, token.canonical)
		}
		var foundVext, foundAext bool
		for _, name := range canonical {
			switch name {
			case "vext":
				foundVext = true
			case "aext":
				foundAext = true
			case "ext":
				t.Fatalf("combined `ext` was not deduplicated; got full list %v", canonical)
			}
		}
		if !foundVext || !foundAext {
			t.Fatalf("combined ext did not expand to vext and aext; got %v", canonical)
		}
	})
	t.Run("oversized-sort-rejected", func(t *testing.T) {
		_, err := ParseSortField(strings.Repeat("x", 257))
		if err == nil {
			t.Fatal("expected error for oversized sort field")
		}
	})
	t.Run("empty-sort-rejected", func(t *testing.T) {
		_, err := ParseSortField("")
		if err == nil {
			t.Fatal("expected error for empty sort field")
		}
	})
	t.Run("trailing-junk-rejected", func(t *testing.T) {
		_, err := ParseSortField("height:100junk")
		if err == nil {
			t.Fatal("expected error for trailing junk")
		}
	})
	t.Run("textual-ordered-limit", func(t *testing.T) {
		fields, err := ParseSortFields([]string{"vcodec:vp9"})
		if err != nil {
			t.Fatal(err)
		}
		sorter, err := newSorter(Options{Sort: fields}, value.NewInfo(value.NewObject()), nil)
		if err != nil {
			t.Fatal(err)
		}
		var token *sortToken
		for index := range sorter.fields {
			if sorter.fields[index].canonical == fieldVCodec {
				token = &sorter.fields[index]
				break
			}
		}
		if token == nil || token.limit == nil {
			t.Fatalf("ordered limit was not preserved: %+v", token)
		}
	})
	t.Run("maximum-raw-lists-deduplicate-before-effective-bound", func(t *testing.T) {
		user := make([]SortField, maxUserSortFields)
		extractor := make([]string, maxExtractorSortFields)
		for i := range user {
			user[i] = SortField{Field: fieldQuality}
			extractor[i] = fieldQuality
		}
		if _, err := newSorter(Options{Sort: user}, value.NewInfo(value.NewObject()), extractor); err != nil {
			t.Fatalf("valid maximum lists rejected before deduplication: %v", err)
		}
	})
	t.Run("ordered-regexes-use-prefix-match", func(t *testing.T) {
		setting, _, _ := lookupFieldSetting(fieldVCodec)
		unknown, _ := orderedRank(setting, "prefix-av1", false)
		empty, _ := orderedRank(setting, "", false)
		if unknown != empty {
			t.Fatalf("ordered regex searched inside value: got %v, want unknown rank %v", unknown, empty)
		}
	})
}

func mustParseSortFields(t *testing.T, raw string) []SortField {
	t.Helper()
	fields, err := ParseSortFields([]string{raw})
	if err != nil {
		t.Fatal(err)
	}
	return fields
}

func fieldNames(s *sorter) []string {
	out := make([]string, len(s.fields))
	for i, token := range s.fields {
		out[i] = token.canonical
	}
	return out
}

// TestFormatSorterOrderingContract verifies the comparator and worst-to-best
// canonical ordering.
func TestFormatSorterOrderingContract(t *testing.T) {
	formats := []value.Value{
		makeFormat("a", map[string]any{"format_id": "a", "url": "https://example.invalid/a",
			"ext": "mp4", "vcodec": "avc1", "acodec": "none",
			"height": float64(720), "tbr": float64(1500)}),
		makeFormat("b", map[string]any{"format_id": "b", "url": "https://example.invalid/b",
			"ext": "mp4", "vcodec": "avc1", "acodec": "none",
			"height": float64(360), "tbr": float64(500)}),
		makeFormat("c", map[string]any{"format_id": "c", "url": "https://example.invalid/c",
			"ext": "m4a", "vcodec": "none", "acodec": "aac",
			"tbr": float64(64)}),
	}
	info := value.NewInfo(value.NewObject(value.Field{Key: "formats", Value: value.List(formats...)}))
	prepared, err := Prepare(info, Options{})
	if err != nil {
		t.Fatal(err)
	}
	wantSourceOrder := []int{2, 1, 0}
	for i, want := range wantSourceOrder {
		if prepared.formats[i].Source != want {
			t.Fatalf("prepared[%d].source = %d, want %d (full order %v)",
				i, prepared.formats[i].Source, want, sourceOrder(prepared))
		}
	}
	t.Run("stable-complete-ties", func(t *testing.T) {
		tied := []value.Value{
			makeFormat("x", map[string]any{"format_id": "x", "url": "https://example.invalid/x1",
				"ext": "mp4", "vcodec": "avc1", "acodec": "none", "height": 720, "tbr": 1500}),
			makeFormat("y", map[string]any{"format_id": "y", "url": "https://example.invalid/y1",
				"ext": "mp4", "vcodec": "avc1", "acodec": "none", "height": 720, "tbr": 1500}),
		}
		tiedInfo := value.NewInfo(value.NewObject(value.Field{Key: "formats", Value: value.List(tied...)}))
		tiedPrepared, err := Prepare(tiedInfo, Options{})
		if err != nil {
			t.Fatal(err)
		}
		if tiedPrepared.formats[0].Source != 0 || tiedPrepared.formats[1].Source != 1 {
			t.Fatalf("stable ties reordered: %v", sourceOrder(tiedPrepared))
		}
	})
}

func sourceOrder(prepared Prepared) []int {
	out := make([]int, len(prepared.formats))
	for i, item := range prepared.formats {
		out[i] = item.Source
	}
	return out
}

func makeFormat(_ string, fields map[string]any) value.Value {
	obj := value.NewObject()
	keys := make([]string, 0, len(fields))
	for k := range fields {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		obj.Set(k, jsonAnyToValue(fields[k]))
	}
	return value.ObjectValue(obj)
}

// TestFormatSorterDerivedFields verifies the pinned observable derived
// sorting fields are filled on the canonical format.
func TestFormatSorterDerivedFields(t *testing.T) {
	t.Run("protocol-and-ext-derived", func(t *testing.T) {
		info := value.NewInfo(value.NewObject(value.Field{Key: "formats", Value: value.List(
			value.ObjectValue(value.NewObject(
				value.Field{Key: "format_id", Value: value.String("p")},
				value.Field{Key: "url", Value: value.String("https://example.invalid/track?x=1")},
			)),
		)}))
		prepared, err := Prepare(info, Options{})
		if err != nil {
			t.Fatal(err)
		}
		if got, _ := prepared.formats[0].Object.Lookup("protocol").StringValue(); got != "https" {
			t.Fatalf("protocol = %q, want https", got)
		}
		if got, _ := prepared.formats[0].Object.Lookup("ext").StringValue(); got != "unknown_video" {
			t.Fatalf("ext = %q, want unknown_video default", got)
		}
	})
	t.Run("hevc-over-flv-preference", func(t *testing.T) {
		info := value.NewInfo(value.NewObject(value.Field{Key: "formats", Value: value.List(
			value.ObjectValue(value.NewObject(
				value.Field{Key: "format_id", Value: value.String("flv")},
				value.Field{Key: "url", Value: value.String("https://example.invalid/flv")},
				value.Field{Key: "ext", Value: value.String("flv")},
				value.Field{Key: "vcodec", Value: value.String("h265")},
				value.Field{Key: "acodec", Value: value.String("aac")},
			)),
		)}))
		prepared, err := Prepare(info, Options{})
		if err != nil {
			t.Fatal(err)
		}
		if got, _ := prepared.formats[0].Object.Lookup("preference").Int(); got != -100 {
			t.Fatalf("preference = %d, want -100", got)
		}
	})
	t.Run("audio-video-ext-derived", func(t *testing.T) {
		video := value.ObjectValue(value.NewObject(
			value.Field{Key: "format_id", Value: value.String("v")},
			value.Field{Key: "url", Value: value.String("https://example.invalid/v")},
			value.Field{Key: "ext", Value: value.String("mp4")},
			value.Field{Key: "vcodec", Value: value.String("avc1")},
			value.Field{Key: "acodec", Value: value.String("none")},
		))
		audio := value.ObjectValue(value.NewObject(
			value.Field{Key: "format_id", Value: value.String("a")},
			value.Field{Key: "url", Value: value.String("https://example.invalid/a")},
			value.Field{Key: "ext", Value: value.String("m4a")},
			value.Field{Key: "vcodec", Value: value.String("none")},
			value.Field{Key: "acodec", Value: value.String("aac")},
		))
		info := value.NewInfo(value.NewObject(value.Field{Key: "formats", Value: value.List(video, audio)}))
		prepared, err := Prepare(info, Options{})
		if err != nil {
			t.Fatal(err)
		}
		// Worst-to-best: audio first (vcodec=none => aud_or_vid == 1 but hasvid == 0),
		// then video.
		if got, _ := prepared.formats[0].Object.Lookup("video_ext").StringValue(); got != "none" {
			t.Fatalf("audio video_ext = %q, want none", got)
		}
		if got, _ := prepared.formats[0].Object.Lookup("audio_ext").StringValue(); got != "m4a" {
			t.Fatalf("audio audio_ext = %q, want m4a", got)
		}
		if got, _ := prepared.formats[1].Object.Lookup("video_ext").StringValue(); got != "mp4" {
			t.Fatalf("video video_ext = %q, want mp4", got)
		}
		if got, _ := prepared.formats[1].Object.Lookup("audio_ext").StringValue(); got != "none" {
			t.Fatalf("video audio_ext = %q, want none", got)
		}
	})
	t.Run("zero-bitrate-is-derived", func(t *testing.T) {
		format := value.ObjectValue(value.NewObject(
			value.Field{Key: "format_id", Value: value.String("zero")},
			value.Field{Key: "url", Value: value.String("https://example.invalid/zero")},
			value.Field{Key: "ext", Value: value.String("mp4")},
			value.Field{Key: "vcodec", Value: value.String("avc1")},
			value.Field{Key: "acodec", Value: value.String("aac")},
			value.Field{Key: "tbr", Value: value.Int(1000)},
			value.Field{Key: "vbr", Value: value.Int(0)},
			value.Field{Key: "abr", Value: value.Int(200)},
		))
		prepared, err := Prepare(value.NewInfo(value.NewObject(value.Field{Key: "formats", Value: value.List(format)})), Options{})
		if err != nil {
			t.Fatal(err)
		}
		if got, _ := prepared.formats[0].Object.Lookup("vbr").Float(); got != 800 {
			t.Fatalf("vbr = %v, want 800", got)
		}
	})
	t.Run("hevc-pattern-is-prefix-and-requires-h-or-x", func(t *testing.T) {
		if !matchesHEVC("x265-main") || matchesHEVC("265-main") || matchesHEVC("prefix-h265") {
			t.Fatal("HEVC preference pattern does not match pinned re.match semantics")
		}
	})
}

// TestFormatSorterEvaluatorReversalAdapter verifies the adapter contract:
// new slice, reverse iteration, no mutation of canonical, preserved pointers.
func TestFormatSorterEvaluatorReversalAdapter(t *testing.T) {
	formats := []value.Value{
		value.ObjectValue(value.NewObject(value.Field{Key: "format_id", Value: value.String("a")},
			value.Field{Key: "url", Value: value.String("https://example.invalid/a")})),
		value.ObjectValue(value.NewObject(value.Field{Key: "format_id", Value: value.String("b")},
			value.Field{Key: "url", Value: value.String("https://example.invalid/b")})),
		value.ObjectValue(value.NewObject(value.Field{Key: "format_id", Value: value.String("c")},
			value.Field{Key: "url", Value: value.String("https://example.invalid/c")})),
	}
	info := value.NewInfo(value.NewObject(value.Field{Key: "formats", Value: value.List(formats...)}))
	prepared, err := Prepare(info, Options{})
	if err != nil {
		t.Fatal(err)
	}
	adapter := prepared.evaluationFormats()
	if len(adapter) != len(prepared.formats) {
		t.Fatalf("adapter length = %d, want %d", len(adapter), len(prepared.formats))
	}
	for i := range prepared.formats {
		if adapter[i] != prepared.formats[len(prepared.formats)-1-i].Object {
			t.Fatalf("adapter[%d] != reversed canonical[%d]", i, len(prepared.formats)-1-i)
		}
	}
	prepared.formats[0].Object.Set("format_id", value.String("mutated"))
	if got, _ := adapter[len(adapter)-1].Lookup("format_id").StringValue(); got != "mutated" {
		t.Fatalf("adapter does not share pointer identity")
	}
	originalCanonical := prepared.formats[0].ID
	_ = prepared.evaluationFormats()
	if prepared.formats[0].ID != originalCanonical {
		t.Fatalf("canonical mutated by evaluationFormats adapter call")
	}
}

// TestFormatSorterSourceAndNormalizedIndexes verifies both indexes are
// preserved through preparation and reverse ordering.
func TestFormatSorterSourceAndNormalizedIndexes(t *testing.T) {
	formats := []value.Value{
		value.ObjectValue(value.NewObject(value.Field{Key: "format_id", Value: value.String("a")},
			value.Field{Key: "url", Value: value.String("https://example.invalid/a")},
			value.Field{Key: "preference", Value: value.Int(0)})),
		value.ObjectValue(value.NewObject(value.Field{Key: "format_id", Value: value.String("b")},
			value.Field{Key: "url", Value: value.String("https://example.invalid/b")},
			value.Field{Key: "preference", Value: value.Int(10)})),
	}
	info := value.NewInfo(value.NewObject(value.Field{Key: "formats", Value: value.List(formats...)}))
	prepared, err := Prepare(info, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if prepared.formats[0].Source != 0 || prepared.formats[1].Source != 1 {
		t.Fatalf("source indexes = %v, want [0 1]", sourceOrder(prepared))
	}
	if prepared.formats[0].Index != 0 || prepared.formats[1].Index != 1 {
		t.Fatalf("normalized indexes = [%d %d], want [0 1]",
			prepared.formats[0].Index, prepared.formats[1].Index)
	}
}

// TestFormatSorterHeadersURLPreserved verifies that after sorting and ID
// generation, the canonical format still carries the original URL and headers.
func TestFormatSorterHeadersURLPreserved(t *testing.T) {
	info := value.NewInfo(value.NewObject(value.Field{Key: "formats", Value: value.List(
		value.ObjectValue(value.NewObject(
			value.Field{Key: "format_id", Value: value.String("a")},
			value.Field{Key: "url", Value: value.String("https://example.invalid/a")},
			value.Field{Key: "http_headers", Value: value.ObjectValue(value.NewObject(
				value.Field{Key: "X-Track", Value: value.String("alpha")},
			))},
			value.Field{Key: "preference", Value: value.Int(0)},
		)),
		value.ObjectValue(value.NewObject(
			value.Field{Key: "format_id", Value: value.String("b")},
			value.Field{Key: "url", Value: value.String("https://example.invalid/b")},
			value.Field{Key: "http_headers", Value: value.ObjectValue(value.NewObject(
				value.Field{Key: "X-Track", Value: value.String("beta")},
			))},
			value.Field{Key: "preference", Value: value.Int(10)},
		)),
	)}))
	prepared, err := Prepare(info, Options{})
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range prepared.formats {
		if _, ok := item.Object.Lookup("url").StringValue(); !ok {
			t.Fatalf("format %d missing url", item.Source)
		}
		headers, ok := item.Object.Lookup("http_headers").Object()
		if !ok {
			t.Fatalf("format %d missing headers", item.Source)
		}
		track, _ := headers.Lookup("X-Track").StringValue()
		if track == "" {
			t.Fatalf("format %d missing X-Track header", item.Source)
		}
	}
}

// TestFormatSorterNonMutationOfExtractorInfo verifies the canonical prepared
// view never mutates the original extractor-owned Info.
func TestFormatSorterNonMutationOfExtractorInfo(t *testing.T) {
	formats := []value.Value{
		value.ObjectValue(value.NewObject(value.Field{Key: "format_id", Value: value.String("a")},
			value.Field{Key: "url", Value: value.String("https://example.invalid/a")},
			value.Field{Key: "ext", Value: value.String("mp4")},
			value.Field{Key: "vcodec", Value: value.String("avc1")},
			value.Field{Key: "acodec", Value: value.String("none")},
		)),
	}
	info := value.NewInfo(value.NewObject(value.Field{Key: "formats", Value: value.List(formats...)}))
	before, _ := json.Marshal(info.Fields())
	if _, err := Prepare(info, Options{}); err != nil {
		t.Fatal(err)
	}
	after, _ := json.Marshal(info.Fields())
	if string(before) != string(after) {
		t.Fatalf("original info mutated\nbefore=%s\nafter=%s", before, after)
	}
}

// TestFormatSorterConcurrentDeterministic verifies preparation is safe and
// deterministic across goroutines.
func TestFormatSorterConcurrentDeterministic(t *testing.T) {
	formats := []value.Value{
		value.ObjectValue(value.NewObject(value.Field{Key: "format_id", Value: value.String("a")},
			value.Field{Key: "url", Value: value.String("https://example.invalid/a")},
			value.Field{Key: "preference", Value: value.Int(0)})),
		value.ObjectValue(value.NewObject(value.Field{Key: "format_id", Value: value.String("b")},
			value.Field{Key: "url", Value: value.String("https://example.invalid/b")},
			value.Field{Key: "preference", Value: value.Int(10)})),
		value.ObjectValue(value.NewObject(value.Field{Key: "format_id", Value: value.String("c")},
			value.Field{Key: "url", Value: value.String("https://example.invalid/c")},
			value.Field{Key: "preference", Value: value.Int(5)})),
	}
	info := value.NewInfo(value.NewObject(value.Field{Key: "formats", Value: value.List(formats...)}))
	const workers = 16
	done := make(chan []int, workers)
	for i := 0; i < workers; i++ {
		go func() {
			prepared, err := Prepare(info, Options{})
			if err != nil {
				done <- nil
				return
			}
			out := make([]int, len(prepared.formats))
			for i, item := range prepared.formats {
				out[i] = item.Source
			}
			done <- out
		}()
	}
	first := <-done
	for i := 0; i < workers-1; i++ {
		got := <-done
		if got == nil {
			t.Fatal("concurrent prepare failed")
		}
		if !reflect.DeepEqual(got, first) {
			t.Fatalf("nondeterministic order: %v vs %v", got, first)
		}
	}
}

// TestFormatSorterMixedTypeOrdering verifies mixed numeric/string ordering.
func TestFormatSorterMixedTypeOrdering(t *testing.T) {
	formats := []value.Value{
		value.ObjectValue(value.NewObject(value.Field{Key: "format_id", Value: value.String("n")},
			value.Field{Key: "url", Value: value.String("https://example.invalid/n")},
			value.Field{Key: "custom_rank", Value: value.Float(5)})),
		value.ObjectValue(value.NewObject(value.Field{Key: "format_id", Value: value.String("s")},
			value.Field{Key: "url", Value: value.String("https://example.invalid/s")},
			value.Field{Key: "custom_rank", Value: value.String("high")})),
	}
	info := value.NewInfo(value.NewObject(value.Field{Key: "formats", Value: value.List(formats...)}))
	prepared, err := Prepare(info, Options{Sort: mustParseSortFields(t, "custom_rank")})
	if err != nil {
		t.Fatal(err)
	}
	// Strings sort higher (later) than numbers per the pinned comparator.
	if prepared.formats[0].Source != 0 {
		t.Fatalf("mixed order = %v, want [0 1] (numeric first)", sourceOrder(prepared))
	}
}

// TestFormatSorterExtractedExtractorFields verifies extractor sort fields
// parsing bounds and error handling.
func TestFormatSorterExtractedExtractorFields(t *testing.T) {
	t.Run("missing", func(t *testing.T) {
		info := value.NewInfo(value.NewObject(value.Field{Key: "formats", Value: value.List(
			value.ObjectValue(value.NewObject(value.Field{Key: "format_id", Value: value.String("a")},
				value.Field{Key: "url", Value: value.String("https://example.invalid/a")})),
		)}))
		fields, err := extractExtractorSortFields(info)
		if err != nil || len(fields) != 0 {
			t.Fatalf("expected empty, got %v err=%v", fields, err)
		}
	})
	t.Run("non-list", func(t *testing.T) {
		fields := value.NewObject()
		fields.Set("formats", value.List(value.ObjectValue(value.NewObject(
			value.Field{Key: "format_id", Value: value.String("a")},
			value.Field{Key: "url", Value: value.String("https://example.invalid/a")}))))
		fields.Set("_format_sort_fields", value.String("lang"))
		info := value.NewInfo(fields)
		_, err := extractExtractorSortFields(info)
		if err == nil {
			t.Fatal("expected error for non-list extractor sort fields")
		}
	})
	t.Run("non-string-member", func(t *testing.T) {
		fields := value.NewObject()
		fields.Set("formats", value.List(value.ObjectValue(value.NewObject(
			value.Field{Key: "format_id", Value: value.String("a")},
			value.Field{Key: "url", Value: value.String("https://example.invalid/a")}))))
		fields.Set("_format_sort_fields", value.List(value.Int(42)))
		info := value.NewInfo(fields)
		_, err := extractExtractorSortFields(info)
		if err == nil {
			t.Fatal("expected error for non-string extractor sort field")
		}
	})
	t.Run("oversized-list", func(t *testing.T) {
		items := make([]value.Value, maxExtractorSortFields+1)
		for i := range items {
			items[i] = value.String("x")
		}
		fields := value.NewObject()
		fields.Set("formats", value.List(value.ObjectValue(value.NewObject(
			value.Field{Key: "format_id", Value: value.String("a")},
			value.Field{Key: "url", Value: value.String("https://example.invalid/a")}))))
		fields.Set("_format_sort_fields", value.List(items...))
		info := value.NewInfo(fields)
		_, err := extractExtractorSortFields(info)
		if err == nil {
			t.Fatal("expected error for oversized extractor sort fields list")
		}
	})
}
