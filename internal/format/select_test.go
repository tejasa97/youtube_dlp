package format

import (
	"errors"
	"testing"

	"github.com/ytdlp-go/ytdlp/internal/value"
)

func TestBestSelectsFirstDownloadableFormat(t *testing.T) {
	info := value.NewInfo(value.NewObject(value.Field{Key: "formats", Value: value.List(
		value.ObjectValue(value.NewObject(
			value.Field{Key: "format_id", Value: value.String("best")},
			value.Field{Key: "url", Value: value.String("https://example.invalid/media")},
			value.Field{Key: "ext", Value: value.String("mp4")},
			value.Field{Key: "_youtube_post_live", Value: value.Bool(true)},
			value.Field{Key: "_youtube_live_from_start", Value: value.Bool(true)},
			value.Field{Key: "_youtube_itag", Value: value.Int(137)},
			value.Field{Key: "_youtube_client", Value: value.String("WEB")},
			value.Field{Key: "_youtube_source_url", Value: value.String("https://www.youtube.com/watch?v=fixture0001")},
			value.Field{Key: "target_duration", Value: value.Float(5)},
			value.Field{Key: "live_start_timestamp", Value: value.Int(1234)},
		)),
	)}))
	selected, err := Best(info)
	if err != nil {
		t.Fatal(err)
	}
	if selected.ID != "best" || selected.Ext != "mp4" || !selected.YouTubePostLive || !selected.YouTubeLiveFromStart ||
		selected.YouTubeItag != 137 || selected.YouTubeClient != "WEB" ||
		selected.YouTubeSourceURL != "https://www.youtube.com/watch?v=fixture0001" ||
		selected.TargetDuration != 5 || selected.LiveStartTimestamp != 1234 {
		t.Fatalf("selection = %#v", selected)
	}
}

func TestBestPropagatesCredentialIsolationHostPolicy(t *testing.T) {
	info := value.NewInfo(value.NewObject(value.Field{Key: "formats", Value: value.List(value.ObjectValue(value.NewObject(
		value.Field{Key: "format_id", Value: value.String("ted")},
		value.Field{Key: "url", Value: value.String("https://hls.ted.com/fixture/master.m3u8?sig=1")},
		value.Field{Key: "protocol", Value: value.String("m3u8_native")},
		value.Field{Key: "_credential_isolated", Value: value.Bool(true)},
		value.Field{Key: "_ted_host_policy", Value: value.String("ted")},
	)))}))
	selected, err := Best(info)
	if err != nil {
		t.Fatal(err)
	}
	if !selected.CredentialIsolated || selected.HostPolicy != "ted" {
		t.Fatalf("selection=%#v", selected)
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

func TestSelectRejectsMalformedAllowedHosts(t *testing.T) {
	for _, rawHost := range []string{"", "ttvnw.net/", "ttvnw..net", " ttvnw.net", "ttvnw.net:443", "127.0.0.1"} {
		t.Run(rawHost, func(t *testing.T) {
			formatObject := value.NewObject(
				value.Field{Key: "format_id", Value: value.String("1")},
				value.Field{Key: "url", Value: value.String("https://edge.ttvnw.net/media.m3u8")},
				value.Field{Key: "_allowed_hosts", Value: value.List(value.String(rawHost))},
			)
			info := value.NewInfo(value.NewObject(value.Field{Key: "formats", Value: value.List(value.ObjectValue(formatObject))}))
			selector, err := ParseSelector("1")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := Select(info, selector); !errors.Is(err, ErrInvalidFormats) {
				t.Fatalf("host %q error=%v", rawHost, err)
			}
		})
	}
}

func TestBestRejectsNonObjectMember(t *testing.T) {
	info := value.NewInfo(value.NewObject(value.Field{Key: "formats", Value: value.List(
		value.String("invalid"),
		value.ObjectValue(value.NewObject(value.Field{Key: "url", Value: value.String("https://example.invalid/media")})),
	)}))
	if _, err := Best(info); !errors.Is(err, ErrInvalidFormats) {
		t.Fatalf("Best() error = %v", err)
	}
}

func TestBestRejectsMissingFormatMember(t *testing.T) {
	info := value.NewInfo(value.NewObject(value.Field{Key: "formats", Value: value.List(
		value.Missing(),
		value.ObjectValue(value.NewObject(value.Field{Key: "url", Value: value.String("https://example.invalid/media")})),
	)}))
	if _, err := Best(info); !errors.Is(err, ErrInvalidFormats) {
		t.Fatalf("Best() error = %v", err)
	}
}
