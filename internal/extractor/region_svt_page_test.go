package extractor

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"
)

type svtPageFixtureTransport struct {
	mu       sync.Mutex
	body     []byte
	video    []byte
	status   int
	err      error
	requests []*http.Request
	apiCalls []string
}

func (transport *svtPageFixtureTransport) Do(ctx context.Context, request *http.Request) (*http.Response, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if transport.video == nil || !strings.HasPrefix(request.URL.String(), svtVideoAPIBase) {
		return nil, errors.New("unexpected credential-bearing transport use")
	}
	transport.mu.Lock()
	transport.apiCalls = append(transport.apiCalls, request.URL.String())
	transport.mu.Unlock()
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     make(http.Header),
		Body:       io.NopCloser(bytes.NewReader(transport.video)),
		Request:    request,
	}, nil
}

func (*svtPageFixtureTransport) ReadPage(context.Context, string) ([]byte, http.Header, error) {
	return nil, nil, errors.New("redirect-following page transport used")
}

func (transport *svtPageFixtureTransport) DoWithoutCredentialsNoRedirect(ctx context.Context, request *http.Request) (*http.Response, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if transport.err != nil {
		return nil, transport.err
	}
	transport.mu.Lock()
	transport.requests = append(transport.requests, request.Clone(ctx))
	transport.mu.Unlock()
	status := transport.status
	if status == 0 {
		status = http.StatusOK
	}
	return &http.Response{
		StatusCode: status,
		Header:     make(http.Header),
		Body:       io.NopCloser(bytes.NewReader(transport.body)),
		Request:    request,
	}, nil
}

func TestRegionSVTPageRouting(t *testing.T) {
	accepted := []string{
		"https://www.svt.se/nyheter/lokalt/skane/fixture-article",
		"https://svt.se/sport/fixture",
		"https://www.svt.se/nyheter/utrikes/svensk-%C3%A5terkomst",
	}
	for _, rawURL := range accepted {
		parsed, err := url.Parse(rawURL)
		if err != nil || !NewRegionSVT().Suitable(parsed) {
			t.Errorf("Suitable(%q) = false (parse=%v)", rawURL, err)
		}
	}
	rejected := []string{
		"http://www.svt.se/news/article",
		"https://www.svt.se/",
		"https://www.svt.se/news/article/",
		"https://www.svt.se//news/article",
		"https://www.svt.se/news/../article",
		"https://www.svt.se/news/article?secret=value",
		"https://www.svt.se/news/article#fragment",
		"https://user@www.svt.se/news/article",
		"https://www.svt.se:443/news/article",
		"https://www.svt.se/news/encoded%2fseparator",
		"https://www.svt.se/news/encoded%5cseparator",
		"https://www.svt.se/news/nul%00value",
		"https://www.svt.se/barnkanalen/barnplay/show/video-id",
		"https://www.svt.se/barnkanalen/barnplay/show/video-id/",
		"https://evil-svt.se/news/article",
		"https://www.svtplay.se/news/article",
	}
	for _, rawURL := range rejected {
		parsed, err := url.Parse(rawURL)
		if err == nil && NewRegionSVT().Suitable(parsed) {
			t.Errorf("Suitable(%q) = true", rawURL)
		}
	}
}

func TestRegionSVTPageExtractsTransparentEntries(t *testing.T) {
	transport := &svtPageFixtureTransport{body: svtFixture(t, "article-page.html")}
	result, err := NewRegionSVT().Extract(context.Background(), Request{
		URL:       "https://www.svt.se/nyheter/lokalt/fixture-article",
		Transport: transport,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.IsPlaylist() {
		t.Fatal("result is not a playlist")
	}
	if id, _ := result.Info.ID(); id != "fixture-article" {
		t.Fatalf("playlist id = %q", id)
	}
	if title, _ := result.Info.Title(); title != "Fixture & SVT article" {
		t.Fatalf("title = %q", title)
	}
	entries, err := CollectEntries(context.Background(), result.Entries, 10)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"svt-page-top-001", "svt-page-body-002", "svt-page-body-003"}
	if len(entries) != len(want) {
		t.Fatalf("entries = %#v", entries)
	}
	for index, id := range want {
		entry := entries[index]
		if entry.ID != id || entry.URL != "svt:"+id || entry.ExtractorKey != "region_svt" ||
			entry.Title != "Fixture & SVT article" || !entry.Transparent {
			t.Fatalf("entry %d = %#v", index, entry)
		}
	}
	if len(transport.requests) != 1 {
		t.Fatalf("requests = %d", len(transport.requests))
	}
	request := transport.requests[0]
	if request.Method != http.MethodGet ||
		request.URL.String() != "https://www.svt.se/nyheter/lokalt/fixture-article" ||
		request.Header.Get("Accept") != "text/html,application/xhtml+xml" {
		t.Fatalf("request = %#v", request)
	}
	for _, header := range []string{"Cookie", "Authorization", "Proxy-Authorization"} {
		if request.Header.Get(header) != "" {
			t.Fatalf("%s reached page request", header)
		}
	}

	transport.video = svtFixture(t, "video.json")
	selected, err := NewRegistry(NewRegionSVT()).SelectFor(entries[0].URL, entries[0].ExtractorKey)
	if err != nil {
		t.Fatal(err)
	}
	hydrated, err := selected.Extract(context.Background(), Request{URL: entries[0].URL, Transport: transport})
	if err != nil {
		t.Fatal(err)
	}
	if id, _ := hydrated.Info.ID(); id != entries[0].ID {
		t.Fatalf("hydrated id = %q, want %q", id, entries[0].ID)
	}
	if len(transport.apiCalls) != 1 || transport.apiCalls[0] != svtVideoAPIBase+entries[0].ID {
		t.Fatalf("API calls = %#v", transport.apiCalls)
	}
}

func TestRegionSVTPageRequiresIsolationAndCategorizesFailures(t *testing.T) {
	if _, err := NewRegionSVT().Extract(context.Background(), Request{
		URL:       "https://www.svt.se/news/article",
		Transport: svtNonIsolatedTransport{},
	}); !errors.Is(err, ErrTransportIsolation) {
		t.Fatalf("non-isolated error = %v", err)
	}

	for _, test := range []struct {
		name   string
		status int
		err    error
		want   error
	}{
		{"authentication", http.StatusForbidden, nil, ErrAuthentication},
		{"unavailable", http.StatusNotFound, nil, ErrUnavailable},
		{"regional", http.StatusUnavailableForLegalReasons, nil, ErrRegionRestricted},
		{"network", 0, errors.New("dial token=must-not-leak"), ErrSVTPageNetwork},
	} {
		t.Run(test.name, func(t *testing.T) {
			transport := &svtPageFixtureTransport{status: test.status, err: test.err}
			_, err := NewRegionSVT().Extract(context.Background(), Request{
				URL: "https://www.svt.se/news/article", Transport: transport,
			})
			if !errors.Is(err, test.want) || strings.Contains(fmt.Sprint(err), "must-not-leak") {
				t.Fatalf("error = %v, want %v without secret", err, test.want)
			}
		})
	}
}

func TestRegionSVTPageParserBoundsFailuresAndCancellation(t *testing.T) {
	for _, test := range []struct {
		name string
		page []byte
		want error
	}{
		{"oversized", bytes.Repeat([]byte("x"), svtPageMaxHTMLBytes+1), ErrJSONResponseTooLarge},
		{"missing title", []byte(`<script>urqlState={"x":{"data":"{\"page\":{}}"}}</script>`), ErrInvalidMetadata},
		{"missing state", []byte(`<meta property="og:title" content="title">`), ErrInvalidMetadata},
		{"malformed state", []byte(`<meta property="og:title" content="title"><script>urqlState={</script>`), ErrInvalidMetadata},
		{"missing page", svtPageDocument(t, map[string]any{"other": true}), ErrInvalidMetadata},
		{"no videos", svtPageDocument(t, map[string]any{"page": map[string]any{"body": []any{}}}), ErrUnavailable},
		{"title bound", []byte(`<meta property="og:title" content="` + strings.Repeat("x", svtPageMaxTitleBytes+1) + `">` + svtPageStateScript(t, map[string]any{"page": map[string]any{"body": []any{}}})), ErrInvalidMetadata},
		{"state size", []byte(`<meta property="og:title" content="title"><script>urqlState={"padding":"` + strings.Repeat("x", svtPageMaxStateBytes+1) + `"}</script>`), ErrJSONResponseTooLarge},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, _, err := parseSVTPage(context.Background(), test.page)
			if !errors.Is(err, test.want) {
				t.Fatalf("error = %v, want %v", err, test.want)
			}
		})
	}

	body := make([]any, svtPageMaxEntries+1)
	for index := range body {
		body[index] = map[string]any{"video": map[string]any{"svtId": fmt.Sprintf("fixture-%03d", index)}}
	}
	if _, _, err := parseSVTPage(context.Background(), svtPageDocument(t, map[string]any{
		"page": map[string]any{"body": body},
	})); !errors.Is(err, ErrPlaylistLimit) {
		t.Fatalf("entry bound error = %v", err)
	}

	deep := any(map[string]any{"video": map[string]any{"svtId": "deep-video"}})
	for range svtPageMaxJSONDepth + 1 {
		deep = map[string]any{"nested": deep}
	}
	if _, _, err := parseSVTPage(context.Background(), svtPageDocument(t, map[string]any{
		"page": map[string]any{"body": deep},
	})); !errors.Is(err, ErrInvalidMetadata) {
		t.Fatalf("depth bound error = %v", err)
	}

	nodes := make([]any, svtPageMaxJSONNodes+1)
	if _, _, err := parseSVTPage(context.Background(), svtPageDocument(t, map[string]any{
		"page": map[string]any{"body": nodes},
	})); !errors.Is(err, ErrInvalidMetadata) {
		t.Fatalf("node bound error = %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := parseSVTPage(ctx, svtFixture(t, "article-page.html")); !errors.Is(err, context.Canceled) {
		t.Fatalf("parser cancellation = %v", err)
	}
	transport := &svtPageFixtureTransport{body: svtFixture(t, "article-page.html")}
	if _, err := NewRegionSVT().Extract(ctx, Request{
		URL: "https://www.svt.se/news/article", Transport: transport,
	}); !errors.Is(err, context.Canceled) {
		t.Fatalf("request cancellation = %v", err)
	}
	if len(transport.requests) != 0 {
		t.Fatalf("canceled extraction made requests: %d", len(transport.requests))
	}
}

func FuzzRegionSVTPageRouting(f *testing.F) {
	for _, seed := range []string{
		"https://www.svt.se/news/fixture",
		"https://svt.se/sport/svensk-%C3%A5terkomst",
		"https://www.svt.se/news/encoded%2fseparator",
		"https://evil.example/news/fixture",
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, rawURL string) {
		if len(rawURL) > 1<<20 {
			t.Skip()
		}
		parsed, err := url.Parse(rawURL)
		if err != nil {
			return
		}
		target, ok := classifySVTPageURL(parsed)
		if !ok {
			return
		}
		canonical, err := url.Parse(target.canonical)
		if err != nil || canonical.Scheme != "https" || canonical.User != nil ||
			canonical.Port() != "" || canonical.RawQuery != "" || canonical.Fragment != "" ||
			(canonical.Hostname() != "svt.se" && canonical.Hostname() != "www.svt.se") {
			t.Fatalf("unsafe target %#v: %v", target, err)
		}
		roundTrip, ok := classifySVTPageURL(canonical)
		if !ok || roundTrip != target {
			t.Fatalf("round trip = %#v, %v; want %#v", roundTrip, ok, target)
		}
	})
}

func FuzzParseSVTPage(f *testing.F) {
	f.Add(svtFixture(f, "article-page.html"))
	f.Add([]byte(`<meta property="og:title" content="fixture"><script>urqlState={}</script>`))
	f.Fuzz(func(t *testing.T, page []byte) {
		if len(page) > 1<<20 {
			t.Skip()
		}
		title, entries, err := parseSVTPage(context.Background(), page)
		if err != nil {
			return
		}
		if title == "" || len(entries) == 0 || len(entries) > svtPageMaxEntries {
			t.Fatalf("unsafe successful parse: title=%q entries=%d", title, len(entries))
		}
		for _, entry := range entries {
			if !svtIDPattern.MatchString(entry.ID) || entry.URL != "svt:"+entry.ID ||
				entry.ExtractorKey != "region_svt" || !entry.Transparent {
				t.Fatalf("unsafe entry: %#v", entry)
			}
		}
	})
}

func svtPageDocument(t testing.TB, document any) []byte {
	t.Helper()
	return []byte(`<meta property="og:title" content="Fixture title">` + svtPageStateScript(t, document))
}

func svtPageStateScript(t testing.TB, document any) string {
	t.Helper()
	data, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	state, err := json.Marshal(map[string]any{"fixture": map[string]any{"data": string(data)}})
	if err != nil {
		t.Fatal(err)
	}
	return `<script>urqlState=` + string(state) + `</script>`
}
