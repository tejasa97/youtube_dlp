package ytdlp

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/tejasa97/youtube_dlp/internal/extractor"
	"github.com/tejasa97/youtube_dlp/internal/network"
	"github.com/tejasa97/youtube_dlp/internal/value"
)

func TestNRKFamilyRegistryRouting(t *testing.T) {
	registry := productRegistry()
	cases := map[string]string{
		"https://www.nrk.no/skole/?mediaId=14099":                                     "nrk_skole",
		"https://www.nrk.no/skole/?page=search&q=&mediaId=14099":                      "nrk_skole",
		"https://radio.nrk.no/podkast/fixture/l_96f4f1b0-de54-4e6a-b4f1-b0de54fe6af8": "nrk_radio_podkast",
		"https://tv.nrk.no/serie/hellums-kro/sesong/1/episode/2":                      "nrktv_episode",
		"https://tv.nrk.no/program/episodes/nytt-paa-nytt/69031":                      "nrktv_episodes",
		"https://tv.nrk.no/direkte/nrk1":                                              "nrktv_direkte",
		"https://tv.nrk.no/serie/fixture/sesong/1":                                    "nrktv_season",
		"https://tv.nrk.no/serie/fixture":                                             "nrktv_series",
		"https://tv.nrk.no/program/MDDP12000117":                                      "nrktv",
		"nrk:MDDP12000117":                                                            "nrk",
	}
	for raw, want := range cases {
		selected, err := registry.Select(raw)
		if err != nil || selected.Name() != want {
			t.Fatalf("Select(%q) = %v err=%v want %q", raw, selected, err, want)
		}
	}
}

func TestProductNRKTVReentersOpaquePlayback(t *testing.T) {
	transport, err := network.New(network.Config{RoundTripper: &nrkProductRoundTripper{
		manifest: readProductConformanceFixture(t, "risk", "nrk", "manifest.json"),
		metadata: readProductConformanceFixture(t, "risk", "nrk", "metadata.json"),
	}})
	if err != nil {
		t.Fatal(err)
	}
	registry := productRegistry()
	redirect, name, err := registry.Extract(context.Background(), extractor.Request{
		URL: "https://tv.nrk.no/program/MDDP12000117", Transport: transport,
	})
	if err != nil || name != "nrktv" || !redirect.IsURL() {
		t.Fatalf("redirect=%#v name=%q err=%v", redirect, name, err)
	}
	mediaExtractor, err := registry.SelectFor(redirect.Redirect.URL, redirect.Redirect.ExtractorKey)
	if err != nil || mediaExtractor.Name() != "nrk" {
		t.Fatalf("media=%v err=%v", mediaExtractor, err)
	}
	media, err := mediaExtractor.Extract(context.Background(), extractor.Request{
		URL: redirect.Redirect.URL, Transport: transport,
	})
	if err != nil || media.IsPlaylist() || media.IsURL() {
		t.Fatalf("media=%#v err=%v", media, err)
	}
	if id, _ := media.Info.Lookup("id").StringValue(); id != "MDDP12000117" {
		t.Fatalf("id=%q", id)
	}
}

func TestProductNRKSeasonPlaylistReentersOpaquePlayback(t *testing.T) {
	firstURL := "https://psapi.nrk.no/tv/catalog/series/fixture/seasons/1?pageSize=50"
	transport, err := network.New(network.Config{RoundTripper: &nrkProductRoundTripper{
		manifest: readProductConformanceFixture(t, "risk", "nrk", "manifest.json"),
		metadata: readProductConformanceFixture(t, "risk", "nrk", "metadata.json"),
		catalog: map[string][]byte{
			firstURL: []byte(`{"titles":{"title":"Fixture Season"},"_embedded":{"instalments":[{"prfId":"MDDP12000117","title":"Episode One"}]}}`),
		},
	}})
	if err != nil {
		t.Fatal(err)
	}
	registry := productRegistry()
	playlist, name, err := registry.Extract(context.Background(), extractor.Request{
		URL: "https://tv.nrk.no/serie/fixture/sesong/1", Transport: transport,
	})
	if err != nil || name != "nrktv_season" || !playlist.IsPlaylist() {
		t.Fatalf("playlist=%#v name=%q err=%v", playlist, name, err)
	}
	entry, ok, err := playlist.Entries.Iterator().Next(context.Background())
	if err != nil || !ok || entry.URL != "nrk:MDDP12000117" {
		t.Fatalf("entry=%#v ok=%v err=%v", entry, ok, err)
	}
	mediaExtractor, err := registry.SelectFor(entry.URL, entry.ExtractorKey)
	if err != nil {
		t.Fatal(err)
	}
	media, err := mediaExtractor.Extract(context.Background(), extractor.Request{URL: entry.URL, Transport: transport})
	if err != nil || media.IsPlaylist() {
		t.Fatalf("media=%#v err=%v", media, err)
	}
}

func TestProductNRKCredentialIsolatedMediaDownloadStripsAmbientCredentials(t *testing.T) {
	var mu sync.Mutex
	var captured http.Header
	var requestSeen atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requestSeen.Store(true)
		mu.Lock()
		captured = request.Header.Clone()
		mu.Unlock()
		_, _ = writer.Write([]byte("media-bytes"))
	}))
	defer server.Close()

	transport, err := network.New(network.Config{
		DefaultHeaders: http.Header{
			"Cookie":              {"ambient-session=secret"},
			"Authorization":       {"Bearer ambient-token"},
			"Proxy-Authorization": {"Basic proxy-secret"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	info := value.NewInfo(value.NewObject(
		value.Field{Key: "id", Value: value.String("MDDP12000117")},
		value.Field{Key: "title", Value: value.String("Fixture NRK Programme")},
		value.Field{Key: "ext", Value: value.String("mp4")},
	))
	info.Set("formats", value.List(value.ObjectValue(value.NewObject(
		value.Field{Key: "format_id", Value: value.String("https")},
		value.Field{Key: "url", Value: value.String(server.URL + "/media.bin")},
		value.Field{Key: "ext", Value: value.String("bin")},
		value.Field{Key: "protocol", Value: value.String("https")},
		value.Field{Key: "_credential_isolated", Value: value.Bool(true)},
	))))

	operation := &operation{client: NewClient(), request: Request{OutputDir: root}, transport: transport}
	selected, err := operation.selectFormats(info)
	if err != nil {
		t.Fatal(err)
	}
	if len(selected) != 1 || !selected[0].CredentialIsolated {
		t.Fatalf("selection=%#v", selected)
	}
	_, _, err = operation.downloadSelection(context.Background(), selected[0], root, root+"/out.bin", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !requestSeen.Load() {
		t.Fatal("credential-isolated media download did not issue a media request")
	}
	mu.Lock()
	defer mu.Unlock()
	for _, key := range []string{"Authorization", "Cookie", "Proxy-Authorization"} {
		if captured.Get(key) != "" {
			t.Fatalf("isolated media download leaked %s: %s", key, captured.Get(key))
		}
	}
}

func TestProductNRKPlaybackFailureCategoryAndCancellation(t *testing.T) {
	t.Run("geo-failure-zero-artifacts", func(t *testing.T) {
		transport, err := network.New(network.Config{RoundTripper: &nrkProductRoundTripper{
			manifestStatus: http.StatusForbidden,
		}})
		if err != nil {
			t.Fatal(err)
		}
		root := t.TempDir()
		rootExtractor := ""
		operation := &operation{
			client: NewClient(), request: Request{
				URL:            "nrk:MDDP12000117",
				OutputDir:      root,
				OutputTemplate: "%(id)s.%(ext)s",
			},
			transport: transport, registry: productRegistry(), rootExtractor: &rootExtractor,
		}
		_, runErr := operation.process(context.Background(), "nrk:MDDP12000117", "", nil, make(map[string]bool), 0)
		if !IsCategory(runErr, ErrorUnsupported) || !errors.Is(runErr, extractor.ErrRegionRestricted) {
			t.Fatalf("geo category=%v", runErr)
		}
		entries, err := os.ReadDir(root)
		if err != nil {
			t.Fatal(err)
		}
		if len(entries) != 0 {
			t.Fatalf("artifacts remain after geo failure: %v", entries)
		}
	})
	t.Run("cancel-zero-artifacts", func(t *testing.T) {
		transport, err := network.New(network.Config{RoundTripper: &nrkProductRoundTripper{
			manifest: readProductConformanceFixture(t, "risk", "nrk", "manifest.json"),
		}})
		if err != nil {
			t.Fatal(err)
		}
		root := t.TempDir()
		rootExtractor := ""
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		operation := &operation{
			client: NewClient(), request: Request{
				URL:            "https://tv.nrk.no/program/MDDP12000117",
				OutputDir:      root,
				OutputTemplate: "%(id)s.%(ext)s",
			},
			transport: transport, registry: productRegistry(), rootExtractor: &rootExtractor,
		}
		_, runErr := operation.process(ctx, "https://tv.nrk.no/program/MDDP12000117", "", nil, make(map[string]bool), 0)
		if !IsCategory(runErr, ErrorCancelled) {
			t.Fatalf("cancel category=%v", runErr)
		}
		entries, err := os.ReadDir(root)
		if err != nil {
			t.Fatal(err)
		}
		if len(entries) != 0 {
			t.Fatalf("artifacts remain after cancellation: %v", entries)
		}
	})
}

type nrkProductRoundTripper struct {
	manifest       []byte
	metadata       []byte
	manifestStatus int
	catalog        map[string][]byte
}

func (rt *nrkProductRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	switch request.URL.String() {
	case "https://psapi.nrk.no/playback/manifest/program/MDDP12000117?preferredCdn=akamai":
		status := rt.manifestStatus
		if status == 0 {
			status = http.StatusOK
		}
		body := rt.manifest
		if body == nil {
			body = []byte("{}")
		}
		return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(bytes.NewReader(body)), Request: request}, nil
	case "https://psapi.nrk.no/playback/metadata/program/MDDP12000117":
		body := rt.metadata
		if body == nil {
			body = []byte("{}")
		}
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(bytes.NewReader(body)), Request: request}, nil
	default:
		if body, ok := rt.catalog[request.URL.String()]; ok {
			return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(bytes.NewReader(body)), Request: request}, nil
		}
		return &http.Response{StatusCode: http.StatusNotFound, Header: make(http.Header), Body: io.NopCloser(bytes.NewReader(nil)), Request: request}, nil
	}
}
