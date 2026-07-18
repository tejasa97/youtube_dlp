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
