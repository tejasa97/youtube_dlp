package ytdlp

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/ytdlp-go/ytdlp/internal/extractor"
	"github.com/ytdlp-go/ytdlp/internal/network"
)

type dailymotionProductRoundTripper struct {
	mu       sync.Mutex
	requests []string
	metadata []byte
}

func newDailymotionProductRoundTripper(t *testing.T) *dailymotionProductRoundTripper {
	t.Helper()
	return &dailymotionProductRoundTripper{
		metadata: readProductConformanceFixture(t, "public", "dailymotion", "success.json"),
	}
}

func (rt *dailymotionProductRoundTripper) record(request *http.Request) {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	rt.requests = append(rt.requests, request.Method+" "+request.URL.String())
}

func (rt *dailymotionProductRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	rt.record(request)
	switch {
	case request.URL.String() == "https://graphql.api.dailymotion.com/oauth/token":
		for _, header := range []string{"Authorization", "Cookie", "Proxy-Authorization"} {
			if request.Header.Get(header) != "" {
				return nil, extractor.ErrTransportIsolation
			}
		}
		return &http.Response{
			StatusCode: http.StatusOK, Header: make(http.Header),
			Body:    io.NopCloser(strings.NewReader(`{"access_token":"fixture-dailymotion-token","token_type":"Bearer"}`)),
			Request: request,
		}, nil
	case request.URL.String() == "https://graphql.api.dailymotion.com/":
		if !strings.HasPrefix(request.Header.Get("Authorization"), "Bearer fixture-") {
			return &http.Response{StatusCode: http.StatusUnauthorized, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("{}")), Request: request}, nil
		}
		if request.Header.Get("Cookie") != "" || request.Header.Get("Proxy-Authorization") != "" {
			return nil, extractor.ErrTransportIsolation
		}
		body, _ := io.ReadAll(request.Body)
		if bytes.Contains(body, []byte(`"operationName":"SEARCH_QUERY"`)) {
			return &http.Response{
				StatusCode: http.StatusOK, Header: make(http.Header),
				Body:    io.NopCloser(strings.NewReader(`{"data":{"search":{"videos":{"edges":[{"node":{"xid":"xfixture"}}]}}}}`)),
				Request: request,
			}, nil
		}
		return &http.Response{
			StatusCode: http.StatusOK, Header: make(http.Header),
			Body:    io.NopCloser(strings.NewReader(`{"data":{"channel":{"videos":{"edges":[{"node":{"xid":"xfixture","url":"https://www.dailymotion.com/video/xfixture"}}]}}}}`)),
			Request: request,
		}, nil
	case request.URL.String() == "https://www.dailymotion.com/player/metadata/video/xfixture":
		return &http.Response{
			StatusCode: http.StatusOK, Header: make(http.Header),
			Body: io.NopCloser(bytes.NewReader(rt.metadata)), Request: request,
		}, nil
	case request.URL.String() == "https://www.dailymotion.com/player/metadata/video/xmissing":
		return &http.Response{
			StatusCode: http.StatusNotFound, Header: make(http.Header),
			Body: io.NopCloser(strings.NewReader(`{}`)), Request: request,
		}, nil
	default:
		return &http.Response{StatusCode: http.StatusNotFound, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("")), Request: request}, nil
	}
}

func TestProductRegistrySelectsDailymotionDiscoveryExtractors(t *testing.T) {
	registry := productRegistry()
	for raw, want := range map[string]string{
		"https://www.dailymotion.com/search/fixture/videos": "dailymotion_search",
		"https://www.dailymotion.com/user/fixture":          "dailymotion_user",
	} {
		selected, err := registry.Select(raw)
		if err != nil || selected.Name() != want {
			t.Fatalf("Select(%q) = %v err=%v want %q", raw, selected, err, want)
		}
	}
}

func TestProductDailymotionSearchIsLazyAndReentersMedia(t *testing.T) {
	rt := newDailymotionProductRoundTripper(t)
	transport, err := network.New(network.Config{RoundTripper: rt})
	if err != nil {
		t.Fatal(err)
	}
	registry := productRegistry()
	playlist, name, err := registry.Extract(context.Background(), extractor.Request{
		URL: "https://www.dailymotion.com/search/fixture/videos", Transport: transport,
	})
	if err != nil || name != "dailymotion_search" || !playlist.IsPlaylist() {
		t.Fatalf("playlist=%#v name=%q err=%v", playlist, name, err)
	}
	rt.mu.Lock()
	if len(rt.requests) != 0 {
		t.Fatalf("eager requests=%v", rt.requests)
	}
	rt.mu.Unlock()
	entry, ok, err := playlist.Entries.Iterator().Next(context.Background())
	if err != nil || !ok || entry.URL != "https://www.dailymotion.com/video/xfixture" {
		t.Fatalf("entry=%#v ok=%v err=%v", entry, ok, err)
	}
	mediaExtractor, err := registry.SelectFor(entry.URL, entry.ExtractorKey)
	if err != nil || mediaExtractor.Name() != "dailymotion" {
		t.Fatalf("media=%v err=%v", mediaExtractor, err)
	}
	media, err := mediaExtractor.Extract(context.Background(), extractor.Request{URL: entry.URL, Transport: transport})
	if err != nil || media.IsPlaylist() {
		t.Fatalf("media=%#v err=%v", media, err)
	}
	if id, _ := media.Info.Lookup("id").StringValue(); id != "xfixture" {
		t.Fatalf("id=%q", id)
	}
}

func TestProductDailymotionDiscoveryChildFailurePreservesCategory(t *testing.T) {
	rt := newDailymotionProductRoundTripper(t)
	transport, err := network.New(network.Config{RoundTripper: rt})
	if err != nil {
		t.Fatal(err)
	}
	registry := productRegistry()
	playlist, _, err := registry.Extract(context.Background(), extractor.Request{
		URL: "https://www.dailymotion.com/search/fixture/videos", Transport: transport,
	})
	if err != nil {
		t.Fatal(err)
	}
	entry, ok, err := playlist.Entries.Iterator().Next(context.Background())
	if err != nil || !ok {
		t.Fatal(err)
	}
	entry.URL = "https://www.dailymotion.com/video/xmissing"
	mediaExtractor, err := registry.SelectFor(entry.URL, entry.ExtractorKey)
	if err != nil {
		t.Fatal(err)
	}
	_, err = mediaExtractor.Extract(context.Background(), extractor.Request{URL: entry.URL, Transport: transport})
	if !IsCategory(categorized("dailymotion", err), ErrorUnsupported) {
		t.Fatalf("child failure category=%v", err)
	}
}

func TestProductDailymotionDiscoveryCancellationPreservesCategory(t *testing.T) {
	rt := newDailymotionProductRoundTripper(t)
	transport, err := network.New(network.Config{RoundTripper: rt})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	registry := productRegistry()
	_, _, err = registry.Extract(ctx, extractor.Request{
		URL: "https://www.dailymotion.com/user/fixture", Transport: transport,
	})
	if !IsCategory(categorized("dailymotion_user", err), ErrorCancelled) {
		t.Fatalf("cancel category=%v", err)
	}
}
