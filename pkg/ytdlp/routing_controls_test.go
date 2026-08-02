package ytdlp

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/ytdlp-go/ytdlp/internal/extractor"
	"github.com/ytdlp-go/ytdlp/internal/network"
	"github.com/ytdlp-go/ytdlp/internal/value"
)

func TestRouteRequestInputURLAndSearchClassification(t *testing.T) {
	registry := productRegistry()
	tests := []struct {
		name          string
		rawURL        string
		defaultSearch string
		forceGeneric  bool
		wantURL       string
		wantQuery     string
		wantExtractor string
		wantWarning   bool
		wantCategory  ErrorCategory
		wantSentinel  error
	}{
		{name: "https URL wins", rawURL: "https://example.invalid/watch", defaultSearch: "auto", wantURL: "https://example.invalid/watch"},
		{name: "protocol-less public host is repaired", rawURL: "example.invalid/watch", wantURL: "https://example.invalid/watch", wantWarning: true},
		{name: "auto search", rawURL: "cats & dogs?", defaultSearch: "auto", wantURL: "ytsearch:ytdlp-routing-placeholder", wantQuery: "cats & dogs?", wantExtractor: "youtube_search"},
		{name: "auto warning", rawURL: "cats", defaultSearch: "auto_warning", wantURL: "ytsearch:ytdlp-routing-placeholder", wantQuery: "cats", wantExtractor: "youtube_search", wantWarning: true},
		{name: "soundcloud prefix", rawURL: "ambient mix", defaultSearch: "scsearch5:", wantURL: "scsearch5:ytdlp-routing-placeholder", wantQuery: "ambient mix", wantExtractor: "soundcloud_search"},
		{name: "niconico prefix", rawURL: "fixture term", defaultSearch: "nicosearch", wantURL: "nicosearch:ytdlp-routing-placeholder", wantQuery: "fixture term", wantExtractor: "niconico_search"},
		{name: "prx prefix", rawURL: "fixture term", defaultSearch: "prxstories:", wantURL: "prxstories:ytdlp-routing-placeholder", wantQuery: "fixture term", wantExtractor: "prx_stories_search"},
		{name: "force generic URL", rawURL: "https://example.invalid/watch", forceGeneric: true, wantURL: "https://example.invalid/watch"},
		{name: "default search error", rawURL: "plain term", defaultSearch: "error", wantCategory: ErrorUnsupported, wantSentinel: extractor.ErrUnsupported},
		{name: "default fixup error", rawURL: "plain term", wantCategory: ErrorUnsupported, wantSentinel: extractor.ErrUnsupported},
		{name: "unsupported prefix", rawURL: "plain term", defaultSearch: "gvsearch2", wantCategory: ErrorUnsupported, wantSentinel: extractor.ErrUnsupportedRouting},
		{name: "localhost never repaired", rawURL: "localhost/watch", defaultSearch: "auto", wantCategory: ErrorInvalidInput, wantSentinel: extractor.ErrInvalidRouting},
		{name: "IP never repaired", rawURL: "127.0.0.1/watch", defaultSearch: "auto", wantCategory: ErrorInvalidInput, wantSentinel: extractor.ErrInvalidRouting},
		{name: "IPv6 never repaired", rawURL: "::1/watch", defaultSearch: "auto", wantCategory: ErrorInvalidInput, wantSentinel: extractor.ErrInvalidRouting},
		{name: "userinfo never repaired", rawURL: "user:pass@example.invalid/watch", defaultSearch: "auto", wantCategory: ErrorInvalidInput, wantSentinel: extractor.ErrInvalidRouting},
		{name: "force generic rejects pseudo URL", rawURL: "ytsearch:cats", forceGeneric: true, wantCategory: ErrorUnsupported, wantSentinel: extractor.ErrUnsupported},
		{name: "force generic rejects userinfo", rawURL: "https://user:pass@example.invalid/watch", forceGeneric: true, wantCategory: ErrorUnsupported, wantSentinel: extractor.ErrUnsupported},
		{name: "force generic rejects non HTTP", rawURL: "ftp://example.invalid/file", forceGeneric: true, wantCategory: ErrorUnsupported, wantSentinel: extractor.ErrUnsupported},
		{name: "query bound", rawURL: strings.Repeat("q", 501), defaultSearch: "auto", wantCategory: ErrorInvalidInput, wantSentinel: extractor.ErrInvalidRouting},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := routeRequestInput(context.Background(), registry, Request{
				URL: test.rawURL, DefaultSearch: test.defaultSearch, ForceGenericExtractor: test.forceGeneric,
			})
			if test.wantSentinel != nil {
				if err == nil || !errors.Is(err, test.wantSentinel) || !IsCategory(categorized("route", err), test.wantCategory) {
					t.Fatalf("route error=%v category=%v want sentinel=%v category=%v", err, categorized("route", err), test.wantSentinel, test.wantCategory)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got.URL != test.wantURL || got.SearchQuery != test.wantQuery || got.SearchExtractor != test.wantExtractor || (got.Warning != "") != test.wantWarning {
				t.Fatalf("route=%+v", got)
			}
			if strings.Contains(got.URL, test.wantQuery) && test.wantQuery != "" {
				t.Fatalf("generated target echoed the query: %q", got.URL)
			}
		})
	}
}

func TestRouteRequestInputCancellationAndConcurrency(t *testing.T) {
	registry := productRegistry()
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := routeRequestInput(cancelled, registry, Request{URL: "plain term", DefaultSearch: "auto"}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled route error=%v", err)
	}

	const workers = 24
	var group sync.WaitGroup
	var failures atomic.Int32
	for index := 0; index < workers; index++ {
		group.Add(1)
		go func(index int) {
			defer group.Done()
			query := "term-" + string(rune('a'+index))
			got, err := routeRequestInput(context.Background(), registry, Request{URL: query, DefaultSearch: "ytsearch"})
			if err != nil || got.SearchQuery != query || got.SearchExtractor != "youtube_search" {
				failures.Add(1)
			}
		}(index)
	}
	group.Wait()
	if failures.Load() != 0 {
		t.Fatalf("concurrent routing failures=%d", failures.Load())
	}
}

func TestClientRoutesBeforeTransportAndDoesNotExposeInput(t *testing.T) {
	var calls atomic.Int32
	client := NewClient(withTransportFactory(func(config network.Config) (*network.Client, error) {
		calls.Add(1)
		return network.New(config)
	}))
	_, err := client.Run(context.Background(), Request{URL: "plain term", DefaultSearch: "error"})
	if err == nil || !IsCategory(err, ErrorUnsupported) || !errors.Is(err, extractor.ErrUnsupported) {
		t.Fatalf("error=%v", err)
	}
	if calls.Load() != 0 {
		t.Fatalf("transport factory called %d times for invalid routing", calls.Load())
	}
	if strings.Contains(err.Error(), "plain term") {
		t.Fatalf("diagnostic echoed input: %v", err)
	}

	_, err = client.Run(context.Background(), Request{URL: "https://user:secret@example.invalid/watch", ForceGenericExtractor: true})
	if err == nil || !IsCategory(err, ErrorUnsupported) || !errors.Is(err, extractor.ErrUnsupported) {
		t.Fatalf("unsafe generic error=%v", err)
	}
	if strings.Contains(err.Error(), "secret") || strings.Contains(err.Error(), "user:") {
		t.Fatalf("credential-bearing diagnostic: %v", err)
	}
	if calls.Load() != 0 {
		t.Fatalf("transport factory called %d times for unsafe generic routing", calls.Load())
	}
}

func TestClientForceGenericUsesRegisteredGenericExtractor(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "video/mp4")
		writer.Header().Set("Content-Length", "4")
		if request.Method != http.MethodHead {
			_, _ = io.WriteString(writer, "data")
		}
	}))
	defer server.Close()
	result, err := NewClient().Run(context.Background(), Request{URL: server.URL + "/media.mp4", ForceGenericExtractor: true, SkipDownload: true})
	if err != nil {
		t.Fatal(err)
	}
	if result.Extractor != "generic" {
		t.Fatalf("extractor=%q result=%+v", result.Extractor, result)
	}
}

func TestDefaultSearchRequestOverrideIsTypedAndSafe(t *testing.T) {
	registry := productRegistry()
	routed, err := routeRequestInput(context.Background(), registry, Request{URL: "cats & dogs? token=secret", DefaultSearch: "ytsearch"})
	if err != nil {
		t.Fatal(err)
	}
	if routed.SearchQuery != "cats & dogs? token=secret" || routed.URL != "ytsearch:ytdlp-routing-placeholder" {
		t.Fatalf("routed=%+v", routed)
	}
	if strings.Contains(routed.URL, "secret") {
		t.Fatalf("generated URL contains query data: %q", routed.URL)
	}
}

type routingSearchRoundTripper struct {
	mu       sync.Mutex
	requests []string
}

func (transport *routingSearchRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	transport.mu.Lock()
	transport.requests = append(transport.requests, request.URL.String())
	transport.mu.Unlock()

	status := http.StatusOK
	if request.URL.Scheme != "https" || request.URL.Host != "cms.prx.org" || request.URL.Path != "/api/v1/stories/search" {
		status = http.StatusNotFound
	}
	return &http.Response{
		StatusCode: status,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(`{"count":0,"total":0,"_embedded":{"prx:items":[]}}`)),
		Request:    request,
	}, nil
}

func (transport *routingSearchRoundTripper) requestURLs() []string {
	transport.mu.Lock()
	defer transport.mu.Unlock()
	return append([]string(nil), transport.requests...)
}

func TestClientDefaultSearchReachesRegisteredBackendAndIsolatesConcurrentRuns(t *testing.T) {
	transport := &routingSearchRoundTripper{}
	var eventsMu sync.Mutex
	var events []Event
	client := NewClient(
		WithEventHandler(func(_ context.Context, event Event) error {
			eventsMu.Lock()
			events = append(events, event)
			eventsMu.Unlock()
			return nil
		}),
		withTransportFactory(func(config network.Config) (*network.Client, error) {
			config.RoundTripper = transport
			return network.New(config)
		}),
	)

	queries := []string{
		"alpha & plus+ one",
		"beta & percent two",
		"gamma token=secret three",
		"delta café four",
	}
	type outcome struct {
		query  string
		result Result
		err    error
	}
	outcomes := make(chan outcome, len(queries))
	var group sync.WaitGroup
	for _, query := range queries {
		group.Add(1)
		go func(query string) {
			defer group.Done()
			result, err := client.Run(context.Background(), Request{URL: query, DefaultSearch: "prxstories"})
			outcomes <- outcome{query: query, result: result, err: err}
		}(query)
	}
	group.Wait()
	close(outcomes)

	for result := range outcomes {
		if result.err != nil {
			t.Fatalf("query %q: %v", result.query, result.err)
		}
		if result.result.Extractor != "prx_stories_search" || len(result.result.Entries) != 0 {
			t.Fatalf("query %q: result=%+v", result.query, result.result)
		}
	}

	want := make(map[string]string, len(queries))
	for _, query := range queries {
		want[query] = "https://cms.prx.org/api/v1/stories/search?" + url.Values{
			"q": {query}, "page": {"1"}, "per": {"100"},
		}.Encode()
	}
	got := transport.requestURLs()
	if len(got) != len(queries) {
		t.Fatalf("backend request count=%d want=%d requests=%v", len(got), len(queries), got)
	}
	for _, raw := range got {
		matched := false
		for query, expected := range want {
			if raw == expected {
				matched = true
				delete(want, query)
				break
			}
		}
		if !matched {
			t.Fatalf("unexpected backend request %q", raw)
		}
	}
	if len(want) != 0 {
		t.Fatalf("missing backend requests for queries=%v", want)
	}

	eventsMu.Lock()
	capturedEvents := append([]Event(nil), events...)
	eventsMu.Unlock()
	for _, event := range capturedEvents {
		for _, query := range queries {
			if strings.Contains(event.URL, query) || strings.Contains(event.Message, query) {
				t.Fatalf("query %q leaked into event=%+v", query, event)
			}
		}
		if (event.Kind == EventExtracting || event.Kind == EventExtracted) && event.URL != "prxstories:ytdlp-routing-placeholder" {
			t.Fatalf("search target was not fixed in event=%+v", event)
		}
	}
}

type routingOverrideExtractor struct {
	name      string
	scheme    string
	childName string
	root      bool
	mu        sync.Mutex
	overrides []string
}

func (probe *routingOverrideExtractor) Name() string { return probe.name }
func (probe *routingOverrideExtractor) Suitable(parsed *url.URL) bool {
	return parsed != nil && parsed.Scheme == probe.scheme
}
func (probe *routingOverrideExtractor) Extract(ctx context.Context, request extractor.Request) (extractor.Extraction, error) {
	if err := ctx.Err(); err != nil {
		return extractor.Extraction{}, err
	}
	probe.mu.Lock()
	probe.overrides = append(probe.overrides, request.SearchQueryOverride)
	probe.mu.Unlock()
	if probe.root {
		return extractor.URLResult(extractor.Entry{URL: "routing-child:input", ExtractorKey: probe.childName})
	}
	return extractor.Playlist(value.NewInfo(value.NewObject(
		value.Field{Key: "id", Value: value.String("child")},
		value.Field{Key: "title", Value: value.String("child")},
	)), extractor.StaticEntries())
}
func (probe *routingOverrideExtractor) seenOverrides() []string {
	probe.mu.Lock()
	defer probe.mu.Unlock()
	return append([]string(nil), probe.overrides...)
}

func TestOperationSearchQueryOverrideIsRootOnlyForURLResultChildren(t *testing.T) {
	root := &routingOverrideExtractor{name: "routing-root", scheme: "routing-root", childName: "routing-child", root: true}
	child := &routingOverrideExtractor{name: "routing-child", scheme: "routing-child"}
	registry := extractor.NewRegistry(root, child)
	rootExtractor := ""
	operation := &operation{
		client:             NewClient(),
		request:            Request{Simulate: true},
		registry:           registry,
		routingSearchQuery: "original bounded query",
		rootExtractor:      &rootExtractor,
	}
	if _, err := operation.process(context.Background(), "routing-root:input", "", nil, map[string]bool{}, 0); err != nil {
		t.Fatal(err)
	}
	if got := root.seenOverrides(); len(got) != 1 || got[0] != "original bounded query" {
		t.Fatalf("root overrides=%v", got)
	}
	if got := child.seenOverrides(); len(got) != 1 || got[0] != "" {
		t.Fatalf("child inherited root override=%v", got)
	}
}

type routingSelectionSpy struct{ suitableCalls atomic.Int32 }

func (spy *routingSelectionSpy) Name() string { return "built-in-spy" }
func (spy *routingSelectionSpy) Suitable(*url.URL) bool {
	spy.suitableCalls.Add(1)
	return true
}
func (*routingSelectionSpy) Extract(context.Context, extractor.Request) (extractor.Extraction, error) {
	return extractor.Extraction{}, extractor.ErrUnsupported
}

func TestRouteRequestInputPluginIDPreservesOriginalAndBypassesBuiltIns(t *testing.T) {
	spy := &routingSelectionSpy{}
	routed, err := routeRequestInput(context.Background(), extractor.NewRegistry(spy), Request{
		URL: "example.invalid/path", PluginID: "signed-plugin", ForceGenericExtractor: true, DefaultSearch: "auto",
	})
	if err != nil {
		t.Fatal(err)
	}
	if routed.URL != "example.invalid/path" || routed.SearchQuery != "" || routed.Warning != "" || routed.SearchExtractor != "" {
		t.Fatalf("plugin route rewrote input: %+v", routed)
	}
	if spy.suitableCalls.Load() != 0 {
		t.Fatalf("built-in selection was consulted %d times", spy.suitableCalls.Load())
	}
}
