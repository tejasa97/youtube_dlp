package engine

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/tejasa97/youtube_dlp/internal/extractor"
)

type soundCloudEmbedProductTransport struct {
	t        *testing.T
	fixtures map[string][]byte
	mu       sync.Mutex
	requests []string
}

func newSoundCloudEmbedProductTransport(t *testing.T) *soundCloudEmbedProductTransport {
	t.Helper()
	transport := &soundCloudEmbedProductTransport{t: t, fixtures: make(map[string][]byte)}
	for _, name := range []string{"home.html", "client.js", "track.json", "progressive.json", "hls.json", "comments_page1.json", "comments_page2.json"} {
		data, err := os.ReadFile(filepath.Join("..", "conformance", "extractors", "soundcloud", name))
		if err != nil {
			t.Fatal(err)
		}
		transport.fixtures[name] = data
	}
	return transport
}

func (transport *soundCloudEmbedProductTransport) ReadPage(context.Context, string) ([]byte, http.Header, error) {
	return nil, nil, errors.New("unexpected page request")
}

func (transport *soundCloudEmbedProductTransport) DoWithoutCredentialsNoRedirect(
	ctx context.Context,
	request *http.Request,
) (*http.Response, error) {
	for _, header := range []string{"Authorization", "Cookie", "Proxy-Authorization"} {
		if request.Header.Get(header) != "" {
			transport.t.Fatalf("isolated request leaked %s", header)
		}
	}
	return transport.Do(ctx, request)
}

func (transport *soundCloudEmbedProductTransport) Do(ctx context.Context, request *http.Request) (*http.Response, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	transport.mu.Lock()
	transport.requests = append(transport.requests, request.URL.String())
	transport.mu.Unlock()
	var fixture string
	switch {
	case request.URL.Hostname() == "soundcloud.com" && request.URL.Path == "/":
		fixture = "home.html"
	case request.URL.Hostname() == "a-v2.sndcdn.com" && request.URL.Path == "/client.js":
		fixture = "client.js"
	case request.URL.Hostname() == "api-v2.soundcloud.com" && request.URL.Path == "/resolve":
		if got := request.URL.Query().Get("url"); got != "https://soundcloud.com/fixture-artist/synthetic-signal" {
			transport.t.Fatalf("resolve URL = %q", got)
		}
		fixture = "track.json"
	case request.URL.Hostname() == "api-v2.soundcloud.com" && request.URL.Path == "/media/4242/progressive":
		fixture = "progressive.json"
	case request.URL.Hostname() == "api-v2.soundcloud.com" && request.URL.Path == "/media/4242/hls":
		fixture = "hls.json"
	case request.URL.Hostname() == "api-v2.soundcloud.com" && request.URL.Path == "/tracks/4242/comments":
		fixture = "comments_page1.json"
		if request.URL.Query().Get("offset") == "20" {
			fixture = "comments_page2.json"
		}
	case request.Method == http.MethodHead && request.URL.Hostname() == "i1.sndcdn.com" &&
		request.URL.Path == "/artworks-fixture-original.jpg":
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(bytes.NewReader(nil)),
			Request:    request,
		}, nil
	default:
		transport.t.Fatalf("unexpected request: %s", request.URL)
	}
	const clientID = "0123456789abcdef0123456789abcdef"
	if request.URL.Hostname() == "api-v2.soundcloud.com" && request.URL.Query().Get("client_id") != clientID {
		transport.t.Fatalf("missing client ID: %s", request.URL)
	}
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     make(http.Header),
		Body:       io.NopCloser(bytes.NewReader(transport.fixtures[fixture])),
		Request:    request,
	}, nil
}

func TestProductRegistryReentersSoundCloudEmbedIntoMedia(t *testing.T) {
	t.Parallel()
	const playerURL = "https://w.soundcloud.com/player/?url=https%3A%2F%2Fsoundcloud.com%2Ffixture-artist%2Fsynthetic-signal"
	registry := productRegistry()
	transport := newSoundCloudEmbedProductTransport(t)

	first, firstName, err := registry.Extract(context.Background(), extractor.Request{URL: playerURL, Transport: transport})
	if err != nil || firstName != "soundcloud_embed" || !first.IsURL() || first.Redirect == nil {
		t.Fatalf("first extraction = %#v, %q, %v", first, firstName, err)
	}
	secondExtractor, err := registry.SelectFor(first.Redirect.URL, first.Redirect.ExtractorKey)
	if err != nil || secondExtractor.Name() != "soundcloud" {
		t.Fatalf("re-entry selection = %v, %v", secondExtractor, err)
	}
	second, err := secondExtractor.Extract(context.Background(), extractor.Request{
		URL: first.Redirect.URL, Transport: transport,
	})
	if err != nil || second.IsURL() || second.IsPlaylist() {
		t.Fatalf("second extraction = %#v, %v", second, err)
	}
	if id, _ := second.Info.Lookup("id").StringValue(); id != "4242" {
		t.Fatalf("media ID = %q", id)
	}
	if formats, ok := second.Info.Formats(); !ok || len(formats) != 2 {
		t.Fatalf("formats = %d, %v", len(formats), ok)
	}
	transport.mu.Lock()
	defer transport.mu.Unlock()
	if len(transport.requests) != 6 {
		t.Fatalf("downstream requests = %v", transport.requests)
	}
	if transport.requests[5] != "https://i1.sndcdn.com/artworks-fixture-original.jpg" {
		t.Fatalf("thumbnail probe missing: %v", transport.requests)
	}
}
