package format

import (
	"errors"
	"testing"

	"github.com/ytdlp-go/ytdlp/internal/value"
)

func TestBestSelectsFirstDownloadableFormat(t *testing.T) {
	info := value.NewInfo(value.NewObject(value.Field{Key: "formats", Value: value.List(
		value.String("invalid"),
		value.ObjectValue(value.NewObject(
			value.Field{Key: "format_id", Value: value.String("best")},
			value.Field{Key: "url", Value: value.String("https://example.invalid/media")},
			value.Field{Key: "ext", Value: value.String("mp4")},
			value.Field{Key: "_youtube_post_live", Value: value.Bool(true)},
			value.Field{Key: "target_duration", Value: value.Float(5)},
			value.Field{Key: "live_start_timestamp", Value: value.Int(1234)},
		)),
	)}))
	selected, err := Best(info)
	if err != nil {
		t.Fatal(err)
	}
	if selected.ID != "best" || selected.Ext != "mp4" || !selected.YouTubePostLive ||
		selected.TargetDuration != 5 || selected.LiveStartTimestamp != 1234 {
		t.Fatalf("selection = %#v", selected)
	}
}

func TestBestRejectsMissingFormats(t *testing.T) {
	if _, err := Best(value.NewInfo(nil)); !errors.Is(err, ErrNoFormats) {
		t.Fatalf("Best() error = %v", err)
	}
}

func TestBestMergesInfoAndFormatHTTPHeaders(t *testing.T) {
	info := value.NewInfo(value.NewObject(
		value.Field{Key: "http_headers", Value: value.ObjectValue(value.NewObject(
			value.Field{Key: "Referer", Value: value.String("https://page.example/video")},
			value.Field{Key: "User-Agent", Value: value.String("info-agent")},
		))},
		value.Field{Key: "formats", Value: value.List(value.ObjectValue(value.NewObject(
			value.Field{Key: "url", Value: value.String("https://cdn.example/media")},
			value.Field{Key: "http_headers", Value: value.ObjectValue(value.NewObject(
				value.Field{Key: "User-Agent", Value: value.String("format-agent")},
			))},
		)))},
	))
	selected, err := Best(info)
	if err != nil {
		t.Fatal(err)
	}
	if selected.Headers.Get("Referer") != "https://page.example/video" || selected.Headers.Get("User-Agent") != "format-agent" {
		t.Fatalf("headers = %v", selected.Headers)
	}
	selected.Headers.Set("Referer", "mutated")
	headers, _ := info.Lookup("http_headers").Object()
	if original, _ := headers.Lookup("Referer").StringValue(); original != "https://page.example/video" {
		t.Fatal("selection headers mutated metadata")
	}
}

func TestBestRejectsMalformedHTTPHeaders(t *testing.T) {
	info := value.NewInfo(value.NewObject(
		value.Field{Key: "http_headers", Value: value.ObjectValue(value.NewObject(value.Field{Key: "X-Test", Value: value.String("bad\r\nvalue")}))},
		value.Field{Key: "formats", Value: value.List(value.ObjectValue(value.NewObject(value.Field{Key: "url", Value: value.String("https://cdn.example/media")})))},
	))
	if _, err := Best(info); !errors.Is(err, ErrInvalidHeaders) {
		t.Fatalf("Best() error = %v", err)
	}
}
