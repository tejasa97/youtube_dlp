package ytdlp

import (
	"context"
	"net/url"
	"testing"

	"github.com/ytdlp-go/ytdlp/internal/extractor"
	"github.com/ytdlp-go/ytdlp/internal/network"
	"github.com/ytdlp-go/ytdlp/internal/value"
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

func TestPublicYouTubePOTProviderConfiguration(t *testing.T) {
	client := NewClient(WithYouTubePOTProviders(YouTubePOTConfig{
		Policy: YouTubePOTFetchAlways,
		Providers: []YouTubePOTProvider{YouTubePOTProviderFunc{ProviderName: "fixture", Function: func(context.Context, YouTubePOTRequest) (YouTubePOTResponse, error) {
			return YouTubePOTResponse{Token: "Zm9v"}, nil
		}}},
	}))
	if client.youtubePOTErr != nil || client.youtubePOT == nil {
		t.Fatalf("provider configuration = %v, %#v", client.youtubePOTErr, client.youtubePOT)
	}
	token, ok, err := client.youtubePOT.Resolve(context.Background(), YouTubePOTRequest{
		Context: YouTubePOTContextPlayer, Client: "ANDROID", VideoID: "fixture0001",
	}, true)
	if err != nil || !ok || token != "Zm9v" {
		t.Fatalf("resolve = %q %v %v", token, ok, err)
	}
}

func TestPublicYouTubePOTConfigurationFailsClosed(t *testing.T) {
	client := NewClient(WithYouTubePOTProviders(YouTubePOTConfig{
		Providers: []YouTubePOTProvider{YouTubePOTProviderFunc{ProviderName: "INVALID NAME"}},
	}))
	_, err := client.Run(context.Background(), Request{URL: "https://www.youtube.com/watch?v=fixture0001", SkipDownload: true})
	if !IsCategory(err, ErrorInvalidInput) {
		t.Fatalf("configuration error = %v", err)
	}
}

func TestPublicYouTubeCaptionOptionsReachExtractor(t *testing.T) {
	client := NewClient(WithYouTubePOTProviders(YouTubePOTConfig{Policy: YouTubePOTFetchNever}))
	transport, err := network.New(network.Config{})
	if err != nil {
		t.Fatal(err)
	}
	capture := &youtubeOptionCaptureExtractor{}
	operation := &operation{
		client:    client,
		request:   Request{SkipDownload: true, YouTubeTranslatedCaptions: true},
		transport: transport,
		registry:  extractor.NewRegistry(capture),
	}
	if _, err := operation.process(context.Background(), "https://example.test/video", "", nil, make(map[string]bool), 0); err != nil {
		t.Fatal(err)
	}
	if !capture.request.YouTubeTranslatedCaptions || capture.request.YouTubePOT == nil {
		t.Fatalf("extractor request = %#v", capture.request)
	}
}
