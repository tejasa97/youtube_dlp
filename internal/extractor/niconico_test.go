package extractor

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
)

const niconicoFixtureRoot = "../../conformance/extractors/risk/niconico"

func readNiconicoFixture(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(niconicoFixtureRoot, name))
	if err != nil {
		t.Fatalf("read Niconico fixture %s: %v", name, err)
	}
	return data
}

type niconicoFixtureTransport struct {
	mu         sync.Mutex
	responses  map[string]fixtureResponse
	requests   []*http.Request
	headers    []http.Header
	pageBody   map[int][]byte
	seriesBody map[int][]byte
	userBody   map[int][]byte
	block      <-chan struct{}
}

type fixtureResponse struct {
	status int
	body   []byte
}

func newNiconicoFixtureTransport() *niconicoFixtureTransport {
	return &niconicoFixtureTransport{responses: make(map[string]fixtureResponse), pageBody: make(map[int][]byte), seriesBody: make(map[int][]byte), userBody: make(map[int][]byte)}
}

func (transport *niconicoFixtureTransport) set(method, path string, response fixtureResponse) {
	transport.mu.Lock()
	defer transport.mu.Unlock()
	transport.responses[method+" "+path] = response
}

func (transport *niconicoFixtureTransport) lookup(request *http.Request) fixtureResponse {
	transport.mu.Lock()
	defer transport.mu.Unlock()
	key := request.Method + " " + request.URL.Host + request.URL.Path
	if response, ok := transport.responses[key]; ok {
		return response
	}
	if request.URL.Path == "/api/watch/v3_guest/sm9" {
		return fixtureResponse{status: http.StatusOK, body: readNiconicoFixtureUnchecked("watch_guest.json")}
	}
	if request.URL.Path == "/v1/watch/sm9/access-rights/hls" {
		return fixtureResponse{status: http.StatusOK, body: readNiconicoFixtureUnchecked("access_rights.json")}
	}
	if request.URL.Host == "delivery.domand.nicovideo.jp" && request.URL.Path == "/fixture/master.m3u8" {
		return fixtureResponse{status: http.StatusOK, body: readNiconicoFixtureUnchecked("master.m3u8")}
	}
	if request.URL.Path == "/v2/mylists/99" {
		if request.URL.Query().Get("pageSize") == "1" {
			return fixtureResponse{status: http.StatusOK, body: []byte(`{"meta":{"status":200},"data":{"mylist":{"name":"Fixture mylist","description":"Fixture list","owner":{"id":"7","name":"Owner"}}}}`)}
		}
		page, _ := strconv.Atoi(request.URL.Query().Get("page"))
		if body, ok := transport.pageBody[page]; ok {
			return fixtureResponse{status: http.StatusOK, body: body}
		}
	}
	if request.URL.Path == "/v2/series/88" {
		if request.URL.Query().Get("pageSize") == "1" {
			return fixtureResponse{status: http.StatusOK, body: []byte(`{"meta":{"status":200},"data":{"detail":{"title":"Fixture series","description":"Series description","owner":{"id":"8","name":"Series owner"}}}}`)}
		}
		page, _ := strconv.Atoi(request.URL.Query().Get("page"))
		if body, ok := transport.seriesBody[page]; ok {
			return fixtureResponse{status: http.StatusOK, body: body}
		}
	}
	if request.URL.Path == "/v2/users/7/videos" {
		page, _ := strconv.Atoi(request.URL.Query().Get("page"))
		if body, ok := transport.userBody[page]; ok {
			return fixtureResponse{status: http.StatusOK, body: body}
		}
	}
	if strings.HasPrefix(request.URL.Path, "/search/") {
		page, _ := strconv.Atoi(request.URL.Query().Get("page"))
		if page == 1 {
			return fixtureResponse{status: http.StatusOK, body: []byte(`<div data-video-id="sm9"></div><div data-video-id="nm14296458"></div>`)}
		}
		return fixtureResponse{status: http.StatusOK, body: []byte(`<main></main>`)}
	}
	if strings.HasPrefix(request.URL.Path, "/tag/") {
		return fixtureResponse{status: http.StatusOK, body: []byte(`<div data-video-id="sm9"></div>`)}
	}
	return fixtureResponse{status: http.StatusNotFound, body: []byte(`not found`)}
}

func readNiconicoFixtureUnchecked(name string) []byte {
	data, _ := os.ReadFile(filepath.Join(niconicoFixtureRoot, name))
	return data
}

func (transport *niconicoFixtureTransport) do(ctx context.Context, request *http.Request) (*http.Response, error) {
	if transport.block != nil {
		select {
		case <-transport.block:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	transport.mu.Lock()
	transport.requests = append(transport.requests, request.Clone(ctx))
	transport.headers = append(transport.headers, request.Header.Clone())
	transport.mu.Unlock()
	fixture := transport.lookup(request)
	return &http.Response{StatusCode: fixture.status, Body: io.NopCloser(bytes.NewReader(fixture.body)), Header: make(http.Header), Request: request}, nil
}

func (transport *niconicoFixtureTransport) Do(ctx context.Context, request *http.Request) (*http.Response, error) {
	return transport.do(ctx, request)
}

func (transport *niconicoFixtureTransport) DoWithoutCredentialsNoRedirect(ctx context.Context, request *http.Request) (*http.Response, error) {
	return transport.do(ctx, request)
}

func (transport *niconicoFixtureTransport) ReadPage(ctx context.Context, rawURL string) ([]byte, http.Header, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, nil, err
	}
	response, err := transport.do(ctx, request)
	if err != nil {
		return nil, nil, err
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	return body, response.Header.Clone(), err
}

func TestNiconicoRouteMatrixAndDeferredSurfaces(t *testing.T) {
	registry := NewRegistry(NewNiconicoSearch(), NewNiconicoSearchURL(), NewNiconicoTag(), NewNiconicoPlaylist(), NewNiconicoSeries(), NewNiconicoUser(), NewNiconico())
	valid := map[string]string{
		"https://nicovideo.jp/watch/sm9":                                "niconico",
		"https://www.nicovideo.jp/shorts/ss123":                         "niconico",
		"https://embed.nicovideo.jp/watch/so123":                        "niconico",
		"https://nico.ms/mylist/99":                                     "niconico_playlist",
		"https://www.nicovideo.jp/user/7/mylist/99":                     "niconico_playlist",
		"https://www.nicovideo.jp/my/mylist/#/99":                       "niconico_playlist",
		"https://sp.nicovideo.jp/user/7/series/99":                      "niconico_series",
		"https://nico.ms/series/99/":                                    "niconico_series",
		"https://www.nicovideo.jp/search/sm9?sort=h&order=d":            "niconico_search_url",
		"https://nicovideo.jp/tag/%E3%83%95%E3%82%A3%E3%82%AF%E3%82%B9": "niconico_tag",
		"https://www.nicovideo.jp/user/7/video":                         "niconico_user",
		"nicosearch:fixture term":                                       "niconico_search",
	}
	for rawURL, want := range valid {
		selected, err := registry.Select(rawURL)
		if err != nil || selected.Name() != want {
			t.Errorf("route %q = %v, %v; want %s", rawURL, selected, err, want)
		}
	}
	for _, rawURL := range []string{
		"ftp://www.nicovideo.jp/watch/sm9",
		"https://www.nicovideo.jp:443/watch/sm9",
		"https://user:pass@www.nicovideo.jp/watch/sm9",
		"https://www.nicovideo.jp/watch/sm9?x=1",
		"https://www.nicovideo.jp/watch/sm9#x",
		"https://www.nicovideo.jp/watch/sm9%2Fextra",
		"https://www.nicovideo.jp/watch/sm9/extra",
		"https://nicovideo.jp.attacker.example/watch/sm9",
		"https://live.nicovideo.jp/watch/lv123",
		"https://www.nicovideo.jp/my/history",
		"nicosearchdate:fixture",
		"https://www.nicovideo.jp/search/fixture?sort=h&sort=d",
		"https://www.nicovideo.jp/search/fixture%2Fbad",
		"https://www.nicovideo.jp/tag/fixture?unknown=x",
		"https://www.nicovideo.jp/user/7?x=1",
	} {
		if _, err := registry.Select(rawURL); err == nil {
			t.Errorf("hostile/deferred URL was routed: %q", rawURL)
		}
	}
}

func TestNiconicoWatchMetadataAndAccessRightOutputMatrix(t *testing.T) {
	transport := newNiconicoFixtureTransport()
	result, err := NewNiconico().Extract(context.Background(), Request{
		URL: "https://www.nicovideo.jp/watch/sm9", Transport: transport,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.IsPlaylist() || result.IsURL() {
		t.Fatal("watch must return media")
	}
	if id, _ := result.Info.ID(); id != "sm9" {
		t.Fatalf("id=%q", id)
	}
	if title, _ := result.Info.Title(); title != "Fixture public watch" {
		t.Fatalf("title=%q", title)
	}
	if timestamp, _ := result.Info.Lookup("timestamp").Int(); timestamp != reparseTimestamp {
		t.Fatalf("timestamp=%d", timestamp)
	}
	formats, ok := result.Info.Formats()
	if !ok || len(formats) != 4 {
		t.Fatalf("formats=%d ok=%t", len(formats), ok)
	}
	wantIDs := []string{"a64", "a128", "v360", "v720"}
	for index, wantID := range wantIDs {
		format, _ := formats[index].Object()
		if got, _ := format.Lookup("format_id").StringValue(); got != wantID {
			t.Fatalf("format[%d] id=%q; want %q", index, got, wantID)
		}
		if got, _ := format.Lookup("url").StringValue(); !strings.HasPrefix(got, "https://delivery.domand.nicovideo.jp/") || !strings.Contains(got, "token=signed%2Fvalue&expires=1700000000") {
			t.Fatalf("format[%d] signed URL changed: %q", index, got)
		}
		if isolated, _ := format.Lookup("_credential_isolated").Bool(); !isolated {
			t.Fatalf("format[%d] is not credential isolated", index)
		}
		if scoped, _ := format.Lookup("_niconico_scoped").Bool(); !scoped {
			t.Fatalf("format[%d] is not Niconico scoped", index)
		}
		switch wantID {
		case "a64":
			abr, _ := format.Lookup("abr").Float()
			asr, _ := format.Lookup("asr").Int()
			if abr != 64 || asr != 48000 {
				t.Fatalf("a64 bitrate/sample rate=%v/%v", format.Lookup("abr"), format.Lookup("asr"))
			}
		case "a128":
			if abr, _ := format.Lookup("abr").Float(); abr != 128 {
				t.Fatalf("a128 bitrate=%v", abr)
			}
		case "v360":
			tbr, _ := format.Lookup("tbr").Float()
			vcodec, _ := format.Lookup("vcodec").StringValue()
			if tbr != 300 || vcodec != "avc1.64001f" {
				t.Fatalf("v360 bitrate/codec=%v/%v", format.Lookup("tbr"), format.Lookup("vcodec"))
			}
		case "v720":
			tbr, _ := format.Lookup("tbr").Float()
			vcodec, _ := format.Lookup("vcodec").StringValue()
			if tbr != 664 || vcodec != "avc1.640020" {
				t.Fatalf("v720 bitrate/codec=%v/%v", format.Lookup("tbr"), format.Lookup("vcodec"))
			}
		}
	}
	if len(transport.requests) != 3 || transport.requests[1].Method != http.MethodPost || transport.requests[2].Method != http.MethodGet {
		t.Fatalf("requests=%d %#v", len(transport.requests), transport.requests)
	}
	if got := transport.requests[1].Header.Get("X-Access-Right-Key"); got != "fixture-access-key==" {
		t.Fatalf("access key header=%q", got)
	}
	if got := transport.requests[0].URL.Query().Get("actionTrackId"); !strings.HasPrefix(got, "AAAAAAAAAA_") {
		t.Fatalf("guest action track id=%q", got)
	} else if _, err := strconv.ParseInt(strings.TrimPrefix(got, "AAAAAAAAAA_"), 10, 64); err != nil {
		t.Fatalf("guest action track id is not unix milliseconds: %q", got)
	}
	if got, want := transport.requests[1].URL.Query().Get("actionTrackId"), "fixture-watch-track"; got != want {
		t.Fatalf("access-right action track id=%q; want %q", got, want)
	}
}

const reparseTimestamp = 1704164645

func TestNiconicoWatchStatusMappingAndSecretSafety(t *testing.T) {
	cases := []struct {
		name   string
		status int
		reason string
		cause  error
		kind   string
	}{
		{"auth", http.StatusUnauthorized, "", ErrAuthentication, "authentication"},
		{"bad-request", http.StatusBadRequest, "", ErrUnavailable, "request"},
		{"forbidden", http.StatusForbidden, "", ErrAuthentication, "authentication"},
		{"missing", http.StatusNotFound, "", ErrUnavailable, "unavailable"},
		{"gone", http.StatusGone, "", ErrUnavailable, "unavailable"},
		{"rate", http.StatusTooManyRequests, "", ErrNiconicoRateLimit, "rate_limit"},
		{"geo", http.StatusUnavailableForLegalReasons, "", ErrRegionRestricted, "geo"},
		{"server", http.StatusBadGateway, "", ErrNiconicoServer, "server"},
		{"redirect", http.StatusFound, "", ErrUnavailable, "redirect"},
		{"premium", http.StatusOK, "PREMIUM_ONLY", ErrNiconicoPremium, "premium"},
		{"member", http.StatusOK, "CHANNEL_MEMBER_ONLY", ErrNiconicoMember, "member"},
		{"ppv", http.StatusOK, "PPV_VIDEO", ErrNiconicoPPV, "ppv"},
		{"sensitive", http.StatusOK, "HARMFUL_VIDEO", ErrNiconicoSensitive, "sensitive"},
		{"scheduled", http.StatusOK, "HIDDEN_VIDEO", ErrNiconicoScheduled, "scheduled"},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			transport := newNiconicoFixtureTransport()
			metaStatus := test.status
			if metaStatus == http.StatusOK {
				metaStatus = http.StatusForbidden
			}
			transport.set("GET", "nvapi.nicovideo.jp/api/watch/v3_guest/sm9", fixtureResponse{
				status: test.status,
				body:   []byte(`{"meta":{"status":` + strconv.Itoa(metaStatus) + `},"data":{"reasonCode":"` + test.reason + `","publishScheduledAt":"2027-01-01T00:00:00Z"}}`),
			})
			_, err := NewNiconico().Extract(context.Background(), Request{URL: "https://www.nicovideo.jp/watch/sm9", Transport: transport})
			if err == nil || !errors.Is(err, test.cause) {
				t.Fatalf("error=%v; want %v", err, test.cause)
			}
			var typed *NiconicoError
			if !errors.As(err, &typed) || typed.Kind != test.kind {
				t.Fatalf("typed=%#v; want kind %s", typed, test.kind)
			}
			for _, secret := range []string{"fixture-access-key", "fixture-watch-track", "signed%2Fvalue", test.reason} {
				if secret != "" && strings.Contains(err.Error(), secret) {
					t.Fatalf("secret %q leaked in %q", secret, err)
				}
			}
		})
	}
}

func TestNiconicoCollectionPaginationIsReusableAndAdvancesBySourceRows(t *testing.T) {
	transport := newNiconicoFixtureTransport()
	transport.pageBody[1] = niconicoCollectionPage(100, 1)
	transport.pageBody[2] = niconicoCollectionPage(1, 101)
	result, err := NewNiconicoPlaylist().Extract(context.Background(), Request{URL: "https://nico.ms/mylist/99", Transport: transport})
	if err != nil {
		t.Fatal(err)
	}
	first := result.Entries.Iterator()
	firstIDs := make([]string, 0, 101)
	for index := 0; index < 2; index++ {
		entry, ok, err := first.Next(context.Background())
		if err != nil || !ok || entry.ID == "" {
			t.Fatalf("first iterator entry %d = %#v, %t, %v", index, entry, ok, err)
		}
		firstIDs = append(firstIDs, entry.ID)
	}
	transport.mu.Lock()
	if len(transport.requests) != 2 {
		t.Fatalf("partial consumption fetched %d requests", len(transport.requests))
	}
	transport.mu.Unlock()
	for {
		entry, ok, err := first.Next(context.Background())
		if err != nil || !ok {
			if err != nil {
				t.Fatal(err)
			}
			break
		}
		firstIDs = append(firstIDs, entry.ID)
	}
	second := result.Entries.Iterator()
	entry, ok, err := second.Next(context.Background())
	if err != nil || !ok || entry.ID != "sm1" {
		t.Fatalf("reusable iterator first=%#v %t %v", entry, ok, err)
	}
	secondIDs := []string{entry.ID}
	for {
		entry, ok, err := second.Next(context.Background())
		if err != nil || !ok {
			if err != nil {
				t.Fatal(err)
			}
			break
		}
		secondIDs = append(secondIDs, entry.ID)
	}
	if strings.Join(firstIDs, ",") != strings.Join(secondIDs, ",") || len(firstIDs) != 101 {
		t.Fatalf("reusable iterator IDs first=%d second=%d", len(firstIDs), len(secondIDs))
	}
	transport.mu.Lock()
	requests := len(transport.requests)
	transport.mu.Unlock()
	if requests != 5 {
		t.Fatalf("reusable pagination requests=%d; want metadata + two page1/page2 sequences", requests)
	}
}

func TestNiconicoSeriesAndUserAPIChildShapes(t *testing.T) {
	for _, test := range []struct {
		name       string
		path       string
		page1      string
		page2      string
		extract    func(context.Context, Request) (Extraction, error)
		firstPages int
	}{
		{
			name: "series", path: "/v2/series/88", page1: "series_page_1.json", page2: "series_page_2.json",
			extract: func(ctx context.Context, request Request) (Extraction, error) {
				return NewNiconicoSeries().Extract(ctx, request)
			}, firstPages: 1,
		},
		{
			name: "user", path: "/v2/users/7/videos", page1: "user_page_1.json", page2: "user_page_2.json",
			extract: func(ctx context.Context, request Request) (Extraction, error) {
				return NewNiconicoUser().Extract(ctx, request)
			}, firstPages: 1,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			transport := newNiconicoFixtureTransport()
			if test.name == "series" {
				transport.seriesBody[1] = readNiconicoFixture(t, test.page1)
				transport.seriesBody[2] = readNiconicoFixture(t, test.page2)
			} else {
				transport.userBody[1] = readNiconicoFixture(t, test.page1)
				transport.userBody[2] = readNiconicoFixture(t, test.page2)
			}
			extraction, err := test.extract(context.Background(), Request{URL: map[string]string{
				"series": "https://www.nicovideo.jp/series/88",
				"user":   "https://www.nicovideo.jp/user/7/video",
			}[test.name], Transport: transport})
			if err != nil {
				t.Fatal(err)
			}
			first := extraction.Entries.Iterator()
			entry, ok, err := first.Next(context.Background())
			if err != nil || !ok || entry.ID != "sm10" || entry.ExtractorKey != "niconico" {
				t.Fatalf("first child=%#v %t %v", entry, ok, err)
			}
			if got := niconicoPageRequestCount(transport, test.path); got != test.firstPages {
				t.Fatalf("partial consumption fetched %d source pages; want %d", got, test.firstPages)
			}
			firstIDs := []string{entry.ID}
			for {
				entry, ok, err := first.Next(context.Background())
				if err != nil {
					t.Fatal(err)
				}
				if !ok {
					break
				}
				if entry.ExtractorKey != "niconico" {
					t.Fatalf("child extractor key=%q", entry.ExtractorKey)
				}
				firstIDs = append(firstIDs, entry.ID)
			}
			second := extraction.Entries.Iterator()
			secondIDs := make([]string, 0, len(firstIDs))
			for {
				entry, ok, err := second.Next(context.Background())
				if err != nil {
					t.Fatal(err)
				}
				if !ok {
					break
				}
				secondIDs = append(secondIDs, entry.ID)
			}
			if strings.Join(firstIDs, ",") != strings.Join(secondIDs, ",") || strings.Join(firstIDs, ",") != "sm10,sm11,sm12" {
				t.Fatalf("reusable IDs first=%v second=%v", firstIDs, secondIDs)
			}
			if got := niconicoPageRequestCount(transport, test.path); got != 4 {
				t.Fatalf("reusable sequence fetched %d source pages; want 4", got)
			}
		})
	}
}

func niconicoPageRequestCount(transport *niconicoFixtureTransport, path string) int {
	transport.mu.Lock()
	defer transport.mu.Unlock()
	count := 0
	for _, request := range transport.requests {
		if request.URL.Path == path && request.URL.Query().Get("page") != "" {
			count++
		}
	}
	return count
}

func niconicoCollectionPage(count, start int) []byte {
	items := make([]map[string]any, 0, count)
	for index := 0; index < count; index++ {
		id := "sm" + strconv.Itoa(start+index)
		items = append(items, map[string]any{"video": map[string]any{"id": id, "title": "Fixture " + id, "count": map[string]any{"view": index}}})
	}
	body, _ := json.Marshal(map[string]any{"meta": map[string]any{"status": 200}, "data": map[string]any{"mylist": map[string]any{"items": items}}})
	return body
}

func TestNiconicoSearchTagAndCancellation(t *testing.T) {
	transport := newNiconicoFixtureTransport()
	search, err := NewNiconicoSearchURL().Extract(context.Background(), Request{
		URL: "https://www.nicovideo.jp/search/sm9?sort=h&order=d", Transport: transport,
	})
	if err != nil {
		t.Fatal(err)
	}
	entry, ok, err := search.Entries.Iterator().Next(context.Background())
	if err != nil || !ok || entry.ID != "sm9" {
		t.Fatalf("search entry=%#v %t %v", entry, ok, err)
	}
	transport.mu.Lock()
	if len(transport.requests) != 1 || transport.requests[0].URL.Query().Get("sort") != "h" || transport.requests[0].URL.Query().Get("page") != "1" {
		t.Fatalf("search request=%#v", transport.requests)
	}
	transport.mu.Unlock()
	tag, err := NewNiconicoTag().Extract(context.Background(), Request{URL: "https://www.nicovideo.jp/tag/fixture", Transport: transport})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok, err := tag.Entries.Iterator().Next(context.Background()); err != nil || !ok {
		t.Fatalf("tag first entry=%t %v", ok, err)
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	iterator := search.Entries.Iterator()
	_, ok, err = iterator.Next(cancelled)
	if !errors.Is(err, context.Canceled) || ok {
		t.Fatalf("cancelled iteration=%t %v", ok, err)
	}
}

func TestNiconicoMalformedOversizedAndRepeatedResponsesFailClosed(t *testing.T) {
	for _, test := range []struct {
		name string
		body []byte
		want error
	}{
		{name: "empty", body: nil, want: ErrInvalidMetadata},
		{name: "malformed", body: []byte(`{"meta":`), want: ErrInvalidMetadata},
		{name: "trailing", body: []byte(`{"meta":{"status":200},"data":{}} {}`), want: ErrInvalidMetadata},
		{name: "oversized", body: bytes.Repeat([]byte("x"), int(maxExtractorJSONBytes)+1), want: ErrJSONResponseTooLarge},
	} {
		t.Run(test.name, func(t *testing.T) {
			transport := newNiconicoFixtureTransport()
			transport.set("GET", "nvapi.nicovideo.jp/api/watch/v3_guest/sm9", fixtureResponse{status: http.StatusOK, body: test.body})
			_, err := NewNiconico().Extract(context.Background(), Request{URL: "https://www.nicovideo.jp/watch/sm9", Transport: transport})
			if err == nil || !errors.Is(err, test.want) {
				t.Fatalf("error=%v; want %v", err, test.want)
			}
		})
	}

	transport := newNiconicoFixtureTransport()
	transport.pageBody[1] = niconicoCollectionPage(100, 1)
	transport.pageBody[2] = niconicoCollectionPage(100, 1)
	result, err := NewNiconicoPlaylist().Extract(context.Background(), Request{URL: "https://www.nicovideo.jp/mylist/99", Transport: transport})
	if err != nil {
		t.Fatal(err)
	}
	iterator := result.Entries.Iterator()
	for index := 0; index < 100; index++ {
		if _, ok, err := iterator.Next(context.Background()); err != nil || !ok {
			t.Fatalf("entry %d = %t, %v", index, ok, err)
		}
	}
	if _, _, err := iterator.Next(context.Background()); !errors.Is(err, ErrInvalidPlaylist) {
		t.Fatalf("repeated page error=%v", err)
	}

	transport = newNiconicoFixtureTransport()
	invalidRows, _ := json.Marshal(map[string]any{
		"meta": map[string]any{"status": 200}, "data": map[string]any{"mylist": map[string]any{
			"items": []any{map[string]any{"video": map[string]any{}}, map[string]any{"video": map[string]any{"id": "bad/id"}}, map[string]any{"video": map[string]any{"id": "sm77"}}},
		}}})
	transport.pageBody[1] = invalidRows
	result, err = NewNiconicoPlaylist().Extract(context.Background(), Request{URL: "https://www.nicovideo.jp/mylist/99", Transport: transport})
	if err != nil {
		t.Fatal(err)
	}
	entry, ok, err := result.Entries.Iterator().Next(context.Background())
	if err != nil || !ok || entry.ID != "sm77" {
		t.Fatalf("invalid rows entry=%#v %t %v", entry, ok, err)
	}
}

func TestNiconicoHostPoliciesRejectSignedHostiles(t *testing.T) {
	for _, rawURL := range []string{
		"https://delivery.domand.nicovideo.jp/fixture/master.m3u8?token=signed%2Fvalue",
		"https://img.cdn.nimg.jp/fixture/thumb.jpg",
	} {
		if !NiconicoMediaURLAllowed(rawURL) && strings.Contains(rawURL, "master") {
			t.Fatalf("valid media URL rejected: %s", rawURL)
		}
	}
	for _, rawURL := range []string{
		"http://delivery.domand.nicovideo.jp/fixture/master.m3u8",
		"https://delivery.domand.nicovideo.jp:443/fixture/master.m3u8",
		"https://user:pass@delivery.domand.nicovideo.jp/fixture/master.m3u8",
		"https://delivery.domand.nicovideo.jp/fixture/master.m3u8#fragment",
		"https://video.dmc.nico/fixture/master.m3u8",
		"https://sub.delivery.domand.nicovideo.jp/fixture/master.m3u8",
		"https://evil.example/fixture/master.m3u8",
		"https://delivery.domand.nicovideo.jp/fixture/%2Fescape.ts",
	} {
		if NiconicoMediaURLAllowed(rawURL) {
			t.Errorf("hostile media URL accepted: %s", rawURL)
		}
	}
}

func TestNiconicoMasterHostileURLFailsClosed(t *testing.T) {
	transport := newNiconicoFixtureTransport()
	master := bytes.Replace(
		readNiconicoFixture(t, "master.m3u8"),
		[]byte("video/v720.m3u8?token=signed%2Fvalue&expires=1700000000"),
		[]byte("https://evil.example/video/v720.m3u8"), 1,
	)
	transport.set("GET", "delivery.domand.nicovideo.jp/fixture/master.m3u8", fixtureResponse{status: http.StatusOK, body: master})
	_, err := NewNiconico().Extract(context.Background(), Request{URL: "https://www.nicovideo.jp/watch/sm9", Transport: transport})
	if err == nil {
		t.Fatal("hostile master URL unexpectedly succeeded")
	}
	var typed *NiconicoError
	if !errors.As(err, &typed) || typed.Kind != "invalid_host" {
		t.Fatalf("error=%v; want invalid_host", err)
	}
}

func FuzzNiconicoHLSMaster(f *testing.F) {
	fixture, err := os.ReadFile(filepath.Join(niconicoFixtureRoot, "master.m3u8"))
	if err != nil {
		f.Fatal(err)
	}
	f.Add(fixture)
	f.Add([]byte("#EXTM3U\n#EXT-X-STREAM-INF:BANDWIDTH=1,CODECS=\"avc1.4d401e\"\nv360.m3u8\n"))
	f.Fuzz(func(t *testing.T, body []byte) {
		if len(body) > niconicoPageLimit {
			t.Skip()
		}
		_, _ = niconicoHLSFormats(
			"https://delivery.domand.nicovideo.jp/fixture/master.m3u8?token=signed%2Fvalue",
			body,
			[]niconicoTrack{{ID: "v360", IsAvailable: true, QualityLevel: 1}},
			[]niconicoTrack{{ID: "a64", IsAvailable: true, BitRate: 64000, SamplingRate: 48000, QualityLevel: 1}},
		)
	})
}
