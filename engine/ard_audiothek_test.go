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

type ardAudiothekProductRoundTripper struct {
	mu              sync.Mutex
	requests        map[string][]http.Header
	episodeFixture  []byte
	playlistFixture []byte
	mediaStatus     int
}

func (transport *ardAudiothekProductRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	transport.mu.Lock()
	if transport.requests == nil {
		transport.requests = make(map[string][]http.Header)
	}
	key := request.Method + " " + request.URL.Host + request.URL.Path
	transport.requests[key] = append(transport.requests[key], request.Header.Clone())
	transport.mu.Unlock()

	switch {
	case request.Method == http.MethodPost && request.URL.Host == "api.ardaudiothek.de" && request.URL.Path == "/graphql":
		body, err := io.ReadAll(request.Body)
		if err != nil {
			return nil, err
		}
		var envelope struct {
			Variables map[string]string `json:"variables"`
		}
		if err := json.Unmarshal(body, &envelope); err != nil {
			return nil, err
		}
		switch envelope.Variables["id"] {
		case "urn:ard:show:c405aa26d9a4060a":
			return productJSONResponse(transport.playlistFixture), nil
		case "urn:ard:episode:cafebabe00000001", "urn:ard:episode:eabead1add170e93":
			return productJSONResponse(transport.episodeFixture), nil
		default:
			return &http.Response{StatusCode: http.StatusNotFound, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("{}")), Request: request}, nil
		}
	case request.Method == http.MethodGet && request.URL.Host == "cdn.example.invalid":
		status := transport.mediaStatus
		if status == 0 {
			status = http.StatusOK
		}
		return &http.Response{
			StatusCode: status,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader("fixture-audio-bytes")),
			Request:    request,
		}, nil
	default:
		return nil, errors.New("unexpected ARD Audiothek request: " + request.URL.String())
	}
}

func productJSONResponse(body []byte) *http.Response {
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(string(body))),
	}
}

func readARDAudiothekFixture(t *testing.T, name string) []byte {
	t.Helper()
	body, err := os.ReadFile("../conformance/extractors/risk/ard_audiothek/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func newARDAudiothekProductRoundTripper(t *testing.T) *ardAudiothekProductRoundTripper {
	t.Helper()
	return &ardAudiothekProductRoundTripper{
		episodeFixture:  readARDAudiothekFixture(t, "episode.json"),
		playlistFixture: readARDAudiothekFixture(t, "playlist.json"),
	}
}

func TestProductRegistryRoutesARDAudiothekFamily(t *testing.T) {
	registry := productRegistry()
	for _, test := range []struct{ rawURL, want string }{
		{"https://www.ardmediathek.de/player/Y3JpZDovL2ZpeHR1cmU", "ard"},
		{"https://www.ardaudiothek.de/episode/urn:ard:episode:eabead1add170e93/", "ard_audiothek"},
		{"https://www.ardsounds.de/sendung/mia-insomnia/urn:ard:show:c405aa26d9a4060a/", "ard_audiothek_playlist"},
	} {
		selected, err := registry.Select(test.rawURL)
		if err != nil || selected.Name() != test.want {
			t.Fatalf("Select(%q) = %v, %v; want %q", test.rawURL, selected, err, test.want)
		}
	}
}

func TestProductARDAudiothekPlaylistReentersEpisodeExtractor(t *testing.T) {
	transport, err := network.New(network.Config{RoundTripper: newARDAudiothekProductRoundTripper(t)})
	if err != nil {
		t.Fatal(err)
	}
	defer transport.CloseIdleConnections()

	registry := productRegistry()
	playlist, name, err := registry.Extract(context.Background(), extractor.Request{
		URL:       "https://www.ardaudiothek.de/sendung/mia-insomnia/urn:ard:show:c405aa26d9a4060a/",
		Transport: transport,
	})
	if err != nil || name != "ard_audiothek_playlist" || !playlist.IsPlaylist() {
		t.Fatalf("playlist=%#v name=%q err=%v", playlist, name, err)
	}
	entries, err := extractor.CollectEntries(context.Background(), playlist.Entries, 5)
	if err != nil || len(entries) == 0 {
		t.Fatalf("entries=%d err=%v", len(entries), err)
	}
	child := entries[0]
	selected, err := registry.SelectFor(child.URL, child.ExtractorKey)
	if err != nil || selected.Name() != "ard_audiothek" {
		t.Fatalf("child extractor=%v err=%v", selected, err)
	}
	media, err := selected.Extract(context.Background(), extractor.Request{URL: child.URL, Transport: transport})
	if err != nil || media.IsPlaylist() || media.IsURL() {
		t.Fatalf("media=%#v err=%v", media, err)
	}
	if id, _ := media.Info.ID(); id != "urn:ard:episode:cafebabe00000001" {
		t.Fatalf("episode id=%q", id)
	}
}

func TestProductARDAudiothekDownloadCredentialIsolation(t *testing.T) {
	for _, test := range []struct {
		name         string
		mediaStatus  int
		wantCategory ErrorCategory
	}{
		{name: "success"},
		{name: "media-failure", mediaStatus: http.StatusInternalServerError, wantCategory: ErrorNetwork},
	} {
		t.Run(test.name, func(t *testing.T) {
			roundTripper := newARDAudiothekProductRoundTripper(t)
			roundTripper.mediaStatus = test.mediaStatus
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
				URL:            "https://www.ardaudiothek.de/episode/urn:ard:episode:eabead1add170e93/",
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
				client:        newBroadTestClient(),
				request:       request,
				transport:     transport,
				registry:      productRuntime(),
				compatibility: compatibility,
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
				if err != nil || string(data) != "fixture-audio-bytes" {
					t.Fatalf("download bytes=%q err=%v", data, err)
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
			var mediaHeaders []http.Header
			for key, headers := range roundTripper.requests {
				if strings.HasPrefix(key, "GET cdn.example.invalid/ard/fixture-episode") {
					mediaHeaders = append(mediaHeaders, headers...)
				}
			}
			if len(mediaHeaders) == 0 {
				t.Fatalf("missing media request: %#v", roundTripper.requests)
			}
			for _, headers := range mediaHeaders {
				for _, key := range []string{"Authorization", "Cookie", "Proxy-Authorization"} {
					if headers.Get(key) != "" {
						t.Fatalf("media download leaked %s: %#v", key, headers)
					}
				}
			}
			for _, headers := range roundTripper.requests["POST api.ardaudiothek.de/graphql"] {
				for _, key := range []string{"Authorization", "Cookie", "Proxy-Authorization"} {
					if headers.Get(key) != "" {
						t.Fatalf("graphql leaked %s: %#v", key, headers)
					}
				}
			}
		})
	}
}

func TestProductARDAudiothekGraphQLStripsAmbientCredentials(t *testing.T) {
	roundTripper := newARDAudiothekProductRoundTripper(t)
	transport, err := network.New(network.Config{
		RoundTripper: roundTripper,
		DefaultHeaders: http.Header{
			"Authorization": {"Bearer ambient-secret"},
			"Cookie":        {"session=ambient-secret"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer transport.CloseIdleConnections()

	_, _, err = productRegistry().Extract(context.Background(), extractor.Request{
		URL:       "https://www.ardaudiothek.de/episode/urn:ard:episode:eabead1add170e93/",
		Transport: transport,
	})
	if err != nil {
		t.Fatal(err)
	}
	roundTripper.mu.Lock()
	defer roundTripper.mu.Unlock()
	graphqlHeaders := roundTripper.requests["POST api.ardaudiothek.de/graphql"]
	if len(graphqlHeaders) != 1 {
		t.Fatalf("graphql requests=%d", len(graphqlHeaders))
	}
	for _, key := range []string{"Authorization", "Cookie", "Proxy-Authorization"} {
		if graphqlHeaders[0].Get(key) != "" {
			t.Fatalf("graphql leaked %s: %#v", key, graphqlHeaders[0])
		}
	}
}
