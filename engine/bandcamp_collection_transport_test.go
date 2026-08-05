package engine

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"testing"
)

type bandcampProductTransport struct {
	t        *testing.T
	pages    map[string][]byte
	weekly   []byte
	requests []string
}

func newBandcampProductTransport(t *testing.T) *bandcampProductTransport {
	t.Helper()
	transport := &bandcampProductTransport{t: t, pages: make(map[string][]byte)}
	root := filepath.Join("..", "conformance", "extractors")
	for _, item := range []struct{ dir, name string }{
		{"public/bandcamp", "success.html"},
		{"bandcamp", "user_type1.html"},
		{"bandcamp", "weekly_player_response.json"},
	} {
		body, err := os.ReadFile(filepath.Join(root, item.dir, item.name))
		if err != nil {
			t.Fatal(err)
		}
		switch item.name {
		case "weekly_player_response.json":
			transport.weekly = body
		default:
			if item.dir == "public/bandcamp" {
				transport.pages["https://fixture.bandcamp.com/track/reentry-track"] = body
			}
		}
	}
	transport.pages["https://fixture.bandcamp.com"] = mustReadBandcampProduct(t, "user_type1.html")
	return transport
}

func mustReadBandcampProduct(t *testing.T, name string) []byte {
	t.Helper()
	body, err := os.ReadFile(filepath.Join("..", "conformance", "extractors", "bandcamp", name))
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func (transport *bandcampProductTransport) ReadPage(_ context.Context, raw string) ([]byte, http.Header, error) {
	body, ok := transport.pages[raw]
	if !ok {
		transport.t.Fatalf("unexpected page %q", raw)
	}
	return body, make(http.Header), nil
}

func (transport *bandcampProductTransport) Do(ctx context.Context, request *http.Request) (*http.Response, error) {
	return transport.DoWithoutCredentialsNoRedirect(ctx, request)
}

func (transport *bandcampProductTransport) DoWithoutCredentialsNoRedirect(_ context.Context, request *http.Request) (*http.Response, error) {
	transport.requests = append(transport.requests, request.Method+" "+request.URL.String())
	switch {
	case request.Method == http.MethodGet && request.URL.String() == "https://fixture.bandcamp.com":
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(bytes.NewReader(transport.pages[request.URL.String()])),
			Request:    request,
		}, nil
	case request.Method == http.MethodPost && request.URL.String() == "https://bandcamp.com/api/player/2/player_data_web":
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(bytes.NewReader(transport.weekly)),
			Request:    request,
		}, nil
	default:
		transport.t.Fatalf("unexpected request %s %s", request.Method, request.URL)
		return nil, nil
	}
}
