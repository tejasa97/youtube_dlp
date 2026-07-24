package format

import (
	"errors"
	"strings"
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
		{"best[ext~=webm|mp4]", "720"},
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
	f.Add("bestvideo[height<=1080]+bestaudio/best")
	f.Add("best[ext=mp4]")
	f.Add("worst")
	f.Fuzz(func(t *testing.T, input string) {
		selector, err := ParseSelector(input)
		if err != nil {
			return
		}
		_, _ = Select(selectorInfo(), selector)
	})
}
