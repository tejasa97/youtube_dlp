package ytdlp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"testing"

	"github.com/ytdlp-go/ytdlp/internal/network"
)

const youtubePlayerMetadataProductURL = "https://www.youtube.com/watch?v=fixture0002"

// youtubeMetadataRoundTripper serves the synthetic player-metadata watch page
// and rejects every other request, so the product test proves the complete
// extraction-to-InfoJSON pipeline against the checked-in fixture.
type youtubeMetadataRoundTripper struct {
	page []byte
}

func (transport *youtubeMetadataRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	if request.Method != http.MethodGet || request.URL.String() != youtubePlayerMetadataProductURL {
		return nil, errors.New("unexpected product request: " + request.URL.String())
	}
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": {"text/html"}},
		Body:       io.NopCloser(bytes.NewReader(transport.page)),
		Request:    request,
	}, nil
}

func TestProductYouTubePlayerMetadataJSONSurvival(t *testing.T) {
	page, err := os.ReadFile("../../conformance/extractors/youtube_player_metadata/watch.html")
	if err != nil {
		t.Fatal(err)
	}
	client := NewClient(withTransportFactory(func(config network.Config) (*network.Client, error) {
		config.RoundTripper = &youtubeMetadataRoundTripper{page: page}
		return network.New(config)
	}))
	result, err := client.Run(context.Background(), Request{URL: youtubePlayerMetadataProductURL, Simulate: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.InfoJSON) == 0 {
		t.Fatal("product run produced no InfoJSON")
	}
	var document map[string]any
	if err := json.Unmarshal(result.InfoJSON, &document); err != nil {
		t.Fatalf("InfoJSON decode: %v", err)
	}
	for _, key := range []string{"channel_url", "uploader_id", "uploader_url", "upload_date", "timestamp",
		"age_limit", "categories", "tags", "average_rating", "playable_in_embed", "media_type",
		"thumbnail", "thumbnails"} {
		if _, ok := document[key]; !ok {
			t.Fatalf("field %q did not survive the product JSON pipeline", key)
		}
	}
	formats, ok := document["formats"].([]any)
	if !ok || len(formats) != 2 {
		t.Fatalf("formats = %#v", document["formats"])
	}
	var videoFormat map[string]any
	for _, format := range formats {
		candidate := format.(map[string]any)
		if candidate["vcodec"] != "none" {
			videoFormat = candidate
		}
	}
	if videoFormat == nil {
		t.Fatalf("no video format in product output: %#v", formats)
	}
	ratio, ok := videoFormat["stretched_ratio"].(float64)
	if !ok || ratio < 1.33 || ratio > 1.34 {
		t.Fatalf("video stretched_ratio = %v, %v", ratio, ok)
	}
	thumbnails, ok := document["thumbnails"].([]any)
	if !ok || len(thumbnails) != 42 {
		t.Fatalf("thumbnails = %d entries", len(thumbnails))
	}
}
