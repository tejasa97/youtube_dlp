package engine

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/tejasa97/youtube_dlp/internal/downloader"
	mediaformat "github.com/tejasa97/youtube_dlp/internal/format"
	"github.com/tejasa97/youtube_dlp/internal/value"
)

func TestYouTubeDirectRefreshAllowsClientRotationForExactRepresentation(t *testing.T) {
	const source = "https://www.youtube.com/watch?v=fixture0001"
	original := mediaformat.Selection{
		ID: "299", URL: "https://old.googlevideo.com/videoplayback?sig=old", Ext: "mp4",
		Filesize: 1000, VCodec: "avc1.64002a", ACodec: "none", Width: 1920, Height: 1080, FPS: 60,
		YouTubeSourceURL: source, YouTubeItag: 299, YouTubeClient: "ANDROID_VR",
	}
	operation := &operation{youtubeDirectExtract: func(_ context.Context, gotSource string) (Extraction, error) {
		if gotSource != source {
			t.Fatalf("source = %q", gotSource)
		}
		return Extraction{Info: youtubeDirectRefreshInfo("https://fresh.googlevideo.com/videoplayback?sig=fresh", 1000)}, nil
	}}
	refresh := operation.youtubeDirectRefresh(original)
	if refresh == nil {
		t.Fatal("refresh callback is nil")
	}
	result, err := refresh(context.Background(), downloader.RefreshRequest{StatusCode: http.StatusForbidden, Offset: 400, Total: 1000})
	if err != nil {
		t.Fatal(err)
	}
	if result.URL != "https://fresh.googlevideo.com/videoplayback?sig=fresh" || result.ExpectedBytes != 1000 {
		t.Fatalf("refresh result = %#v", result)
	}
}

func TestYouTubeDirectRefreshRejectsRepresentationMismatch(t *testing.T) {
	const source = "https://www.youtube.com/watch?v=fixture0001"
	original := mediaformat.Selection{
		ID: "299", URL: "https://old.googlevideo.com/videoplayback?sig=old", Ext: "mp4",
		Filesize: 1000, VCodec: "avc1.64002a", ACodec: "none", Width: 1920, Height: 1080, FPS: 60,
		YouTubeSourceURL: source, YouTubeItag: 299,
	}
	operation := &operation{youtubeDirectExtract: func(context.Context, string) (Extraction, error) {
		return Extraction{Info: youtubeDirectRefreshInfo("https://fresh.googlevideo.com/videoplayback?sig=fresh", 999)}, nil
	}}
	_, err := operation.youtubeDirectRefresh(original)(context.Background(), downloader.RefreshRequest{StatusCode: http.StatusForbidden})
	if !errors.Is(err, errYouTubeDirectRefreshRejected) {
		t.Fatalf("error = %v", err)
	}
}

func youtubeDirectRefreshInfo(rawURL string, size int64) value.Info {
	format := value.NewObject(
		value.Field{Key: "format_id", Value: value.String("299")},
		value.Field{Key: "url", Value: value.String(rawURL)},
		value.Field{Key: "ext", Value: value.String("mp4")},
		value.Field{Key: "filesize", Value: value.Int(size)},
		value.Field{Key: "vcodec", Value: value.String("avc1.64002a")},
		value.Field{Key: "acodec", Value: value.String("none")},
		value.Field{Key: "width", Value: value.Int(1920)},
		value.Field{Key: "height", Value: value.Int(1080)},
		value.Field{Key: "fps", Value: value.Int(60)},
	)
	return value.NewInfo(value.NewObject(value.Field{Key: "formats", Value: value.List(value.ObjectValue(format))}))
}
