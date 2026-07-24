package format

import (
	"errors"
	"testing"

	"github.com/ytdlp-go/ytdlp/internal/value"
)

func sabrInfo(formats ...*value.Object) value.Info {
	values := make([]value.Value, len(formats))
	for index, object := range formats {
		values[index] = value.ObjectValue(object)
	}
	return value.NewInfo(value.NewObject(
		value.Field{Key: "http_headers", Value: value.ObjectValue(value.NewObject(
			value.Field{Key: "Referer", Value: value.String("https://www.youtube.com/watch?v=fixture0001")},
		))},
		value.Field{Key: "formats", Value: value.List(values...)},
	))
}

func sabrVideoFormat(id string) *value.Object {
	return value.NewObject(
		value.Field{Key: "format_id", Value: value.String(id)},
		value.Field{Key: "ext", Value: value.String("mp4")},
		value.Field{Key: "protocol", Value: value.String("youtube_sabr_ump")},
		value.Field{Key: "vcodec", Value: value.String("avc1")},
		value.Field{Key: "acodec", Value: value.String("none")},
		value.Field{Key: "height", Value: value.Int(1080)},
		value.Field{Key: "preference", Value: value.Int(-5)},
		value.Field{Key: "_youtube_sabr", Value: value.Bool(true)},
		value.Field{Key: "_youtube_sabr_track", Value: value.String("video")},
		value.Field{Key: "_youtube_sabr_itag", Value: value.Int(137)},
		value.Field{Key: "_youtube_sabr_server_url", Value: value.String("https://rr1---sn-fixture.googlevideo.com/videoplayback/sabr?sig=fixture")},
		value.Field{Key: "_youtube_sabr_ustreamer_config", Value: value.String("Zml4dHVyZS11c3RyZWFtZXI=")},
		value.Field{Key: "_youtube_sabr_client_id", Value: value.Int(1)},
		value.Field{Key: "_youtube_sabr_client_version", Value: value.String("2.fixture")},
		value.Field{Key: "_youtube_sabr_user_agent", Value: value.String("fixture-agent")},
		value.Field{Key: "_youtube_sabr_duration_sec", Value: value.Int(10)},
		value.Field{Key: "_youtube_sabr_video_id", Value: value.String("fixture0001")},
		value.Field{Key: "_youtube_client", Value: value.String("WEB")},
	)
}

func sabrAudioFormat() *value.Object {
	return value.NewObject(
		value.Field{Key: "format_id", Value: value.String("140")},
		value.Field{Key: "ext", Value: value.String("m4a")},
		value.Field{Key: "protocol", Value: value.String("youtube_sabr_ump")},
		value.Field{Key: "vcodec", Value: value.String("none")},
		value.Field{Key: "acodec", Value: value.String("mp4a")},
		value.Field{Key: "preference", Value: value.Int(-5)},
		value.Field{Key: "_youtube_sabr", Value: value.Bool(true)},
		value.Field{Key: "_youtube_sabr_track", Value: value.String("audio")},
		value.Field{Key: "_youtube_sabr_itag", Value: value.Int(140)},
		value.Field{Key: "_youtube_sabr_server_url", Value: value.String("https://rr1---sn-fixture.googlevideo.com/videoplayback/sabr?sig=fixture")},
		value.Field{Key: "_youtube_sabr_ustreamer_config", Value: value.String("Zml4dHVyZS11c3RyZWFtZXI=")},
		value.Field{Key: "_youtube_sabr_client_id", Value: value.Int(1)},
		value.Field{Key: "_youtube_sabr_client_version", Value: value.String("2.fixture")},
		value.Field{Key: "_youtube_sabr_user_agent", Value: value.String("fixture-agent")},
		value.Field{Key: "_youtube_sabr_duration_sec", Value: value.Int(10)},
		value.Field{Key: "_youtube_sabr_video_id", Value: value.String("fixture0001")},
		value.Field{Key: "_youtube_client", Value: value.String("WEB")},
	)
}

func TestBestSelectsSABRWithoutURL(t *testing.T) {
	selected, err := Best(sabrInfo(sabrVideoFormat("137")))
	if err != nil {
		t.Fatal(err)
	}
	if selected.URL != "" || !selected.YouTubeSABR || selected.YouTubeSABRServerURL == "" {
		t.Fatalf("selection=%#v", selected)
	}
	if selected.Headers.Get("Referer") == "" {
		t.Fatal("expected merged headers")
	}
}

func TestDefaultSelectsSABRVideoAndAudio(t *testing.T) {
	selected, err := Default(sabrInfo(sabrVideoFormat("137"), sabrAudioFormat()), Options{})
	if err != nil {
		t.Fatal(err)
	}
	if len(selected) != 2 {
		t.Fatalf("selected=%d", len(selected))
	}
	for _, item := range selected {
		if item.URL != "" || !item.YouTubeSABR || item.Headers.Get("Referer") == "" {
			t.Fatalf("selection=%#v", item)
		}
	}
}

func TestSelectExplicitSABRFormatID(t *testing.T) {
	selected, err := Select(sabrInfo(sabrVideoFormat("137"), sabrAudioFormat()), Selector{Alternatives: []Choice{{Terms: []Term{{Name: "140"}}}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(selected) != 1 || selected[0].ID != "140" || selected[0].URL != "" || !selected[0].YouTubeSABR {
		t.Fatalf("selection=%#v", selected)
	}
}

func TestSelectSABRFilterMatches(t *testing.T) {
	selected, err := Select(sabrInfo(sabrVideoFormat("137"), sabrAudioFormat()), Selector{Alternatives: []Choice{{Terms: []Term{{Name: "bestvideo", Filters: []Filter{{Field: "height", Operator: "=", Value: "1080"}}}}}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(selected) != 1 || selected[0].ID != "137" {
		t.Fatalf("selection=%#v", selected)
	}
}

func TestSelectSABRNoMatch(t *testing.T) {
	_, err := Select(sabrInfo(sabrVideoFormat("137")), Selector{Alternatives: []Choice{{Terms: []Term{{Name: "999"}}}}})
	if !errors.Is(err, ErrNoMatch) {
		t.Fatalf("err=%v", err)
	}
}
