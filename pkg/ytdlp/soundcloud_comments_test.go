package ytdlp

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/tejasa97/youtube_dlp/internal/network"
)

type soundCloudProductRoundTripper func(*http.Request) (*http.Response, error)

func (roundTrip soundCloudProductRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	return roundTrip(request)
}

func TestProductSoundCloudCommentOptionsPropagateAndEnrich(t *testing.T) {
	t.Parallel()
	transport := newSoundCloudEmbedProductTransport(t)
	request := Request{
		URL:          "https://soundcloud.com/fixture-artist/synthetic-signal",
		SkipDownload: true,
		SoundCloudComments: SoundCloudCommentOptions{
			Enabled: true, Sort: "oldest", MaxComments: 1,
		},
	}
	compatibility, err := prepareCompatibility(request)
	if err != nil {
		t.Fatal(err)
	}
	nativeTransport, err := network.New(network.Config{
		RoundTripper: soundCloudProductRoundTripper(func(request *http.Request) (*http.Response, error) {
			return transport.Do(request.Context(), request)
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	operation := &operation{
		client: NewClient(), request: request,
		registry: productRegistry(), transport: nativeTransport,
		compatibility: compatibility,
	}
	result, err := operation.process(context.Background(), request.URL, "", nil, make(map[string]bool), 0)
	if err != nil {
		t.Fatal(err)
	}
	var metadata map[string]any
	if err := json.Unmarshal(result.InfoJSON, &metadata); err != nil {
		t.Fatal(err)
	}
	comments, ok := metadata["comments"].([]any)
	if !ok || len(comments) != 1 || metadata["comment_count"] != float64(1) {
		t.Fatalf("metadata = %#v", metadata)
	}
	transport.mu.Lock()
	defer transport.mu.Unlock()
	var commentRequest string
	for _, raw := range transport.requests {
		if strings.Contains(raw, "/tracks/4242/comments") {
			commentRequest = raw
		}
	}
	if commentRequest == "" || !strings.Contains(commentRequest, "sort=oldest") ||
		!strings.Contains(commentRequest, "limit=20") || strings.Contains(commentRequest, "offset=20") {
		t.Fatalf("comment request = %q; all=%v", commentRequest, transport.requests)
	}
}
