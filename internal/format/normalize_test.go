package format

import (
	"bytes"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/tejasa97/ytdlp-go/internal/value"
)

func normalizationInfo(formats ...value.Value) value.Info {
	return value.NewInfo(value.NewObject(value.Field{Key: "formats", Value: value.List(formats...)}))
}

func normalizationFormat(fields ...value.Field) value.Value {
	return value.ObjectValue(value.NewObject(fields...))
}

func snapshotInfo(t *testing.T, info value.Info) []byte {
	t.Helper()
	body, err := json.Marshal(info.Fields())
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func TestPrepareFormatsNormalizesAfterStableOrdering(t *testing.T) {
	info := normalizationInfo(
		normalizationFormat(
			value.Field{Key: "format_id", Value: value.String("a b/c,d+e[f](g)\t")},
			value.Field{Key: "url", Value: value.String("https://example.invalid/low")},
			value.Field{Key: "ext", Value: value.String("webm")},
			value.Field{Key: "preference", Value: value.Int(0)},
		),
		normalizationFormat(
			value.Field{Key: "url", Value: value.String("https://example.invalid/high")},
			value.Field{Key: "ext", Value: value.String("mp4")},
			value.Field{Key: "preference", Value: value.Int(10)},
		),
	)
	before := snapshotInfo(t, info)
	prepared, err := prepareFormats(info, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if len(prepared.formats) != 2 {
		t.Fatalf("prepared formats = %d", len(prepared.formats))
	}
	if got := prepared.formats[0]; got.Source != 0 || got.ID != "a_b_c_d_e_f__g__" {
		t.Fatalf("first normalized format = %+v", got)
	}
	if got := prepared.formats[1]; got.Source != 1 || got.ID != "1" {
		t.Fatalf("second normalized format = %+v", got)
	}
	if after := snapshotInfo(t, info); !bytes.Equal(before, after) {
		t.Fatalf("original info mutated\nbefore=%s\nafter=%s", before, after)
	}
}

func TestPrepareFormatsDuplicateYouTubeIDsPreserveCanonicalItag(t *testing.T) {
	info := normalizationInfo(
		normalizationFormat(
			value.Field{Key: "format_id", Value: value.String("18")},
			value.Field{Key: "url", Value: value.String("https://a.googlevideo.com/videoplayback")},
			value.Field{Key: "_youtube_itag", Value: value.Int(18)},
		),
		normalizationFormat(
			value.Field{Key: "format_id", Value: value.String("18")},
			value.Field{Key: "url", Value: value.String("https://b.googlevideo.com/videoplayback")},
			value.Field{Key: "_youtube_itag", Value: value.Int(18)},
		),
	)
	prepared, err := prepareFormats(info, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if len(prepared.formats) != 2 {
		t.Fatalf("prepared formats = %d", len(prepared.formats))
	}
	for index, item := range prepared.formats {
		wantID := "18-" + string(rune('0'+index))
		selection, selectionErr := objectSelection(item.Object)
		if selectionErr != nil {
			t.Fatal(selectionErr)
		}
		if item.ID != wantID || selection.ID != wantID || selection.YouTubeItag != 18 {
			t.Fatalf("format %d = id %q selection %#v", index, item.ID, selection)
		}
	}
}

func TestPrepareFormatsDuplicateAndExtensionOnePass(t *testing.T) {
	info := normalizationInfo(
		normalizationFormat(value.Field{Key: "format_id", Value: value.String("x")}, value.Field{Key: "url", Value: value.String("https://example.invalid/x0")}, value.Field{Key: "ext", Value: value.String("webm")}),
		normalizationFormat(value.Field{Key: "format_id", Value: value.String("x")}, value.Field{Key: "url", Value: value.String("https://example.invalid/x1")}, value.Field{Key: "ext", Value: value.String("webm")}),
		normalizationFormat(value.Field{Key: "format_id", Value: value.String("x-0")}, value.Field{Key: "url", Value: value.String("https://example.invalid/x2")}, value.Field{Key: "ext", Value: value.String("webm")}),
		normalizationFormat(value.Field{Key: "format_id", Value: value.String("mp4")}, value.Field{Key: "url", Value: value.String("https://example.invalid/mp4")}, value.Field{Key: "ext", Value: value.String("webm")}),
		normalizationFormat(value.Field{Key: "format_id", Value: value.String("fmp4")}, value.Field{Key: "url", Value: value.String("https://example.invalid/fmp4")}, value.Field{Key: "ext", Value: value.String("webm")}),
	)
	prepared, err := prepareFormats(info, Options{})
	if err != nil {
		t.Fatal(err)
	}
	// Pinned FormatSorter orders by id in worst-to-best; the first
	// collision set ("x") and the second ("x-0"/"x-0") merge into a
	// single duplicate group with pinned suffixes; the "fmp4" and the
	// rewritten "mp4" both end up as plain "fmp4".
	wantIDs := []string{"fmp4", "fmp4", "x-0", "x-1", "x-0"}
	if len(prepared.formats) != len(wantIDs) {
		t.Fatalf("prepared formats = %d, want %d", len(prepared.formats), len(wantIDs))
	}
	gotIDs := make([]string, len(prepared.formats))
	for i, item := range prepared.formats {
		gotIDs[i] = item.ID
	}
	if !reflect.DeepEqual(gotIDs, wantIDs) {
		t.Fatalf("ids = %v, want %v", gotIDs, wantIDs)
	}
}

func TestPrepareFormatsFiltersBeforeGeneratedIDs(t *testing.T) {
	info := normalizationInfo(
		normalizationFormat(value.Field{Key: "url", Value: value.String("https://example.invalid/drm")}, value.Field{Key: "has_drm", Value: value.Bool(true)}),
		normalizationFormat(value.Field{Key: "url", Value: value.String("")}),
		normalizationFormat(value.Field{Key: "url", Value: value.String("https://example.invalid/clear")}),
	)
	prepared, err := prepareFormats(info, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if len(prepared.formats) != 1 || prepared.formats[0].ID != "0" || prepared.formats[0].Source != 2 {
		t.Fatalf("canonical formats = %+v", prepared.formats)
	}
	formats, _ := prepared.Info().Formats()
	if len(formats) != 1 {
		t.Fatalf("canonical Info formats = %d", len(formats))
	}
}

func TestPrepareFormatsCoercesPinnedScalarFieldsBeforeSorting(t *testing.T) {
	info := normalizationInfo(
		normalizationFormat(
			value.Field{Key: "format_id", Value: value.Int(42)},
			value.Field{Key: "url", Value: value.String("https://example.invalid/low")},
			value.Field{Key: "preference", Value: value.String("1")},
			value.Field{Key: "height", Value: value.String("720")},
		),
		normalizationFormat(
			value.Field{Key: "format_id", Value: value.Bool(true)},
			value.Field{Key: "url", Value: value.String("https://example.invalid/high")},
			value.Field{Key: "preference", Value: value.String("2")},
			value.Field{Key: "height", Value: value.String("1080")},
		),
	)
	prepared, err := prepareFormats(info, Options{})
	if err != nil {
		t.Fatal(err)
	}
	// preference is not in _NUMERIC_FIELDS, so string preferences stay strings and
	// do not drive preparation ordering; height is coerced and stable order kept.
	if len(prepared.formats) != 2 || prepared.formats[0].Source != 0 || prepared.formats[0].ID != "42" || prepared.formats[1].ID != "True" {
		t.Fatalf("canonical formats = %+v", prepared.formats)
	}
	if height, ok := prepared.formats[0].Object.Lookup("height").Int(); !ok || height != 720 {
		t.Fatalf("coerced height = %v", prepared.formats[0].Object.Lookup("height"))
	}
	if height, ok := prepared.formats[1].Object.Lookup("height").Int(); !ok || height != 1080 {
		t.Fatalf("coerced height = %v", prepared.formats[1].Object.Lookup("height"))
	}
	if pref, ok := prepared.formats[0].Object.Lookup("preference").StringValue(); !ok || pref != "1" {
		t.Fatalf("preference mutated = %v", prepared.formats[0].Object.Lookup("preference"))
	}
	if pref, ok := prepared.formats[1].Object.Lookup("preference").StringValue(); !ok || pref != "2" {
		t.Fatalf("preference mutated = %v", prepared.formats[1].Object.Lookup("preference"))
	}
}

func TestPrepareFormatsImplicitSharesCanonicalIdentity(t *testing.T) {
	info := value.NewInfo(value.NewObject(
		value.Field{Key: "id", Value: value.String("clip")},
		value.Field{Key: "url", Value: value.String("https://example.invalid/original")},
		value.Field{Key: "ext", Value: value.String("mp4")},
	))
	prepared, err := prepareFormats(info, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if len(prepared.formats) != 1 {
		t.Fatalf("formats = %d", len(prepared.formats))
	}
	if prepared.formats[0].Object != prepared.Info().Fields() {
		t.Fatal("implicit prepared format must share canonical Fields identity")
	}
	prepared.formats[0].Object.Set("url", value.String("https://example.invalid/replaced"))
	if got, _ := prepared.Info().Lookup("url").StringValue(); got != "https://example.invalid/replaced" {
		t.Fatalf("canonical Info url diverged = %q", got)
	}
}

func TestSanitizeFormatIDMatchesPythonRegexWhitespace(t *testing.T) {
	input := "a\x1cb\x1dc\x1ed\x1fe f,\t/+\n[]()"
	want := "a_b_c_d_e_f_________"
	if got := sanitizeFormatID(input); got != want {
		t.Fatalf("sanitizeFormatID = %q, want %q", got, want)
	}
	// Boundary: U+001B is not Python \\s; U+001C is.
	if got := sanitizeFormatID("x\x1by"); got != "x\x1by" {
		t.Fatalf("U+001B changed: %q", got)
	}
	if got := sanitizeFormatID("x\x1cy"); got != "x_y" {
		t.Fatalf("U+001C unchanged: %q", got)
	}
	if got := sanitizeFormatID("x\x1fy"); got != "x_y" {
		t.Fatalf("U+001F unchanged: %q", got)
	}
}

func TestPrepareFormatsRejectsMalformedAndExcessiveCollections(t *testing.T) {
	tests := []struct {
		name string
		info value.Info
		want error
	}{
		{"non-list", value.NewInfo(value.NewObject(value.Field{Key: "formats", Value: value.String("bad")})), ErrInvalidFormats},
		{"non-object", normalizationInfo(value.String("bad")), ErrInvalidFormats},
		{"null-member", normalizationInfo(value.Null()), ErrInvalidFormats},
		{"raw-id-limit", normalizationInfo(normalizationFormat(
			value.Field{Key: "format_id", Value: value.String(strings.Repeat("x", maxNormalizedIDBytes+1))},
			value.Field{Key: "url", Value: value.String("https://example.invalid/media")},
		)), ErrFormatLimit},
	}
	tooMany := make([]value.Value, maxNormalizedEntries+1)
	for index := range tooMany {
		tooMany[index] = normalizationFormat(value.Field{Key: "format_id", Value: value.String("x")})
	}
	tests = append(tests, struct {
		name string
		info value.Info
		want error
	}{"entry-limit", normalizationInfo(tooMany...), ErrFormatLimit})
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			before := snapshotInfo(t, test.info)
			_, err := prepareFormats(test.info, Options{})
			if !errors.Is(err, test.want) {
				t.Fatalf("error = %v, want %v", err, test.want)
			}
			if after := snapshotInfo(t, test.info); !bytes.Equal(before, after) {
				t.Fatal("input mutated on error")
			}
		})
	}
}

func TestPrepareFormatsAggregateFinalIDLimit(t *testing.T) {
	formats := make([]value.Value, 257)
	id := strings.Repeat("x", maxNormalizedIDBytes-5)
	for index := range formats {
		formats[index] = normalizationFormat(
			value.Field{Key: "format_id", Value: value.String(id)},
			value.Field{Key: "url", Value: value.String("https://example.invalid/media")},
		)
	}
	if _, err := prepareFormats(normalizationInfo(formats...), Options{}); !errors.Is(err, ErrFormatLimit) {
		t.Fatalf("error = %v, want ErrFormatLimit", err)
	}
}

func TestPrepareFormatsDeepCloneAndConcurrentDeterminism(t *testing.T) {
	nested := value.NewObject(value.Field{Key: "token", Value: value.String("original")})
	info := normalizationInfo(normalizationFormat(
		value.Field{Key: "format_id", Value: value.String("a b")},
		value.Field{Key: "url", Value: value.String("https://example.invalid/media")},
		value.Field{Key: "http_headers", Value: value.ObjectValue(nested)},
	))
	before := snapshotInfo(t, info)
	const workers = 16
	var wg sync.WaitGroup
	errs := make(chan error, workers)
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			prepared, err := prepareFormats(info, Options{})
			if err != nil {
				errs <- err
				return
			}
			if len(prepared.formats) != 1 || prepared.formats[0].ID != "a_b" || prepared.formats[0].Source != 0 {
				errs <- errors.New("nondeterministic prepared output")
				return
			}
			headers, _ := prepared.formats[0].Object.Lookup("http_headers").Object()
			headers.Set("token", value.String("changed"))
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	if after := snapshotInfo(t, info); !bytes.Equal(before, after) {
		t.Fatal("concurrent normalization mutated original metadata")
	}
}

func TestPlanSelectUsesExactCloneForResidualCollisionHeaders(t *testing.T) {
	info := value.NewInfo(value.NewObject(
		value.Field{Key: "http_headers", Value: value.ObjectValue(value.NewObject(value.Field{Key: "Referer", Value: value.String("https://page.example/")}))},
		value.Field{Key: "formats", Value: value.List(
			normalizationFormat(value.Field{Key: "format_id", Value: value.String("mp4")}, value.Field{Key: "url", Value: value.String("https://example.invalid/one")}, value.Field{Key: "ext", Value: value.String("webm")}, value.Field{Key: "http_headers", Value: value.ObjectValue(value.NewObject(value.Field{Key: "X-Track", Value: value.String("one")}))}),
			normalizationFormat(value.Field{Key: "format_id", Value: value.String("fmp4")}, value.Field{Key: "url", Value: value.String("https://example.invalid/two")}, value.Field{Key: "ext", Value: value.String("webm")}, value.Field{Key: "http_headers", Value: value.ObjectValue(value.NewObject(value.Field{Key: "X-Track", Value: value.String("two")}))}),
		)},
	))
	selector, err := ParseSelector("all")
	if err != nil {
		t.Fatal(err)
	}
	plans, err := PlanSelect(info, selector)
	if err != nil {
		t.Fatal(err)
	}
	if len(plans) != 2 {
		t.Fatalf("plan count = %d, want 2", len(plans))
	}
	sourceToHeader := map[int]string{}
	for _, plan := range plans {
		if len(plan.Tracks) != 1 {
			t.Fatalf("plan track count = %d, want 1", len(plan.Tracks))
		}
		source, known := plan.Tracks[0].SourceFormatIndex()
		if !known {
			t.Fatal("source index unknown")
		}
		sourceToHeader[source] = plan.Tracks[0].Headers.Get("X-Track")
	}
	if sourceToHeader[0] != "one" || sourceToHeader[1] != "two" {
		t.Fatalf("source headers = %+v", sourceToHeader)
	}
}

func TestFormatSelectionEntryPointsDoNotMutateInfo(t *testing.T) {
	info := normalizationInfo(
		normalizationFormat(
			value.Field{Key: "format_id", Value: value.String("video main")},
			value.Field{Key: "url", Value: value.String("https://example.invalid/video")},
			value.Field{Key: "ext", Value: value.String("mp4")},
			value.Field{Key: "vcodec", Value: value.String("avc1")},
			value.Field{Key: "acodec", Value: value.String("none")},
		),
		normalizationFormat(
			value.Field{Key: "format_id", Value: value.String("audio main")},
			value.Field{Key: "url", Value: value.String("https://example.invalid/audio")},
			value.Field{Key: "ext", Value: value.String("m4a")},
			value.Field{Key: "vcodec", Value: value.String("none")},
			value.Field{Key: "acodec", Value: value.String("aac")},
		),
	)
	before := snapshotInfo(t, info)
	all, err := ParseSelector("all")
	if err != nil {
		t.Fatal(err)
	}
	best, err := ParseSelector("best*")
	if err != nil {
		t.Fatal(err)
	}
	calls := []struct {
		name string
		run  func() error
	}{
		{"PlanSelect", func() error { _, err := PlanSelect(info, all); return err }},
		{"Select", func() error { _, err := Select(info, best); return err }},
		{"Default", func() error { _, err := Default(info, Options{}); return err }},
		{"Best", func() error { _, err := Best(info); return err }},
	}
	for _, call := range calls {
		t.Run(call.name, func(t *testing.T) {
			if err := call.run(); err != nil {
				t.Fatal(err)
			}
			if after := snapshotInfo(t, info); !bytes.Equal(before, after) {
				t.Fatalf("%s mutated original info", call.name)
			}
		})
	}
}

func FuzzPrepareFormats(f *testing.F) {
	f.Add("a b", "mp4", false)
	f.Add("mp4", "webm", true)
	f.Fuzz(func(t *testing.T, id, ext string, drm bool) {
		if len(id) > 1024 || len(ext) > 64 {
			t.Skip()
		}
		info := normalizationInfo(normalizationFormat(
			value.Field{Key: "format_id", Value: value.String(id)},
			value.Field{Key: "url", Value: value.String("https://example.invalid/media")},
			value.Field{Key: "ext", Value: value.String(ext)},
			value.Field{Key: "has_drm", Value: value.Bool(drm)},
		))
		before := snapshotInfo(t, info)
		prepared, err := prepareFormats(info, Options{})
		if err != nil && !errors.Is(err, ErrNoFormats) && !errors.Is(err, ErrInvalidFormats) && !errors.Is(err, ErrFormatLimit) {
			t.Fatalf("unexpected error: %v", err)
		}
		for _, item := range prepared.formats {
			if len(item.ID) > maxNormalizedIDBytes {
				t.Fatalf("oversized normalized ID: %d", len(item.ID))
			}
		}
		if after := snapshotInfo(t, info); !bytes.Equal(before, after) {
			t.Fatal("input mutated")
		}
	})
}
