package ytdlp

import (
	"context"
	"net/url"
	"testing"

	"github.com/tejasa97/ytdlp-go/engine"
	providerapi "github.com/tejasa97/ytdlp-go/engine/provider"
	"github.com/tejasa97/ytdlp-go/internal/extractor"
	"github.com/tejasa97/ytdlp-go/internal/value"
)

type youtubeOptionCaptureExtractor struct{ request extractor.Request }

func (*youtubeOptionCaptureExtractor) Name() string           { return "youtube-option-capture" }
func (*youtubeOptionCaptureExtractor) Suitable(*url.URL) bool { return true }
func (candidate *youtubeOptionCaptureExtractor) Extract(_ context.Context, request extractor.Request) (extractor.Extraction, error) {
	candidate.request = request
	info := value.NewInfo(value.NewObject(
		value.Field{Key: "id", Value: value.String("fixture0001")},
		value.Field{Key: "title", Value: value.String("fixture")},
		value.Field{Key: "formats", Value: value.List(value.ObjectValue(value.NewObject(
			value.Field{Key: "url", Value: value.String("https://media.example/video.mp4")},
			value.Field{Key: "ext", Value: value.String("mp4")},
		)))},
	))
	return extractor.Media(info), nil
}

func newYouTubeOptionCaptureClient(candidate *youtubeOptionCaptureExtractor, options ...Option) *Client {
	composition := engine.NewComposition[extractor.Request](
		func(engine.ClientProviderConfig) []providerapi.Provider[extractor.Request] {
			return []providerapi.Provider[extractor.Request]{candidate}
		},
		broadProviderRequest,
		engine.ProviderHooks{},
	)
	return engine.NewClient(composition, options...)
}

func TestPublicYouTubePOTProviderConfiguration(t *testing.T) {
	capture := &youtubeOptionCaptureExtractor{}
	client := newYouTubeOptionCaptureClient(capture, WithYouTubePOTProviders(YouTubePOTConfig{
		Policy: YouTubePOTFetchAlways,
		Providers: []YouTubePOTProvider{YouTubePOTProviderFunc{ProviderName: "fixture", Function: func(context.Context, YouTubePOTRequest) (YouTubePOTResponse, error) {
			return YouTubePOTResponse{Token: "Zm9v"}, nil
		}}},
	}))
	if _, err := client.Run(context.Background(), Request{URL: "https://example.test/video", SkipDownload: true}); err != nil {
		t.Fatal(err)
	}
	if capture.request.YouTubePOT == nil {
		t.Fatal("PO-token director did not reach the provider request")
	}
	token, ok, err := capture.request.YouTubePOT.Resolve(context.Background(), YouTubePOTRequest{
		Context: YouTubePOTContextPlayer, Client: "ANDROID", VideoID: "fixture0001",
	}, true)
	if err != nil || !ok || token != "Zm9v" {
		t.Fatalf("resolve = %q %v %v", token, ok, err)
	}
}

func TestPublicYouTubePOTConfigurationFailsClosed(t *testing.T) {
	client := newYouTubeOptionCaptureClient(&youtubeOptionCaptureExtractor{}, WithYouTubePOTProviders(YouTubePOTConfig{
		Providers: []YouTubePOTProvider{YouTubePOTProviderFunc{ProviderName: "INVALID NAME"}},
	}))
	_, err := client.Run(context.Background(), Request{URL: "https://www.youtube.com/watch?v=fixture0001", SkipDownload: true})
	if !IsCategory(err, ErrorInvalidInput) {
		t.Fatalf("configuration error = %v", err)
	}
}

func TestPublicYouTubeCaptionOptionsReachExtractor(t *testing.T) {
	capture := &youtubeOptionCaptureExtractor{}
	client := newYouTubeOptionCaptureClient(capture, WithYouTubePOTProviders(YouTubePOTConfig{Policy: YouTubePOTFetchNever}))
	if _, err := client.Run(context.Background(), Request{
		URL: "https://example.test/video", SkipDownload: true, YouTubeTranslatedCaptions: true,
	}); err != nil {
		t.Fatal(err)
	}
	if !capture.request.YouTubeTranslatedCaptions || capture.request.YouTubePOT == nil {
		t.Fatalf("extractor request = %#v", capture.request)
	}
}
