package format

import (
	"errors"
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
	for _, input := range []string{"", "unknown", "best[height]", "best[height>10", "best+"} {
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
	_, err := ParseSelector("bestvideo+unknown")
	var syntaxError *SyntaxError
	if !errors.As(err, &syntaxError) {
		t.Fatalf("ParseSelector() error = %v", err)
	}
	if syntaxError.Start != 10 || syntaxError.End != 17 {
		t.Fatalf("syntax span = %d:%d, want 10:17", syntaxError.Start, syntaxError.End)
	}

	_, err = ParseSelector("best[height]")
	if !errors.As(err, &syntaxError) || syntaxError.Start != 5 || syntaxError.End != 11 {
		t.Fatalf("filter syntax error = %#v, %v", syntaxError, err)
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
