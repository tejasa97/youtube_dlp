package ytdlp

import (
	"context"
	"errors"
	"net/url"
	"testing"
	"time"

	"github.com/ytdlp-go/ytdlp/internal/extractor"
	mediaformat "github.com/ytdlp-go/ytdlp/internal/format"
	"github.com/ytdlp-go/ytdlp/internal/protocol/youtubelive"
	"github.com/ytdlp-go/ytdlp/internal/value"
)

type liveOptionCaptureExtractor struct{ liveFromStart bool }

func (capture *liveOptionCaptureExtractor) Name() string           { return "live-option-capture" }
func (capture *liveOptionCaptureExtractor) Suitable(*url.URL) bool { return true }
func (capture *liveOptionCaptureExtractor) Extract(_ context.Context, request extractor.Request) (extractor.Extraction, error) {
	capture.liveFromStart = request.YouTubeLiveFromStart
	return extractor.Media(value.NewInfo(value.NewObject(
		value.Field{Key: "id", Value: value.String("live-option")},
		value.Field{Key: "title", Value: value.String("Live option")},
	))), nil
}

func TestYouTubeLiveFromStartPublicRequestReachesExtractor(t *testing.T) {
	capture := &liveOptionCaptureExtractor{}
	request := Request{
		URL: "https://fixture.invalid/live", SkipDownload: true, LiveFromStart: true,
	}
	compatibility, err := prepareCompatibility(request)
	if err != nil {
		t.Fatal(err)
	}
	operation := &operation{
		client: NewClient(), request: request,
		registry: extractor.NewRegistry(capture), compatibility: compatibility,
	}
	if _, err := operation.process(context.Background(), request.URL, "", nil, make(map[string]bool), 0); err != nil {
		t.Fatal(err)
	}
	if !capture.liveFromStart {
		t.Fatal("LiveFromStart did not reach extractor request")
	}
}

func TestYouTubeLiveRefreshCoordinatorCachesAndMatchesExactIdentity(t *testing.T) {
	coordinator := &youtubeLiveRefreshCoordinator{
		operation: &operation{}, cacheFor: time.Hour,
	}
	calls := 0
	coordinator.extract = func(context.Context, string) (extractor.Extraction, error) {
		calls++
		return extractor.Media(value.NewInfo(value.NewObject(
			value.Field{Key: "live_status", Value: value.String("is_live")},
			value.Field{Key: "http_headers", Value: value.ObjectValue(value.NewObject(
				value.Field{Key: "User-Agent", Value: value.String("fixture-agent")},
			))},
			value.Field{Key: "formats", Value: value.List(
				liveRefreshFormat(137, "WEB", "https://media.example/video?pot=rotated"),
				liveRefreshFormat(140, "WEB", "https://media.example/audio?pot=rotated"),
			)},
		))), nil
	}
	video, err := coordinator.callback(mediaformat.Selection{
		YouTubeItag: 137, YouTubeClient: "WEB", YouTubeSourceURL: "https://www.youtube.com/watch?v=fixture0001",
	})(context.Background(), youtubelive.LiveRefreshRequest{})
	if err != nil || video.URL != "https://media.example/video?pot=rotated" ||
		video.Headers.Get("User-Agent") != "fixture-agent" || !video.StillLive {
		t.Fatalf("video refresh = %#v, %v", video, err)
	}
	audio, err := coordinator.callback(mediaformat.Selection{
		YouTubeItag: 140, YouTubeClient: "WEB", YouTubeSourceURL: "https://www.youtube.com/watch?v=fixture0001",
	})(context.Background(), youtubelive.LiveRefreshRequest{})
	if err != nil || audio.URL != "https://media.example/audio?pot=rotated" || calls != 1 {
		t.Fatalf("audio refresh = %#v, calls=%d, %v", audio, calls, err)
	}
	_, err = coordinator.callback(mediaformat.Selection{
		YouTubeItag: 137, YouTubeClient: "ANDROID", YouTubeSourceURL: "https://www.youtube.com/watch?v=fixture0001",
	})(context.Background(), youtubelive.LiveRefreshRequest{})
	if !errors.Is(err, youtubelive.ErrLiveRefreshFailed) {
		t.Fatalf("mismatched client error = %v", err)
	}
}

func TestYouTubeLiveRefreshCoordinatorReportsEndedStatus(t *testing.T) {
	coordinator := &youtubeLiveRefreshCoordinator{
		operation: &operation{}, cacheFor: time.Hour,
	}
	coordinator.extract = func(context.Context, string) (extractor.Extraction, error) {
		return extractor.Media(value.NewInfo(value.NewObject(
			value.Field{Key: "live_status", Value: value.String("post_live")},
			value.Field{Key: "formats", Value: value.List(
				liveRefreshFormat(137, "WEB", "https://media.example/final?pot=rotated"),
			)},
		))), nil
	}
	result, err := coordinator.callback(mediaformat.Selection{
		YouTubeItag: 137, YouTubeClient: "WEB", YouTubeSourceURL: "https://www.youtube.com/watch?v=fixture0001",
	})(context.Background(), youtubelive.LiveRefreshRequest{})
	if err != nil || result.StillLive || result.URL == "" {
		t.Fatalf("ended refresh = %#v, %v", result, err)
	}
}

func TestYouTubeLiveRefreshCoordinatorEndsWithoutMatchingFinalFormat(t *testing.T) {
	coordinator := &youtubeLiveRefreshCoordinator{
		operation: &operation{}, cacheFor: time.Hour,
	}
	coordinator.extract = func(context.Context, string) (extractor.Extraction, error) {
		return extractor.Media(value.NewInfo(value.NewObject(
			value.Field{Key: "live_status", Value: value.String("not_live")},
		))), nil
	}
	result, err := coordinator.callback(mediaformat.Selection{
		YouTubeItag: 137, YouTubeClient: "WEB", YouTubeSourceURL: "https://www.youtube.com/watch?v=fixture0001",
	})(context.Background(), youtubelive.LiveRefreshRequest{})
	if err != nil || result.StillLive || result.URL != "" {
		t.Fatalf("ended missing-format refresh = %#v, %v", result, err)
	}
}

func TestYouTubeLivePublicBoundsFailAtRequestValidation(t *testing.T) {
	for _, options := range []DownloaderOptions{
		{LivePollInterval: -1},
		{LivePollInterval: time.Hour + 1},
		{LiveRefreshInterval: -1},
		{LiveRefreshInterval: 24*time.Hour + 1},
		{LiveMaxPolls: -1},
		{LiveMaxPolls: 100_001},
		{LiveMaxNoProgressPolls: -1},
		{LiveMaxNoProgressPolls: 10_001},
	} {
		err := validateRequestOptions(Request{Downloader: options})
		if !errors.Is(err, errInvalidRequestOptions) {
			t.Fatalf("options=%+v error=%v", options, err)
		}
		if got := categorized("validate live bounds", err); !IsCategory(got, ErrorInvalidInput) {
			t.Fatalf("options=%+v categorized=%v", options, got)
		}
	}
}

func liveRefreshFormat(itag int64, client, rawURL string) value.Value {
	return value.ObjectValue(value.NewObject(
		value.Field{Key: "_youtube_itag", Value: value.Int(itag)},
		value.Field{Key: "_youtube_client", Value: value.String(client)},
		value.Field{Key: "url", Value: value.String(rawURL)},
	))
}
