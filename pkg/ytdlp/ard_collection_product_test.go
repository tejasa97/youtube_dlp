package ytdlp

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ytdlp-go/ytdlp/internal/network"
)

type ardCollectionProductRoundTripper struct {
	mu       sync.Mutex
	requests []*http.Request
	block    bool
	entered  chan struct{}
	once     sync.Once
}

func (transport *ardCollectionProductRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	transport.mu.Lock()
	transport.requests = append(transport.requests, request.Clone(request.Context()))
	transport.mu.Unlock()
	if transport.block && request.URL.Host == "api.ardmediathek.de" && transport.entered != nil {
		transport.once.Do(func() { close(transport.entered) })
		<-request.Context().Done()
		return nil, request.Context().Err()
	}

	if request.URL.Host == "api.ardmediathek.de" {
		switch request.URL.Path {
		case "/page-gateway/widgets/ard/asset/Y3JpZDovL2ZpeHR1cmU":
			if request.URL.Query().Get("pageSize") == "1" {
				return ardCollectionJSONResponse(`{"title":"Fixture Collection","synopsis":"Synthetic collection","teasers":[]}`), nil
			}
			if request.URL.Query().Get("pageNumber") == "0" && request.URL.Query().Get("pageSize") == "100" {
				return ardCollectionJSONResponse(`{"title":"Fixture Collection","synopsis":"Synthetic collection","teasers":[{"id":"asset-1","type":"video","longTitle":"Episode One","links":{"target":{"urlId":"EpisodeAsset1"}}}]}`), nil
			}
		case "/page-gateway/pages/ard/item/EpisodeAsset1":
			fixture, err := os.ReadFile(filepath.Join("..", "..", "conformance", "extractors", "risk", "ard", "item.json"))
			if err != nil {
				return nil, err
			}
			return ardCollectionJSONResponse(string(fixture)), nil
		}
	}
	if request.URL.Host == "media.example.test" && request.URL.Path == "/ard/video.mp4" {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader("ARD-COLLECTION-MEDIA")),
			Request:    request,
		}, nil
	}
	return nil, &url.Error{Op: request.Method, URL: request.URL.String(), Err: os.ErrNotExist}
}

func ardCollectionJSONResponse(body string) *http.Response {
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": {"application/json"}},
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

func newARDCollectionProductClient(t *testing.T, transport *ardCollectionProductRoundTripper) *Client {
	t.Helper()
	return NewClient(withTransportFactory(func(config network.Config) (*network.Client, error) {
		config.RoundTripper = transport
		config.DefaultHeaders = http.Header{
			"Authorization":       {"Bearer ambient-secret"},
			"Cookie":              {"session=ambient-secret"},
			"Proxy-Authorization": {"Basic ambient-secret"},
			"Referer":             {"https://ambient.example.invalid/page"},
		}
		return network.New(config)
	}))
}

func TestProductRegistryRoutesARDMediathekCollection(t *testing.T) {
	registry := productRegistry()
	for _, rawURL := range []string{
		"https://www.ardmediathek.de/sendung/title/Y3JpZDovL2ZpeHR1cmU",
		"https://www.ardmediathek.de/serie/title/staffel-1/Y3JpZDovL2ZpeHR1cmU/1/OV",
		"https://www.ardmediathek.de/sammlung/title/Y3JpZDovL2ZpeHR1cmU",
	} {
		selected, err := registry.Select(rawURL)
		if err != nil || selected.Name() != "ard_mediathek_collection" {
			t.Fatalf("Select(%q) = %v, %v", rawURL, selected, err)
		}
	}
	selected, err := registry.Select("https://www.ardmediathek.de/player/Y3JpZDovL2ZpeHR1cmU")
	if err != nil || selected.Name() != "ard" {
		t.Fatalf("item Select() = %v, %v", selected, err)
	}
}

func TestProductARDMediathekCollectionClientRunReentryAndIsolation(t *testing.T) {
	transport := &ardCollectionProductRoundTripper{}
	client := newARDCollectionProductClient(t, transport)
	for iteration := 0; iteration < 2; iteration++ {
		root := t.TempDir()
		result, err := client.Run(context.Background(), Request{
			URL:            "https://www.ardmediathek.de/sendung/title/Y3JpZDovL2ZpeHR1cmU",
			OutputDir:      root,
			OutputTemplate: "%(id)s.%(ext)s",
			Format:         "best[protocol=https]",
			Overwrite:      true,
			Playlist:       PlaylistOptions{Items: "1"},
		})
		if err != nil {
			t.Fatal(err)
		}
		if result.Extractor != "ard_mediathek_collection" || len(result.Entries) != 1 {
			t.Fatalf("iteration=%d result=%+v", iteration, result)
		}
		child := result.Entries[0]
		if child.Extractor != "ard" || !child.Downloaded || child.Filename == "" {
			t.Fatalf("iteration=%d child=%+v", iteration, child)
		}
		media, err := os.ReadFile(child.Filename)
		if err != nil || string(media) != "ARD-COLLECTION-MEDIA" {
			t.Fatalf("iteration=%d media=%q err=%v", iteration, media, err)
		}
	}

	transport.mu.Lock()
	defer transport.mu.Unlock()
	pageRuns := 0
	for _, request := range transport.requests {
		for _, header := range []string{"Authorization", "Cookie", "Proxy-Authorization", "Referer"} {
			if value := request.Header.Get(header); value != "" {
				t.Fatalf("request %s leaked %s=%q", request.URL, header, value)
			}
		}
		if request.URL.Host == "api.ardmediathek.de" && request.URL.Path == "/page-gateway/widgets/ard/asset/Y3JpZDovL2ZpeHR1cmU" && request.URL.Query().Get("pageSize") == "100" {
			pageRuns++
		}
	}
	if pageRuns != 2 {
		t.Fatalf("collection page runs=%d want 2", pageRuns)
	}
}

func TestProductARDMediathekCollectionCancellationLeavesNoArtifacts(t *testing.T) {
	transport := &ardCollectionProductRoundTripper{block: true, entered: make(chan struct{})}
	client := newARDCollectionProductClient(t, transport)
	root := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := client.Run(ctx, Request{
			URL:            "https://www.ardmediathek.de/sendung/title/Y3JpZDovL2ZpeHR1cmU",
			OutputDir:      root,
			OutputTemplate: "%(id)s.%(ext)s",
			Format:         "best[protocol=https]",
			Overwrite:      true,
		})
		done <- err
	}()
	select {
	case <-transport.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("collection API request was not entered")
	}
	cancel()
	select {
	case err := <-done:
		if err == nil || !IsCategory(err, ErrorCancelled) {
			t.Fatalf("cancellation error=%v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("cancellation did not finish")
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("artifacts remain after cancellation: %v", entries)
	}
}
