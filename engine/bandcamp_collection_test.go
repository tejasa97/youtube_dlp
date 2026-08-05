package engine

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/tejasa97/youtube_dlp/internal/extractor"
	"github.com/tejasa97/youtube_dlp/internal/network"
)

type bandcampWeeklyProductRoundTripper struct {
	mu          sync.Mutex
	mediaBody   []byte
	mediaStatus int
	requests    map[string][]http.Header
}

func (transport *bandcampWeeklyProductRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	transport.mu.Lock()
	if transport.requests == nil {
		transport.requests = make(map[string][]http.Header)
	}
	key := request.Method + " " + request.URL.Host + request.URL.Path
	transport.requests[key] = append(transport.requests[key], request.Header.Clone())
	transport.mu.Unlock()

	switch {
	case request.Method == http.MethodPost && request.URL.Host == "bandcamp.com" && request.URL.Path == "/api/player/2/player_data_web":
		body, err := json.Marshal(map[string]any{
			"tracklist": map[string]any{
				"title":    "Magic Moments",
				"subtitle": "Bandcamp Weekly",
				"date":     float64(1491264000),
				"imageId":  "9982549",
				"compiledTrack": map[string]any{
					"streamUrl": "https://stream.bcbits.com/media/weekly.mp3?enc=mp3-128&sig=fixture-secret",
					"duration":  1.0,
				},
			},
		})
		if err != nil {
			return nil, err
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(string(body))),
			Request:    request,
		}, nil
	case request.Method == http.MethodGet && request.URL.Host == "stream.bcbits.com" && request.URL.Path == "/media/weekly.mp3":
		status := transport.mediaStatus
		if status == 0 {
			status = http.StatusOK
		}
		body := transport.mediaBody
		if body == nil {
			body = []byte("weekly-audio-bytes")
		}
		return &http.Response{
			StatusCode: status,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(string(body))),
			Request:    request,
		}, nil
	default:
		return nil, errors.New("unexpected Bandcamp weekly request: " + request.URL.String())
	}
}

func TestProductRegistryRoutesBandcampCollection(t *testing.T) {
	registry := productRegistry()
	for _, test := range []struct{ rawURL, want string }{
		{"https://fixture.bandcamp.com", "bandcamp_user"},
		{"https://fixture.bandcamp.com/music", "bandcamp_user"},
		{"https://bandcamp.com/radio?show=224", "bandcamp_weekly"},
		{"https://fixture.bandcamp.com/track/example", "bandcamp"},
	} {
		selected, err := registry.Select(test.rawURL)
		if err != nil || selected.Name() != test.want {
			t.Fatalf("Select(%q) = %v, %v; want %q", test.rawURL, selected, err, test.want)
		}
	}
}

func TestProductBandcampUserReentersTrackExtractor(t *testing.T) {
	transport := newBandcampProductTransport(t)
	registry := productRegistry()
	playlist, name, err := registry.Extract(context.Background(), extractor.Request{
		URL: "https://fixture.bandcamp.com", Transport: transport,
	})
	if err != nil || name != "bandcamp_user" || !playlist.IsPlaylist() {
		t.Fatalf("playlist=%#v name=%q err=%v", playlist, name, err)
	}
	entries, err := extractor.CollectEntries(context.Background(), playlist.Entries, 5)
	if err != nil || len(entries) == 0 {
		t.Fatalf("entries=%d err=%v", len(entries), err)
	}
	child := entries[0]
	child.URL = "https://fixture.bandcamp.com/track/reentry-track"
	selected, err := registry.SelectFor(child.URL, child.ExtractorKey)
	if err != nil || selected.Name() != "bandcamp" {
		t.Fatalf("child extractor=%v err=%v", selected, err)
	}
	media, err := selected.Extract(context.Background(), extractor.Request{URL: child.URL, Transport: transport})
	if err != nil || media.IsPlaylist() || media.IsURL() {
		t.Fatalf("media=%#v err=%v", media, err)
	}
	if id, _ := media.Info.ID(); id != "7" {
		t.Fatalf("track id=%q", id)
	}
}

func TestProductBandcampWeeklyDownloadCredentialIsolation(t *testing.T) {
	for _, test := range []struct {
		name         string
		mediaStatus  int
		wantCategory ErrorCategory
	}{
		{name: "success"},
		{name: "media-failure", mediaStatus: http.StatusInternalServerError, wantCategory: ErrorNetwork},
	} {
		t.Run(test.name, func(t *testing.T) {
			roundTripper := &bandcampWeeklyProductRoundTripper{mediaStatus: test.mediaStatus}
			transport, err := network.New(network.Config{
				RoundTripper: roundTripper,
				DefaultHeaders: http.Header{
					"Authorization":       {"Bearer ambient-secret"},
					"Cookie":              {"session=ambient-secret"},
					"Proxy-Authorization": {"Basic ambient-secret"},
				},
			})
			if err != nil {
				t.Fatal(err)
			}
			defer transport.CloseIdleConnections()

			root := t.TempDir()
			request := Request{
				URL:            "https://bandcamp.com/radio?show=224",
				OutputDir:      root,
				OutputTemplate: "%(id)s.%(ext)s",
				Overwrite:      true,
			}
			compatibility, err := prepareCompatibility(request)
			if err != nil {
				t.Fatal(err)
			}
			rootExtractor := ""
			operation := &operation{
				client: newBroadTestClient(), request: request, transport: transport,
				registry: productRuntime(), compatibility: compatibility,
				rootExtractor: &rootExtractor,
			}
			result, runErr := operation.process(context.Background(), request.URL, "", nil, make(map[string]bool), 0)
			if test.wantCategory == "" {
				if runErr != nil {
					t.Fatal(runErr)
				}
				if !result.Downloaded || result.Filename == "" {
					t.Fatalf("result=%+v", result)
				}
				data, err := os.ReadFile(result.Filename)
				if err != nil || string(data) != "weekly-audio-bytes" {
					t.Fatalf("download=%q err=%v", data, err)
				}
				if rootExtractor != "bandcamp_weekly" {
					t.Fatalf("root extractor=%q", rootExtractor)
				}
			} else {
				if runErr == nil || !IsCategory(runErr, test.wantCategory) {
					t.Fatalf("error=%v want category %s", runErr, test.wantCategory)
				}
				entries, err := os.ReadDir(root)
				if err != nil {
					t.Fatal(err)
				}
				if len(entries) != 0 {
					t.Fatalf("artifacts remain after media failure: %v", entries)
				}
			}

			roundTripper.mu.Lock()
			defer roundTripper.mu.Unlock()
			mediaHeaders := roundTripper.requests["GET stream.bcbits.com/media/weekly.mp3"]
			if len(mediaHeaders) == 0 {
				t.Fatal("missing media request")
			}
			for _, header := range mediaHeaders {
				for _, name := range []string{"Authorization", "Cookie", "Proxy-Authorization"} {
					if got := header.Get(name); got != "" {
						t.Fatalf("media request leaked %s: %q", name, got)
					}
				}
			}
		})
	}
}
