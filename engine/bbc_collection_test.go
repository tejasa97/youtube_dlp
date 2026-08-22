package engine

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/tejasa97/ytdlp-go/internal/extractor"
	mediaformat "github.com/tejasa97/ytdlp-go/internal/format"
	"github.com/tejasa97/ytdlp-go/internal/network"
)

type bbcProductFixtureRoundTripper struct {
	mu       sync.Mutex
	pages    map[string][]byte
	requests []string
}

func (transport *bbcProductFixtureRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	transport.mu.Lock()
	transport.requests = append(transport.requests, request.Method+" "+request.URL.String())
	body, ok := transport.pages[request.URL.String()]
	transport.mu.Unlock()
	if !ok {
		return &http.Response{StatusCode: http.StatusNotFound, Body: http.NoBody, Header: make(http.Header)}, nil
	}
	return &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader(string(body))),
		Header:     make(http.Header),
	}, nil
}

func readBBCProductFixture(t *testing.T, name string) []byte {
	if t != nil {
		t.Helper()
	}
	data, err := os.ReadFile(filepath.Join("..", "conformance", "extractors", "risk", "bbciplayer", name))
	if err != nil {
		if t != nil {
			t.Fatal(err)
		}
		panic(err)
	}
	return data
}

func TestProductRegistryRoutesBBCCollectionExtractors(t *testing.T) {
	registry := productRegistry()
	for _, test := range []struct {
		rawURL string
		want   string
	}{
		{"https://www.bbc.co.uk/programmes/articles/FixtureArticleId/title", "bbc_co_uk_article"},
		{"https://www.bbc.co.uk/programmes/p0000000/clips", "bbc_co_uk_playlist"},
		{"https://www.bbc.co.uk/iplayer/episodes/p0000000/fixture", "bbc_co_uk_iplayer_episodes"},
		{"https://www.bbc.co.uk/iplayer/group/p0000000", "bbc_co_uk_iplayer_group"},
		{"https://www.bbc.co.uk/iplayer/episode/p0000000/title", "bbciplayer"},
	} {
		selected, err := registry.Select(test.rawURL)
		if err != nil || selected.Name() != test.want {
			t.Fatalf("Select(%q) = %v, %v; want %q", test.rawURL, selected, err, test.want)
		}
	}
}

func TestProductBBCCollectionTransparentChildReentry(t *testing.T) {
	articleURL := "https://www.bbc.co.uk/programmes/articles/FixtureArticleId/title"
	roundTripper := &bbcProductFixtureRoundTripper{
		pages: map[string][]byte{articleURL: readBBCProductFixture(t, "article.html")},
	}
	netTransport, err := network.New(network.Config{RoundTripper: roundTripper})
	if err != nil {
		t.Fatal(err)
	}
	registry := productRegistry()
	selected, err := registry.Select(articleURL)
	if err != nil {
		t.Fatal(err)
	}
	result, err := selected.Extract(context.Background(), extractor.Request{URL: articleURL, Transport: netTransport})
	if err != nil {
		t.Fatal(err)
	}
	entry, ok, err := result.Entries.Iterator().Next(context.Background())
	if err != nil || !ok || !entry.Transparent || entry.ExtractorKey != "bbciplayer" {
		t.Fatalf("entry=%#v ok=%t err=%v", entry, ok, err)
	}
	child, err := registry.SelectFor(entry.URL, entry.ExtractorKey)
	if err != nil || child.Name() != "bbciplayer" {
		t.Fatalf("SelectFor(%q, %q) = %v, %v", entry.URL, entry.ExtractorKey, child, err)
	}
}

type bbcProductDownloadRoundTripper struct {
	mu           sync.Mutex
	mediaURL     string
	selectorFail bool
	requests     map[string][]http.Header
	targetCalls  int
}

func (transport *bbcProductDownloadRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	transport.mu.Lock()
	if transport.requests == nil {
		transport.requests = make(map[string][]http.Header)
	}
	key := request.URL.Host + request.URL.Path
	transport.requests[key] = append(transport.requests[key], request.Header.Clone())
	transport.mu.Unlock()

	if request.URL.String() == transport.mediaURL {
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader("bbc-direct-media")),
			Header:     make(http.Header),
			Request:    request,
		}, nil
	}

	status := http.StatusOK
	body := ""
	switch key {
	case "www.bbc.co.uk/programmes/articles/FixtureArticleId/title":
		body = string(readBBCProductFixture(nil, "article.html"))
	case "www.bbc.co.uk/programmes/p0000002":
		body = string(readBBCProductFixture(nil, "programme_p0000002.html"))
	case "open.live.bbc.co.uk/mediaselector/6/select/version/2.0/mediaset/iptv-all/vpid/p0000002":
		body = `{"result":"geolocation"}`
	case "open.live.bbc.co.uk/mediaselector/6/select/version/2.0/mediaset/pc/vpid/p0000002":
		if transport.selectorFail {
			body = `{"result":"selectionunavailable"}`
		} else {
			body = fmt.Sprintf(`{"media":[{"kind":"video","bitrate":"1800","encoding":"h264","width":1280,"height":720,"connection":[{"href":"%s","protocol":"https","supplier":"http","transferFormat":"progressive"}]}]}`, transport.mediaURL)
		}
	case "open.live.bbc.co.uk/redirected", "127.0.0.1/redirected":
		transport.mu.Lock()
		transport.targetCalls++
		transport.mu.Unlock()
		body = "must-not-fetch"
	default:
		return &http.Response{StatusCode: http.StatusNotFound, Body: http.NoBody, Header: make(http.Header), Request: request}, nil
	}
	return &http.Response{
		StatusCode: status,
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     make(http.Header),
		Request:    request,
	}, nil
}

func TestBBCProductArticleChildReentryDirectMediaDownloadStripsCredentials(t *testing.T) {
	mediaServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		t.Fatalf("unexpected direct media server request: %s", request.URL.String())
	}))
	defer mediaServer.Close()
	mediaURL := mediaServer.URL + "/video.mp4"

	for _, test := range []struct {
		name         string
		selectorFail bool
		wantCategory ErrorCategory
	}{
		{name: "success"},
		{name: "media-failure", selectorFail: true, wantCategory: ErrorUnsupported},
	} {
		t.Run(test.name, func(t *testing.T) {
			roundTripper := &bbcProductDownloadRoundTripper{
				mediaURL:     mediaURL,
				selectorFail: test.selectorFail,
			}
			transport, err := network.New(network.Config{
				RoundTripper: roundTripper,
				DefaultHeaders: http.Header{
					"Authorization":       {"Bearer ambient-secret"},
					"Cookie":              {"session=ambient-secret"},
					"Proxy-Authorization": {"Basic ambient-secret"},
					"Referer":             {"https://www.bbc.co.uk/iplayer/ambient"},
				},
			})
			if err != nil {
				t.Fatal(err)
			}
			defer transport.CloseIdleConnections()

			root := t.TempDir()
			request := Request{
				URL:            "https://www.bbc.co.uk/programmes/articles/FixtureArticleId/title",
				OutputDir:      root,
				OutputTemplate: "%(id)s.%(ext)s",
				Overwrite:      true,
				Format:         "http",
				Playlist: PlaylistOptions{
					Items:       "1",
					ErrorPolicy: PlaylistErrorAbort,
				},
			}
			compatibility, err := prepareCompatibility(request)
			if err != nil {
				t.Fatal(err)
			}
			rootExtractor := ""
			capabilities := mediaformat.PlannerCapabilities{CanMergeFormats: true}
			operation := &operation{
				client:              newBroadTestClient(),
				request:             request,
				transport:           transport,
				registry:            productRuntime(),
				compatibility:       compatibility,
				rootExtractor:       &rootExtractor,
				plannerCapabilities: &capabilities,
			}
			result, runErr := operation.process(context.Background(), request.URL, "", nil, make(map[string]bool), 0)
			if test.selectorFail {
				if runErr == nil || !IsCategory(runErr, test.wantCategory) {
					t.Fatalf("error = %v, want category %s", runErr, test.wantCategory)
				}
				entries, err := os.ReadDir(root)
				if err != nil {
					t.Fatal(err)
				}
				if len(entries) != 0 {
					t.Fatalf("artifacts remain after media failure: %v", entries)
				}
			} else {
				if runErr != nil {
					t.Fatal(runErr)
				}
				if len(result.Entries) != 1 || !result.Entries[0].Downloaded || result.Entries[0].Filename == "" {
					t.Fatalf("result = %+v", result)
				}
				data, err := os.ReadFile(result.Entries[0].Filename)
				if err != nil || string(data) != "bbc-direct-media" {
					t.Fatalf("download = %q, %v", data, err)
				}
			}

			roundTripper.mu.Lock()
			defer roundTripper.mu.Unlock()
			if roundTripper.targetCalls != 0 {
				t.Fatalf("redirect targets fetched = %d", roundTripper.targetCalls)
			}
			for _, key := range []string{
				"www.bbc.co.uk/programmes/articles/FixtureArticleId/title",
				"www.bbc.co.uk/programmes/p0000002",
				"open.live.bbc.co.uk/mediaselector/6/select/version/2.0/mediaset/iptv-all/vpid/p0000002",
				"open.live.bbc.co.uk/mediaselector/6/select/version/2.0/mediaset/pc/vpid/p0000002",
			} {
				headers := roundTripper.requests[key]
				if len(headers) == 0 {
					t.Fatalf("missing request %s", key)
				}
				for _, header := range headers {
					for _, name := range []string{"Authorization", "Cookie", "Proxy-Authorization"} {
						if v := header.Get(name); v != "" {
							t.Fatalf("isolated BBC request leaked %s on %s: %s", name, key, v)
						}
					}
				}
			}
			if !test.selectorFail {
				parsedMedia, err := url.Parse(mediaURL)
				if err != nil {
					t.Fatal(err)
				}
				mediaRequestKey := parsedMedia.Host + parsedMedia.Path
				headers := roundTripper.requests[mediaRequestKey]
				if len(headers) == 0 {
					t.Fatalf("missing direct media request %s", mediaRequestKey)
				}
				for _, header := range headers {
					for _, name := range []string{"Authorization", "Cookie", "Proxy-Authorization", "Referer"} {
						if v := header.Get(name); v != "" {
							t.Fatalf("isolated media download leaked %s: %s", name, v)
						}
					}
				}
			}
		})
	}
}
