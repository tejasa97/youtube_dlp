package format

import (
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/ytdlp-go/ytdlp/internal/value"
)

func selectorInfo() value.Info {
	format := func(id, ext, vcodec, acodec string, height int64, tbr float64) value.Value {
		return value.ObjectValue(value.NewObject(
			value.Field{Key: "format_id", Value: value.String(id)},
			value.Field{Key: "url", Value: value.String("https://example.invalid/" + id)},
			value.Field{Key: "ext", Value: value.String(ext)},
			value.Field{Key: "vcodec", Value: value.String(vcodec)},
			value.Field{Key: "acodec", Value: value.String(acodec)},
			value.Field{Key: "height", Value: value.Int(height)},
			value.Field{Key: "tbr", Value: value.Float(tbr)},
		))
	}
	return value.NewInfo(value.NewObject(value.Field{Key: "formats", Value: value.List(
		format("360", "mp4", "avc1", "none", 360, 500),
		format("720", "webm", "vp9", "none", 720, 1500),
		format("audio-low", "m4a", "none", "aac", 0, 64),
		format("audio-high", "m4a", "none", "aac", 0, 128),
	)}))
}

func combinedInfo() value.Info {
	format := func(id string, height int64, tbr float64) value.Value {
		return value.ObjectValue(value.NewObject(
			value.Field{Key: "format_id", Value: value.String(id)},
			value.Field{Key: "url", Value: value.String("https://example.invalid/" + id)},
			value.Field{Key: "ext", Value: value.String("mp4")},
			value.Field{Key: "vcodec", Value: value.String("avc1")},
			value.Field{Key: "acodec", Value: value.String("aac")},
			value.Field{Key: "height", Value: value.Int(height)},
			value.Field{Key: "tbr", Value: value.Float(tbr)},
		))
	}
	return value.NewInfo(value.NewObject(value.Field{Key: "formats", Value: value.List(
		format("240c", 240, 300),
		format("360c", 360, 600),
		format("720c", 720, 1200),
	)}))
}

func TestSelectorFallbackMergeAndFilters(t *testing.T) {
	selector, err := ParseSelector("bestvideo[ext=mp4][height>=720]/bestvideo[height<=720]+bestaudio[tbr>100]")
	if err != nil {
		t.Fatal(err)
	}
	selected, err := Select(selectorInfo(), selector)
	if err != nil {
		t.Fatal(err)
	}
	if len(selected) != 2 || selected[0].ID != "720" || selected[1].ID != "audio-high" {
		t.Fatalf("selected = %#v", selected)
	}
}

func TestSelectorBestWorstAndStringFilters(t *testing.T) {
	tests := []struct {
		expression string
		want       string
	}{
		{"bestvideo[vcodec^=av]", "360"},
		{"worstvideo", "360"},
		{"bestaudio[format_id$=high]", "audio-high"},
		{"bestvideo[ext~=webm|mp4]", "720"},
	}
	for _, test := range tests {
		selector, err := ParseSelector(test.expression)
		if err != nil {
			t.Fatalf("ParseSelector(%q): %v", test.expression, err)
		}
		selected, err := Select(selectorInfo(), selector)
		if err != nil || selected[0].ID != test.want {
			t.Fatalf("Select(%q) = %#v, %v", test.expression, selected, err)
		}
	}
}

func TestParseSelectorAtomAliasesAndCanonicalForm(t *testing.T) {
	tests := []struct {
		input, canonical string
		best             bool
		media            AtomMedia
		star             bool
		index            int
	}{
		{"b", "best", true, AtomMediaCombined, false, 1},
		{"w", "worst", false, AtomMediaCombined, false, 1},
		{"bv", "bestvideo", true, AtomMediaVideo, false, 1},
		{"wv", "worstvideo", false, AtomMediaVideo, false, 1},
		{"ba", "bestaudio", true, AtomMediaAudio, false, 1},
		{"wa", "worstaudio", false, AtomMediaAudio, false, 1},
		{"best", "best", true, AtomMediaCombined, false, 1},
		{"worstvideo", "worstvideo", false, AtomMediaVideo, false, 1},
		{"b*", "best*", true, AtomMediaCombined, true, 1},
		{"best*", "best*", true, AtomMediaCombined, true, 1},
		{"bv*", "bestvideo*", true, AtomMediaVideo, true, 1},
		{"ba*", "bestaudio*", true, AtomMediaAudio, true, 1},
		{"wv*", "worstvideo*", false, AtomMediaVideo, true, 1},
		{"wa*", "worstaudio*", false, AtomMediaAudio, true, 1},
		{"b.1", "best", true, AtomMediaCombined, false, 1},
		{"best.2", "best.2", true, AtomMediaCombined, false, 2},
		{"wv*.3", "worstvideo*.3", false, AtomMediaVideo, true, 3},
		{"bestaudio.10", "bestaudio.10", true, AtomMediaAudio, false, 10},
		{"w.1000", "worst.1000", false, AtomMediaCombined, false, 1000},
	}
	for _, test := range tests {
		selector, err := ParseSelector(test.input)
		if err != nil {
			t.Fatalf("ParseSelector(%q): %v", test.input, err)
		}
		term := selector.Alternatives[0].Terms[0]
		if term.Name != test.canonical {
			t.Fatalf("%q Name = %q, want %q", test.input, term.Name, test.canonical)
		}
		atom := term.Atom
		if !atom.OK || atom.Best != test.best || atom.Media != test.media || atom.Star != test.star || atom.Index != test.index {
			t.Fatalf("%q atom = %#v", test.input, atom)
		}
	}
}

func TestSelectorAtomFamiliesAndIndexing(t *testing.T) {
	info := combinedInfo()
	tests := []struct {
		expression string
		want       string
	}{
		{"b", "720c"},
		{"best", "720c"},
		{"w", "240c"},
		{"worst", "240c"},
		{"b.1", "720c"},
		{"best.2", "360c"},
		{"w.1", "240c"},
		{"worst.2", "360c"},
		{"b*", "720c"},
		{"w*", "240c"},
	}
	for _, test := range tests {
		selector, err := ParseSelector(test.expression)
		if err != nil {
			t.Fatal(err)
		}
		selected, err := Select(info, selector)
		if err != nil || len(selected) != 1 || selected[0].ID != test.want {
			t.Fatalf("Select(%q) = %#v, %v", test.expression, selected, err)
		}
	}
}

func TestSelectorTypedStarMaySelectCombined(t *testing.T) {
	// Exclusive tracks are mid-quality. Higher and lower combined formats exist so
	// typed stars can prefer a combined candidate while non-star typed atoms stay
	// exclusive, and worst typed-stars can select the weaker combined format.
	info := value.NewInfo(value.NewObject(value.Field{Key: "formats", Value: value.List(
		value.ObjectValue(value.NewObject(
			value.Field{Key: "format_id", Value: value.String("combined-hi")},
			value.Field{Key: "url", Value: value.String("https://example.invalid/combined-hi")},
			value.Field{Key: "vcodec", Value: value.String("avc1")},
			value.Field{Key: "acodec", Value: value.String("aac")},
			value.Field{Key: "height", Value: value.Int(1080)},
			value.Field{Key: "abr", Value: value.Float(256)},
			value.Field{Key: "tbr", Value: value.Float(3000)},
		)),
		value.ObjectValue(value.NewObject(
			value.Field{Key: "format_id", Value: value.String("combined-lo")},
			value.Field{Key: "url", Value: value.String("https://example.invalid/combined-lo")},
			value.Field{Key: "vcodec", Value: value.String("avc1")},
			value.Field{Key: "acodec", Value: value.String("aac")},
			value.Field{Key: "height", Value: value.Int(120)},
			value.Field{Key: "abr", Value: value.Float(16)},
			value.Field{Key: "tbr", Value: value.Float(80)},
		)),
		value.ObjectValue(value.NewObject(
			value.Field{Key: "format_id", Value: value.String("video")},
			value.Field{Key: "url", Value: value.String("https://example.invalid/video")},
			value.Field{Key: "vcodec", Value: value.String("avc1")},
			value.Field{Key: "acodec", Value: value.String("none")},
			value.Field{Key: "height", Value: value.Int(480)},
			value.Field{Key: "tbr", Value: value.Float(800)},
		)),
		value.ObjectValue(value.NewObject(
			value.Field{Key: "format_id", Value: value.String("audio")},
			value.Field{Key: "url", Value: value.String("https://example.invalid/audio")},
			value.Field{Key: "vcodec", Value: value.String("none")},
			value.Field{Key: "acodec", Value: value.String("aac")},
			value.Field{Key: "abr", Value: value.Float(128)},
			value.Field{Key: "tbr", Value: value.Float(128)},
		)),
	)}))
	tests := []struct {
		expression string
		want       string
	}{
		{"bv", "video"},
		{"bestvideo", "video"},
		{"bv*", "combined-hi"},
		{"bestvideo*", "combined-hi"},
		{"wv", "video"},
		{"worstvideo", "video"},
		{"wv*", "combined-lo"},
		{"worstvideo*", "combined-lo"},
		{"ba", "audio"},
		{"bestaudio", "audio"},
		{"ba*", "combined-hi"},
		{"bestaudio*", "combined-hi"},
		{"wa", "audio"},
		{"worstaudio", "audio"},
		{"wa*", "combined-lo"},
		{"worstaudio*", "combined-lo"},
	}
	for _, test := range tests {
		selector, err := ParseSelector(test.expression)
		if err != nil {
			t.Fatalf("ParseSelector(%q): %v", test.expression, err)
		}
		selected, err := Select(info, selector)
		if err != nil || len(selected) != 1 || selected[0].ID != test.want {
			t.Fatalf("Select(%q) = %#v, %v; want %q", test.expression, selected, err, test.want)
		}
	}
}

func TestSelectorPlainBestRejectsMixedAdaptiveWithoutCombined(t *testing.T) {
	selector, err := ParseSelector("best")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Select(selectorInfo(), selector); !errors.Is(err, ErrNoMatch) {
		t.Fatalf("mixed adaptive best error = %v, want ErrNoMatch", err)
	}
	// Filters that narrow a mixed adaptive set to one side must not unlock
	// incomplete fallback; incomplete_formats is fixed from the original set.
	filtered, err := ParseSelector("best[ext~=webm|mp4]")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Select(selectorInfo(), filtered); !errors.Is(err, ErrNoMatch) {
		t.Fatalf("mixed adaptive best with one-side filter = %v, want ErrNoMatch", err)
	}
	star, err := ParseSelector("b*")
	if err != nil {
		t.Fatal(err)
	}
	selected, err := Select(selectorInfo(), star)
	if err != nil || selected[0].ID != "720" {
		t.Fatalf("b* = %#v, %v", selected, err)
	}
}

func TestSelectorIncompleteFormatFallback(t *testing.T) {
	audioOnly := value.NewInfo(value.NewObject(value.Field{Key: "formats", Value: value.List(
		value.ObjectValue(value.NewObject(
			value.Field{Key: "format_id", Value: value.String("a1")},
			value.Field{Key: "url", Value: value.String("https://example.invalid/a1")},
			value.Field{Key: "vcodec", Value: value.String("none")},
			value.Field{Key: "acodec", Value: value.String("aac")},
			value.Field{Key: "ext", Value: value.String("m4a")},
			value.Field{Key: "tbr", Value: value.Float(64)},
		)),
		value.ObjectValue(value.NewObject(
			value.Field{Key: "format_id", Value: value.String("a2")},
			value.Field{Key: "url", Value: value.String("https://example.invalid/a2")},
			value.Field{Key: "vcodec", Value: value.String("none")},
			value.Field{Key: "acodec", Value: value.String("aac")},
			value.Field{Key: "ext", Value: value.String("m4a")},
			value.Field{Key: "tbr", Value: value.Float(128)},
		)),
	)}))
	selector, _ := ParseSelector("best")
	selected, err := Select(audioOnly, selector)
	if err != nil || selected[0].ID != "a2" {
		t.Fatalf("audio-only incomplete fallback = %#v, %v", selected, err)
	}
	filteredAudio, _ := ParseSelector("best[ext=m4a]")
	selected, err = Select(audioOnly, filteredAudio)
	if err != nil || selected[0].ID != "a2" {
		t.Fatalf("audio-only incomplete fallback with filter = %#v, %v", selected, err)
	}
	worstAudio, _ := ParseSelector("worst[tbr>50]")
	selected, err = Select(audioOnly, worstAudio)
	if err != nil || selected[0].ID != "a1" {
		t.Fatalf("audio-only worst fallback with filter = %#v, %v", selected, err)
	}

	videoOnly := value.NewInfo(value.NewObject(value.Field{Key: "formats", Value: value.List(
		value.ObjectValue(value.NewObject(
			value.Field{Key: "format_id", Value: value.String("v1")},
			value.Field{Key: "url", Value: value.String("https://example.invalid/v1")},
			value.Field{Key: "vcodec", Value: value.String("avc1")},
			value.Field{Key: "acodec", Value: value.String("none")},
			value.Field{Key: "ext", Value: value.String("mp4")},
			value.Field{Key: "height", Value: value.Int(360)},
		)),
		value.ObjectValue(value.NewObject(
			value.Field{Key: "format_id", Value: value.String("v2")},
			value.Field{Key: "url", Value: value.String("https://example.invalid/v2")},
			value.Field{Key: "vcodec", Value: value.String("avc1")},
			value.Field{Key: "acodec", Value: value.String("none")},
			value.Field{Key: "ext", Value: value.String("mp4")},
			value.Field{Key: "height", Value: value.Int(720)},
		)),
	)}))
	selected, err = Select(videoOnly, selector)
	if err != nil || selected[0].ID != "v2" {
		t.Fatalf("video-only incomplete fallback = %#v, %v", selected, err)
	}
	filteredVideo, _ := ParseSelector("best[height>=360]")
	selected, err = Select(videoOnly, filteredVideo)
	if err != nil || selected[0].ID != "v2" {
		t.Fatalf("video-only incomplete fallback with filter = %#v, %v", selected, err)
	}

	combined := value.NewInfo(value.NewObject(value.Field{Key: "formats", Value: value.List(
		value.ObjectValue(value.NewObject(
			value.Field{Key: "format_id", Value: value.String("combined")},
			value.Field{Key: "url", Value: value.String("https://example.invalid/combined")},
			value.Field{Key: "vcodec", Value: value.String("avc1")},
			value.Field{Key: "acodec", Value: value.String("aac")},
			value.Field{Key: "height", Value: value.Int(480)},
		)),
		value.ObjectValue(value.NewObject(
			value.Field{Key: "format_id", Value: value.String("video-only")},
			value.Field{Key: "url", Value: value.String("https://example.invalid/video-only")},
			value.Field{Key: "vcodec", Value: value.String("avc1")},
			value.Field{Key: "acodec", Value: value.String("none")},
			value.Field{Key: "height", Value: value.Int(1080)},
		)),
	)}))
	selected, err = Select(combined, selector)
	if err != nil || selected[0].ID != "combined" {
		t.Fatalf("combined format must match plain best = %#v, %v", selected, err)
	}
}

func TestSelectorFiltersApplyBeforeIndex(t *testing.T) {
	selector, err := ParseSelector("bestvideo.2[height<=720]")
	if err != nil {
		t.Fatal(err)
	}
	// Matching videos by height<=720 in best order: 720 then 360. .2 => 360.
	selected, err := Select(selectorInfo(), selector)
	if err != nil || selected[0].ID != "360" {
		t.Fatalf("filters-before-index = %#v, %v", selected, err)
	}
	unindexed, _ := ParseSelector("bestvideo[height<=720]")
	first, err := Select(selectorInfo(), unindexed)
	if err != nil || first[0].ID != "720" {
		t.Fatalf("unindexed = %#v, %v", first, err)
	}
	indexed1, _ := ParseSelector("bestvideo.1[height<=720]")
	same, err := Select(selectorInfo(), indexed1)
	if err != nil || same[0].ID != first[0].ID {
		t.Fatalf(".1 must equal unindexed: %#v vs %#v", same, first)
	}
}

func TestSelectorAtomFallbackMergeAndDirectDottedIDs(t *testing.T) {
	info := combinedInfo()
	formats, _ := info.Formats()
	formats = append(formats,
		value.ObjectValue(value.NewObject(
			value.Field{Key: "format_id", Value: value.String("18.1")},
			value.Field{Key: "url", Value: value.String("https://example.invalid/18.1")},
			value.Field{Key: "vcodec", Value: value.String("avc1")},
			value.Field{Key: "acodec", Value: value.String("aac")},
			value.Field{Key: "height", Value: value.Int(180)},
		)),
		value.ObjectValue(value.NewObject(
			value.Field{Key: "format_id", Value: value.String("b.mp4")},
			value.Field{Key: "url", Value: value.String("https://example.invalid/b.mp4")},
			value.Field{Key: "vcodec", Value: value.String("avc1")},
			value.Field{Key: "acodec", Value: value.String("aac")},
			value.Field{Key: "height", Value: value.Int(120)},
		)),
		value.ObjectValue(value.NewObject(
			value.Field{Key: "format_id", Value: value.String("best.mp4")},
			value.Field{Key: "url", Value: value.String("https://example.invalid/best.mp4")},
			value.Field{Key: "vcodec", Value: value.String("avc1")},
			value.Field{Key: "acodec", Value: value.String("aac")},
			value.Field{Key: "height", Value: value.Int(100)},
		)),
	)
	info = value.NewInfo(value.NewObject(value.Field{Key: "formats", Value: value.List(formats...)}))

	selector, err := ParseSelector("missing/b.2")
	if err != nil {
		t.Fatal(err)
	}
	selected, err := Select(info, selector)
	if err != nil || selected[0].ID != "360c" {
		t.Fatalf("fallback chain = %#v, %v", selected, err)
	}

	merge, err := ParseSelector("bv+ba")
	if err != nil {
		t.Fatal(err)
	}
	adaptive := selectorInfo()
	selected, err = Select(adaptive, merge)
	if err != nil || len(selected) != 2 || selected[0].ID != "720" || selected[1].ID != "audio-high" {
		t.Fatalf("merge = %#v, %v", selected, err)
	}

	for _, id := range []string{"18.1", "b.mp4", "best.mp4", "wav", "bestx", "bestvideox"} {
		direct, err := ParseSelector(id)
		if err != nil {
			t.Fatal(err)
		}
		if direct.Alternatives[0].Terms[0].Atom.OK {
			t.Fatalf("%q parsed as atom", id)
		}
		if id == "18.1" || id == "b.mp4" || id == "best.mp4" {
			selected, err = Select(info, direct)
			if err != nil || selected[0].ID != id {
				t.Fatalf("direct %q = %#v, %v", id, selected, err)
			}
		}
	}
}

func TestSelectorAtomMalformedSyntaxSpans(t *testing.T) {
	tests := []struct {
		input      string
		start, end int
		substr     string
	}{
		{"best.0", 4, 6, "leading zeros"},
		{"best.", 4, 5, "missing atom index"},
		{"bv.-1", 2, 5, "sign"},
		{"w.1001", 1, 6, "maximum"},
		{"best.10000", 4, 10, "digits"},
		{"bv*x", 3, 4, "suffix"},
		{"bestvideo.2x", 11, 12, "suffix"},
		{"b*.", 2, 3, "missing"},
		{"best.+2", 4, 5, "missing"}, // '+' is merge; "best." is rejected before the merge term
	}
	for _, test := range tests {
		_, err := ParseSelector(test.input)
		var syntax *SyntaxError
		if !errors.As(err, &syntax) {
			t.Fatalf("ParseSelector(%q) = %v", test.input, err)
		}
		if syntax.Start != test.start || syntax.End != test.end || !strings.Contains(syntax.Message, test.substr) {
			t.Fatalf("%q span=%d:%d message=%q want %d:%d containing %q", test.input, syntax.Start, syntax.End, syntax.Message, test.start, test.end, test.substr)
		}
	}
}

func TestSelectorIndexedNoMatchAndMissingCodec(t *testing.T) {
	selector, err := ParseSelector("best.4")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Select(combinedInfo(), selector); !errors.Is(err, ErrNoMatch) {
		t.Fatalf("best.4 error = %v", err)
	}
	neither := value.NewInfo(value.NewObject(value.Field{Key: "formats", Value: value.List(
		value.ObjectValue(value.NewObject(
			value.Field{Key: "format_id", Value: value.String("storyboard")},
			value.Field{Key: "url", Value: value.String("https://example.invalid/sb")},
			value.Field{Key: "vcodec", Value: value.String("none")},
			value.Field{Key: "acodec", Value: value.String("none")},
		)),
	)}))
	star, _ := ParseSelector("b*")
	if _, err := Select(neither, star); !errors.Is(err, ErrNoMatch) {
		t.Fatalf("neither-side b* = %v", err)
	}
	missingKeys := value.NewInfo(value.NewObject(value.Field{Key: "formats", Value: value.List(
		value.ObjectValue(value.NewObject(
			value.Field{Key: "format_id", Value: value.String("progressive")},
			value.Field{Key: "url", Value: value.String("https://example.invalid/p")},
			value.Field{Key: "height", Value: value.Int(720)},
		)),
	)}))
	best, _ := ParseSelector("best")
	selected, err := Select(missingKeys, best)
	if err != nil || selected[0].ID != "progressive" {
		t.Fatalf("missing codec keys best = %#v, %v", selected, err)
	}
}

func TestSelectorAtomOptionsOrderingTiesDRM(t *testing.T) {
	format := func(id, ext string, height int64, drm bool) value.Value {
		fields := []value.Field{
			{Key: "format_id", Value: value.String(id)},
			{Key: "url", Value: value.String("https://example.invalid/" + id)},
			{Key: "ext", Value: value.String(ext)},
			{Key: "height", Value: value.Int(height)},
			{Key: "tbr", Value: value.Int(100)},
			{Key: "vcodec", Value: value.String("avc")},
			{Key: "acodec", Value: value.String("aac")},
		}
		if drm {
			fields = append(fields, value.Field{Key: "has_drm", Value: value.Bool(true)})
		}
		return value.ObjectValue(value.NewObject(fields...))
	}
	info := value.NewInfo(value.NewObject(value.Field{Key: "formats", Value: value.List(
		format("mp4", "mp4", 720, false),
		format("webm", "webm", 720, false),
		format("drm", "mp4", 1080, true),
	)}))
	selector, _ := ParseSelector("b.2")
	selected, err := SelectWithOptions(info, selector, Options{PreferExtensions: []string{"webm"}})
	if err != nil || selected[0].ID != "mp4" {
		t.Fatalf("b.2 with ext preference = %#v, %v", selected, err)
	}
	fields, err := ParseSortFields([]string{"height"})
	if err != nil {
		t.Fatal(err)
	}
	selected, err = SelectWithOptions(info, selector, Options{Sort: fields})
	if err != nil || selected[0].ID != "webm" {
		t.Fatalf("sorted b.2 = %#v, %v", selected, err)
	}
	best, _ := ParseSelector("best")
	selected, err = SelectWithOptions(info, best, Options{})
	if err != nil || selected[0].ID != "mp4" && selected[0].ID != "webm" {
		t.Fatalf("DRM filtered best = %#v, %v", selected, err)
	}
	if selected[0].ID == "drm" {
		t.Fatal("DRM format selected")
	}
}

func TestSelectorAtomNoMutationDeterministicAndConcurrent(t *testing.T) {
	info := combinedInfo()
	formats, _ := info.Formats()
	object, _ := formats[0].Object()
	object.Set("marker", value.String("keep"))
	selector, _ := ParseSelector("best.2/worstvideo+bestaudio")
	var first []Selection
	for i := 0; i < 5; i++ {
		selected, err := Select(info, selector)
		if err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			first = selected
		} else if selected[0].ID != first[0].ID {
			t.Fatalf("non-deterministic: %#v vs %#v", selected, first)
		}
	}
	if marker, _ := object.Lookup("marker").StringValue(); marker != "keep" {
		t.Fatalf("mutated extractor metadata: %q", marker)
	}
	var wg sync.WaitGroup
	errs := make(chan error, 32)
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			selected, err := Select(info, selector)
			if err != nil {
				errs <- err
				return
			}
			if selected[0].ID != first[0].ID {
				errs <- errors.New("race result mismatch")
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
}

func TestSelectorAtomSABRAndHeaderPropagation(t *testing.T) {
	info := sabrInfo(sabrVideoFormat("137"), sabrAudioFormat())
	selector, err := ParseSelector("bv+ba")
	if err != nil {
		t.Fatal(err)
	}
	selected, err := Select(info, selector)
	if err != nil || len(selected) != 2 || !selected[0].YouTubeSABR || selected[0].Headers.Get("Referer") == "" {
		t.Fatalf("SABR selection = %#v, %v", selected, err)
	}
	selected[0].Headers.Set("Referer", "mutated")
	if selected[1].Headers.Get("Referer") != "https://www.youtube.com/watch?v=fixture0001" {
		t.Fatalf("header alias: %#v", selected)
	}
}

func TestDefaultPrefersAdaptivePairThenCombined(t *testing.T) {
	combined := value.ObjectValue(value.NewObject(
		value.Field{Key: "format_id", Value: value.String("combined")},
		value.Field{Key: "url", Value: value.String("https://example.invalid/combined")},
		value.Field{Key: "ext", Value: value.String("mp4")},
		value.Field{Key: "vcodec", Value: value.String("avc1")},
		value.Field{Key: "acodec", Value: value.String("aac")},
		value.Field{Key: "height", Value: value.Int(360)},
	))
	adaptive := selectorInfo()
	formats, _ := adaptive.Formats()
	for _, index := range []int{1, 3} {
		object, _ := formats[index].Object()
		object.Set("_youtube_post_live", value.Bool(true))
		object.Set("_youtube_live_from_start", value.Bool(true))
		object.Set("_youtube_itag", value.Int(137+int64(index)))
		object.Set("_youtube_client", value.String("WEB"))
		object.Set("_youtube_source_url", value.String("https://www.youtube.com/watch?v=fixture0001"))
		object.Set("target_duration", value.Float(5))
		object.Set("live_start_timestamp", value.Int(1234))
	}
	info := value.NewInfo(value.NewObject(value.Field{Key: "formats", Value: value.List(append([]value.Value{combined}, formats...)...)}))
	selected, err := Default(info, Options{})
	if err != nil || len(selected) != 2 || selected[0].ID != "720" || selected[1].ID != "audio-high" {
		t.Fatalf("Default() = %#v, %v", selected, err)
	}
	for _, selection := range selected {
		if !selection.YouTubePostLive || !selection.YouTubeLiveFromStart ||
			selection.YouTubeItag == 0 || selection.YouTubeClient != "WEB" ||
			selection.YouTubeSourceURL == "" || selection.TargetDuration != 5 || selection.LiveStartTimestamp != 1234 {
			t.Fatalf("post-live metadata dropped: %#v", selected)
		}
	}

	onlyCombined := value.NewInfo(value.NewObject(value.Field{Key: "formats", Value: value.List(combined)}))
	selected, err = Default(onlyCombined, Options{})
	if err != nil || len(selected) != 1 || selected[0].ID != "combined" {
		t.Fatalf("combined Default() = %#v, %v", selected, err)
	}
}

func TestDefaultInfersAdaptiveKindsFromExplicitAbsentCodecSide(t *testing.T) {
	video := value.ObjectValue(value.NewObject(
		value.Field{Key: "format_id", Value: value.String("video")},
		value.Field{Key: "url", Value: value.String("https://example.invalid/video")},
		value.Field{Key: "acodec", Value: value.String("none")},
		value.Field{Key: "height", Value: value.Int(1080)},
	))
	audio := value.ObjectValue(value.NewObject(
		value.Field{Key: "format_id", Value: value.String("audio")},
		value.Field{Key: "url", Value: value.String("https://example.invalid/audio")},
		value.Field{Key: "vcodec", Value: value.String("none")},
	))
	info := value.NewInfo(value.NewObject(value.Field{Key: "formats", Value: value.List(video, audio)}))
	selected, err := Default(info, Options{})
	if err != nil || len(selected) != 2 || selected[0].ID != "video" || selected[1].ID != "audio" {
		t.Fatalf("Default() = %#v, %v", selected, err)
	}
}

func TestPreferenceRanksDefaultsButNotExplicitFormatIDs(t *testing.T) {
	info := value.NewInfo(value.NewObject(value.Field{Key: "formats", Value: value.List(
		value.ObjectValue(value.NewObject(
			value.Field{Key: "format_id", Value: value.String("finite")},
			value.Field{Key: "url", Value: value.String("https://example.invalid/finite")},
			value.Field{Key: "height", Value: value.Int(720)},
			value.Field{Key: "vcodec", Value: value.String("avc")},
			value.Field{Key: "acodec", Value: value.String("none")},
		)),
		value.ObjectValue(value.NewObject(
			value.Field{Key: "format_id", Value: value.String("18")},
			value.Field{Key: "url", Value: value.String("https://example.invalid/incomplete")},
			value.Field{Key: "height", Value: value.Int(2160)},
			value.Field{Key: "vcodec", Value: value.String("avc")},
			value.Field{Key: "acodec", Value: value.String("none")},
			value.Field{Key: "preference", Value: value.Int(-10)},
		)),
	)}))
	defaultSelection, err := Select(info, Selector{Alternatives: []Choice{{Terms: []Term{{Name: "bestvideo"}}}}})
	if err != nil || len(defaultSelection) != 1 || defaultSelection[0].ID != "finite" {
		t.Fatalf("default selection = %#v, %v", defaultSelection, err)
	}
	sortFields, err := ParseSortFields([]string{"height"})
	if err != nil {
		t.Fatal(err)
	}
	sorted, err := SelectWithOptions(
		info,
		Selector{Alternatives: []Choice{{Terms: []Term{{Name: "bestvideo"}}}}},
		Options{Sort: sortFields},
	)
	if err != nil || len(sorted) != 1 || sorted[0].ID != "finite" {
		t.Fatalf("height-sorted selection = %#v, %v", sorted, err)
	}
	explicit, err := Select(info, Selector{Alternatives: []Choice{{Terms: []Term{{Name: "18"}}}}})
	if err != nil || len(explicit) != 1 || explicit[0].ID != "18" {
		t.Fatalf("explicit selection = %#v, %v", explicit, err)
	}
}

func TestSelectorRejectsInvalidSyntaxAndNoMatch(t *testing.T) {
	for _, input := range []string{"", "?unknown", "best[height]", "best[height>10", "best+"} {
		if _, err := ParseSelector(input); !errors.Is(err, ErrInvalidSelector) {
			t.Fatalf("ParseSelector(%q) error = %v", input, err)
		}
	}
	selector, _ := ParseSelector("bestvideo[height>9000]")
	if _, err := Select(selectorInfo(), selector); !errors.Is(err, ErrNoMatch) {
		t.Fatalf("Select() error = %v", err)
	}
}

func TestSelectorSyntaxErrorReportsSourceSpan(t *testing.T) {
	_, err := ParseSelector("bestvideo+?unknown")
	var syntaxError *SyntaxError
	if !errors.As(err, &syntaxError) {
		t.Fatalf("ParseSelector() error = %v", err)
	}
	if syntaxError.Start != 10 || syntaxError.End != 18 {
		t.Fatalf("syntax span = %d:%d, want 10:18", syntaxError.Start, syntaxError.End)
	}

	_, err = ParseSelector("best[height]")
	if !errors.As(err, &syntaxError) || syntaxError.Start != 5 || syntaxError.End != 11 {
		t.Fatalf("filter syntax error = %#v, %v", syntaxError, err)
	}
}

func TestSelectorDirectIDAllAndPreferences(t *testing.T) {
	info := selectorInfo()
	selector, err := ParseSelector("720+bestaudio/all[ext=m4a]")
	if err != nil {
		t.Fatal(err)
	}
	selected, err := SelectWithOptions(info, selector, Options{PreferExtensions: []string{"webm"}})
	if err != nil || len(selected) != 2 || selected[0].ID != "720" || selected[1].ID != "audio-high" {
		t.Fatalf("SelectWithOptions() = %#v, %v", selected, err)
	}
	all, err := ParseSelector("all[ext=m4a]")
	if err != nil {
		t.Fatal(err)
	}
	selected, err = Select(info, all)
	if err != nil || len(selected) != 2 || selected[0].ID != "audio-low" {
		t.Fatalf("all = %#v, %v", selected, err)
	}
}

func TestSelectorDRMAndSortPolicy(t *testing.T) {
	formats := value.List(
		value.ObjectValue(value.NewObject(value.Field{Key: "format_id", Value: value.String("clear")}, value.Field{Key: "url", Value: value.String("https://example.invalid/clear")}, value.Field{Key: "height", Value: value.Int(720)}, value.Field{Key: "vcodec", Value: value.String("avc")}, value.Field{Key: "acodec", Value: value.String("aac")})),
		value.ObjectValue(value.NewObject(value.Field{Key: "format_id", Value: value.String("drm")}, value.Field{Key: "url", Value: value.String("https://example.invalid/drm")}, value.Field{Key: "height", Value: value.Int(1080)}, value.Field{Key: "has_drm", Value: value.Bool(true)}, value.Field{Key: "vcodec", Value: value.String("avc")}, value.Field{Key: "acodec", Value: value.String("aac")})),
	)
	info := value.NewInfo(value.NewObject(value.Field{Key: "formats", Value: formats}))
	selector, _ := ParseSelector("best")
	selected, err := SelectWithOptions(info, selector, Options{})
	if err != nil || selected[0].ID != "clear" {
		t.Fatalf("DRM default = %#v, %v", selected, err)
	}
	fields, err := ParseSortFields([]string{"height~800"})
	if err != nil {
		t.Fatal(err)
	}
	selected, err = SelectWithOptions(info, selector, Options{AllowDRM: true, Sort: fields})
	if err != nil || selected[0].ID != "clear" { // 1080 and 720 tie in distance; source order is stable
		t.Fatalf("policy = %#v, %v", selected, err)
	}
}

func TestSortFieldsRejectBounds(t *testing.T) {
	for _, input := range []string{"", "+", "height~NaN", "height:Inf", "invalid-field"} {
		if _, err := ParseSortField(input); !errors.Is(err, ErrInvalidPreference) {
			t.Fatalf("ParseSortField(%q) = %v", input, err)
		}
	}
}

func TestSelectorRejectsBoundedInvalidRegexAndStructure(t *testing.T) {
	for _, input := range []string{"best[ext~=(]", strings.Repeat("best/", maxAlternatives), strings.Repeat("best+", maxMergeTerms)} {
		if _, err := ParseSelector(input); !errors.Is(err, ErrInvalidSelector) {
			t.Fatalf("ParseSelector(%q) = %v", input[:min(len(input), 20)], err)
		}
	}
}

func TestSelectorNumericAndMissingInequality(t *testing.T) {
	for _, expression := range []string{"bestvideo[height=720]", "bestvideo[missing!=x]"} {
		selector, err := ParseSelector(expression)
		if err != nil {
			t.Fatal(err)
		}
		selected, err := Select(selectorInfo(), selector)
		if err != nil || selected[0].ID != "720" {
			t.Fatalf("Select(%q) = %#v, %v", expression, selected, err)
		}
	}
	selector, _ := ParseSelector("bestvideo[height!=720]")
	selected, err := Select(selectorInfo(), selector)
	if err != nil || selected[0].ID != "360" {
		t.Fatalf("numeric inequality = %#v, %v", selected, err)
	}
}

func TestSelectorExtensionAndFreePreferencesBreakQualityTies(t *testing.T) {
	format := func(id, ext string, height int64) value.Value {
		return value.ObjectValue(value.NewObject(value.Field{Key: "format_id", Value: value.String(id)}, value.Field{Key: "url", Value: value.String("https://example.invalid/" + id)}, value.Field{Key: "ext", Value: value.String(ext)}, value.Field{Key: "height", Value: value.Int(height)}, value.Field{Key: "tbr", Value: value.Int(100)}, value.Field{Key: "vcodec", Value: value.String("avc")}, value.Field{Key: "acodec", Value: value.String("aac")}))
	}
	info := value.NewInfo(value.NewObject(value.Field{Key: "formats", Value: value.List(format("mp4", "mp4", 720), format("webm", "webm", 720), format("higher", "mp4", 1080))}))
	selector, _ := ParseSelector("best")
	selected, err := SelectWithOptions(info, selector, Options{PreferExtensions: []string{"webm"}})
	if err != nil || selected[0].ID != "higher" {
		t.Fatalf("quality must precede extension: %#v, %v", selected, err)
	}
	tied := value.NewInfo(value.NewObject(value.Field{Key: "formats", Value: value.List(format("mp4", "mp4", 720), format("webm", "webm", 720))}))
	selected, err = SelectWithOptions(tied, selector, Options{PreferExtensions: []string{"webm"}})
	if err != nil || selected[0].ID != "webm" {
		t.Fatalf("extension preference: %#v, %v", selected, err)
	}
	selected, err = SelectWithOptions(tied, selector, Options{PreferFreeFormats: true})
	if err != nil || selected[0].ID != "webm" {
		t.Fatalf("free preference: %#v, %v", selected, err)
	}
}

func TestSelectorMergesGlobalAndPerFormatHeaders(t *testing.T) {
	info := value.NewInfo(value.NewObject(
		value.Field{Key: "http_headers", Value: value.ObjectValue(value.NewObject(
			value.Field{Key: "Referer", Value: value.String("https://page.example/video")},
			value.Field{Key: "User-Agent", Value: value.String("global-agent")},
		))},
		value.Field{Key: "formats", Value: value.List(
			value.ObjectValue(value.NewObject(value.Field{Key: "format_id", Value: value.String("video")}, value.Field{Key: "url", Value: value.String("https://cdn.example/video")}, value.Field{Key: "height", Value: value.Int(720)}, value.Field{Key: "vcodec", Value: value.String("avc")}, value.Field{Key: "acodec", Value: value.String("none")}, value.Field{Key: "http_headers", Value: value.ObjectValue(value.NewObject(value.Field{Key: "User-Agent", Value: value.String("video-agent")}))})),
			value.ObjectValue(value.NewObject(value.Field{Key: "format_id", Value: value.String("audio")}, value.Field{Key: "url", Value: value.String("https://cdn.example/audio")}, value.Field{Key: "vcodec", Value: value.String("none")}, value.Field{Key: "acodec", Value: value.String("aac")}, value.Field{Key: "http_headers", Value: value.ObjectValue(value.NewObject(value.Field{Key: "X-Audio", Value: value.String("1")}))})),
		)},
	))
	selector, err := ParseSelector("video+audio")
	if err != nil {
		t.Fatal(err)
	}
	selected, err := Select(info, selector)
	if err != nil {
		t.Fatal(err)
	}
	if len(selected) != 2 || selected[0].Headers.Get("Referer") != "https://page.example/video" || selected[0].Headers.Get("User-Agent") != "video-agent" || selected[1].Headers.Get("User-Agent") != "global-agent" || selected[1].Headers.Get("X-Audio") != "1" {
		t.Fatalf("headers = %#v", selected)
	}
	selected[0].Headers.Set("Referer", "mutated")
	if selected[1].Headers.Get("Referer") != "https://page.example/video" {
		t.Fatalf("headers alias across selections: %#v", selected)
	}
}

func TestSelectorRejectsMalformedSelectedHeaders(t *testing.T) {
	info := value.NewInfo(value.NewObject(value.Field{Key: "formats", Value: value.List(value.ObjectValue(value.NewObject(value.Field{Key: "format_id", Value: value.String("1")}, value.Field{Key: "url", Value: value.String("https://cdn.example/1")}, value.Field{Key: "http_headers", Value: value.ObjectValue(value.NewObject(value.Field{Key: "X-Test", Value: value.String("bad\r\nvalue")}))})))}))
	selector, _ := ParseSelector("1")
	if _, err := Select(info, selector); !errors.Is(err, ErrInvalidHeaders) {
		t.Fatalf("Select() = %v", err)
	}
}

func FuzzParseSelector(f *testing.F) {
	for _, seed := range []string{
		"bestvideo[height<=1080]+bestaudio/best",
		"best[ext=mp4]",
		"worst",
		"b", "w", "bv", "ba", "wv", "wa",
		"b*", "bv*", "ba*", "w*",
		"best.2", "worst.3", "bv*.2",
		"best.0", "best.", "b.-1", "w.1001",
		"18.1", "b.mp4", "bestvideo.2x",
		"bv+ba/b", "missing/wa.2",
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, input string) {
		selector, err := ParseSelector(input)
		if err != nil {
			var syntax *SyntaxError
			if errors.As(err, &syntax) {
				if syntax.Start < 0 || syntax.End < syntax.Start || syntax.End > len(input)+16 {
					t.Fatalf("bad syntax span for %q: %#v", input, syntax)
				}
			}
			return
		}
		if len(selector.Alternatives) > maxAlternatives {
			t.Fatalf("too many alternatives: %d", len(selector.Alternatives))
		}
		for _, alternative := range selector.Alternatives {
			if len(alternative.Terms) > maxMergeTerms {
				t.Fatalf("too many merge terms: %d", len(alternative.Terms))
			}
			for _, term := range alternative.Terms {
				if len(term.Filters) > maxTermFilters {
					t.Fatalf("too many filters: %d", len(term.Filters))
				}
				if term.Atom.OK {
					if term.Atom.Index < 1 || term.Atom.Index > maxAtomIndex {
						t.Fatalf("atom index out of bounds: %#v", term.Atom)
					}
					if term.Name != term.Atom.Canonical() {
						t.Fatalf("canonical mismatch: name=%q atom=%#v", term.Name, term.Atom)
					}
				}
			}
		}
		_, _ = Select(selectorInfo(), selector)
		_, _ = Select(combinedInfo(), selector)
	})
}

func FuzzSelectAtoms(f *testing.F) {
	for _, seed := range []string{"best", "b*", "bv+ba", "best.2", "worst.1", "wa*/b", "all"} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, expression string) {
		selector, err := ParseSelector(expression)
		if err != nil {
			return
		}
		info := combinedInfo()
		first, err1 := Select(info, selector)
		second, err2 := Select(info, selector)
		if (err1 == nil) != (err2 == nil) {
			t.Fatalf("non-deterministic errors: %v vs %v", err1, err2)
		}
		if err1 != nil {
			return
		}
		if len(first) != len(second) {
			t.Fatalf("non-deterministic lengths")
		}
		formats, _ := info.Formats()
		ids := map[string]struct{}{}
		for _, item := range formats {
			object, _ := item.Object()
			id, _ := object.Lookup("format_id").StringValue()
			ids[id] = struct{}{}
		}
		for index := range first {
			if first[index].ID != second[index].ID {
				t.Fatalf("non-deterministic IDs")
			}
			if _, ok := ids[first[index].ID]; !ok {
				t.Fatalf("selected unknown id %q", first[index].ID)
			}
		}
	})
}
