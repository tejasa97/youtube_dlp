package ytdlp

import (
	"context"
	"testing"
)

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
