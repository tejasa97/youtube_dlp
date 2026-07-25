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
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

const svtFixtureRoot = "../../conformance/extractors/region-svt"

type svtFixtureTransport struct {
	mu                        sync.Mutex
	page                      []byte
	video                     []byte
	series                    []byte
	status                    int
	seriesStatus              int
	apiCalls                  []string
	graphqlCalls              []string
	credentialIsolatedCalls   int
	lastCredentialIsolatedReq *http.Request
	wait                      bool
}

func (transport *svtFixtureTransport) Do(ctx context.Context, request *http.Request) (*http.Response, error) {
	if transport.wait {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !strings.HasPrefix(request.URL.String(), svtVideoAPIBase) {
		return nil, fmt.Errorf("unexpected SVT API URL %s", request.URL.Redacted())
	}
	transport.mu.Lock()
	transport.apiCalls = append(transport.apiCalls, request.URL.String())
	transport.mu.Unlock()
	status := transport.status
	if status == 0 {
		status = http.StatusOK
	}
	return &http.Response{
		StatusCode: status,
		Header:     make(http.Header),
		Body:       io.NopCloser(bytes.NewReader(transport.video)),
		Request:    request,
	}, nil
}

func (transport *svtFixtureTransport) DoWithoutCredentialsNoRedirect(ctx context.Context, request *http.Request) (*http.Response, error) {
	if transport.wait {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	for _, header := range []string{"Cookie", "Authorization", "Proxy-Authorization"} {
		if request.Header.Get(header) != "" {
			return nil, fmt.Errorf("%s reached isolated SVT request", header)
		}
	}
	if request.Method != http.MethodGet {
		return nil, fmt.Errorf("unexpected SVT GraphQL method %s", request.Method)
	}
	if request.URL.Scheme != "https" || request.URL.Host != "api.svt.se" || request.URL.Path != "/contento/graphql" {
		return nil, fmt.Errorf("unexpected SVT GraphQL URL %s", request.URL.Redacted())
	}
	transport.mu.Lock()
	transport.credentialIsolatedCalls++
	transport.graphqlCalls = append(transport.graphqlCalls, request.URL.String())
	transport.lastCredentialIsolatedReq = request.Clone(ctx)
	transport.mu.Unlock()
	status := transport.seriesStatus
	if status == 0 {
		status = http.StatusOK
	}
	return &http.Response{
		StatusCode: status,
		Header:     make(http.Header),
		Body:       io.NopCloser(bytes.NewReader(transport.series)),
		Request:    request,
	}, nil
}

func (transport *svtFixtureTransport) ReadPage(ctx context.Context, rawURL string) ([]byte, http.Header, error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Hostname() != "www.svtplay.se" {
		return nil, nil, fmt.Errorf("unexpected SVT page URL")
	}
	return append([]byte(nil), transport.page...), make(http.Header), nil
}

func svtFixture(t testing.TB, name string) []byte {
	t.Helper()
	body, err := os.ReadFile(filepath.Join(svtFixtureRoot, name))
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func TestRegionSVTSuitable(t *testing.T) {
	t.Parallel()
	tests := []struct {
		rawURL string
		want   bool
	}{
		{"https://www.svtplay.se/video/eXYgwZb/program", true},
		{"https://svtplay.se/klipp/9023742", true},
		{"http://www.oppetarkiv.se/video/5219710", true},
		{"https://www.svtplay.se/kanaler/svt1", true},
		{"https://www.svtplay.se/rederiet", true},
		{"https://www.svtplay.se/rederiet?tab=season-2-jpmQYgn", true},
		{"svt:svt-fixture-001", true},
		{"https://www.svtplay.se/rederiet/extra", false},
		{"http://www.svtplay.se/rederiet", false},
		{"https://www.svtplay.se/rederiet#season", false},
		{"https://www.svtplay.se/rederiet?tab=", false},
		{"https://www.svtplay.se/rederiet?tab=season-2-jpmQYgn&tab=season-1-jpmQYgn", false},
		{"https://www.svtplay.se/rederiet?secret=token", false},
		{"https://www.svtplay.se/%72ederiet", false},
		{"svt://www.svtplay.se/svt-fixture-001", false},
		{"svt:/svt-fixture-001", false},
		{"svt:svt-fixture-001?modalId=x", false},
		{"svt:svt-fixture-001#frag", false},
		{"https://www.svtplay.se/", false},
		{"https://www.oppetarkiv.se/rederiet", false},
		{"https://evil.svtplay.se.evil/rederiet", false},
		{"https://user@www.svtplay.se/rederiet", false},
		{"https://www.svtplay.se:8443/rederiet", false},
		{"https://www.svt.se/news/article", false},
		{"ftp://www.svtplay.se/video/id", false},
		{"https://www.svtplay.se/rederiet?tab=bad/id", false},
	}
	for _, test := range tests {
		parsed, err := url.Parse(test.rawURL)
		if err != nil {
			t.Fatal(err)
		}
		if got := NewRegionSVT().Suitable(parsed); got != test.want {
			t.Errorf("Suitable(%q) = %t, want %t", test.rawURL, got, test.want)
		}
	}
}

func TestRegionSVTExtractExplicitID(t *testing.T) {
	transport := &svtFixtureTransport{video: svtFixture(t, "video.json")}
	result, err := NewRegionSVT().Extract(context.Background(), Request{
		URL: "https://www.svtplay.se/video/page-slug?modalId=svt-fixture-001", Transport: transport,
	})
	if err != nil {
		t.Fatal(err)
	}
	assertSVTExpected(t, result)
	if len(transport.apiCalls) != 1 || transport.apiCalls[0] != svtVideoAPIBase+"svt-fixture-001" {
		t.Fatalf("API calls = %#v", transport.apiCalls)
	}
}

func TestRegionSVTExtractPseudoURL(t *testing.T) {
	transport := &svtFixtureTransport{video: svtFixture(t, "video.json")}
	result, err := NewRegionSVT().Extract(context.Background(), Request{
		URL: "svt:svt-fixture-001", Transport: transport,
	})
	if err != nil {
		t.Fatal(err)
	}
	if id, _ := result.Info.ID(); id != "svt-fixture-001" {
		t.Fatalf("id = %q", id)
	}
}

func TestRegionSVTDiscoversIDFromPage(t *testing.T) {
	transport := &svtFixtureTransport{page: svtFixture(t, "page.html"), video: svtFixture(t, "video.json")}
	result, err := NewRegionSVT().Extract(context.Background(), Request{
		URL: "https://www.svtplay.se/video/page-slug", Transport: transport,
	})
	if err != nil {
		t.Fatal(err)
	}
	if id, _ := result.Info.ID(); id != "svt-fixture-001" {
		t.Fatalf("id = %q", id)
	}
}

func TestRegionSVTSeriesPlaylistAllSeasons(t *testing.T) {
	transport := &svtFixtureTransport{series: svtFixture(t, "series.json")}
	result, err := NewRegionSVT().Extract(context.Background(), Request{
		URL: "https://www.svtplay.se/rederiet", Transport: transport,
	})
	if err != nil {
		t.Fatal(err)
	}
	assertSVTSeriesExpected(t, result, false)
	if len(transport.graphqlCalls) != 1 || transport.credentialIsolatedCalls != 1 {
		t.Fatalf("graphql calls = %#v isolated=%d", transport.graphqlCalls, transport.credentialIsolatedCalls)
	}
	if webpage, _ := result.Info.Lookup("webpage_url").StringValue(); webpage != "https://www.svtplay.se/rederiet" {
		t.Fatalf("webpage_url = %q", webpage)
	}
	transport.mu.Lock()
	lastRequest := transport.lastCredentialIsolatedReq
	transport.mu.Unlock()
	if lastRequest == nil {
		t.Fatal("missing credential-isolated request")
	}
	for _, header := range []string{"Cookie", "Authorization", "Proxy-Authorization"} {
		if lastRequest.Header.Get(header) != "" {
			t.Fatalf("%s header present on GraphQL request", header)
		}
	}
	parsed, err := url.Parse(transport.graphqlCalls[0])
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Scheme != "https" || parsed.Host != "api.svt.se" || parsed.Path != "/contento/graphql" {
		t.Fatalf("graphql URL = %s", parsed.Redacted())
	}
	query := parsed.Query().Get("query")
	if !strings.Contains(query, `"rederiet"`) || strings.Contains(query, `slugs: [rederiet]`) {
		t.Fatalf("query slug encoding = %q", query)
	}
	if len(transport.apiCalls) != 0 {
		t.Fatalf("video API called during playlist extraction: %#v", transport.apiCalls)
	}
}

func TestRegionSVTSeriesPlaylistSeasonTab(t *testing.T) {
	transport := &svtFixtureTransport{series: svtFixture(t, "series.json")}
	result, err := NewRegionSVT().Extract(context.Background(), Request{
		URL: "https://www.svtplay.se/rederiet?tab=season-2-jpmQYgn", Transport: transport,
	})
	if err != nil {
		t.Fatal(err)
	}
	assertSVTSeriesExpected(t, result, true)
	if webpage, _ := result.Info.Lookup("webpage_url").StringValue(); webpage != "https://www.svtplay.se/rederiet?tab=season-2-jpmQYgn" {
		t.Fatalf("webpage_url = %q", webpage)
	}
}

func TestRegionSVTSeriesPlaylistLazyReentry(t *testing.T) {
	transport := &svtFixtureTransport{
		series: svtFixture(t, "series.json"),
		video:  svtFixture(t, "video.json"),
	}
	playlist, err := NewRegionSVT().Extract(context.Background(), Request{
		URL: "https://www.svtplay.se/rederiet?tab=season-2-jpmQYgn", Transport: transport,
	})
	if err != nil {
		t.Fatal(err)
	}
	entries, err := CollectEntries(context.Background(), playlist.Entries, 10)
	if err != nil || len(entries) != 2 {
		t.Fatalf("entries=%#v err=%v", entries, err)
	}
	if entries[0].URL != "svt:svt-fixture-s02e01" || entries[0].ExtractorKey != "region_svt" {
		t.Fatalf("entry = %#v", entries[0])
	}
	if len(transport.apiCalls) != 0 {
		t.Fatalf("video API called before entry iteration: %#v", transport.apiCalls)
	}
	media, err := NewRegionSVT().Extract(context.Background(), Request{URL: entries[0].URL, Transport: transport})
	if err != nil {
		t.Fatal(err)
	}
	if id, _ := media.Info.ID(); id != "svt-fixture-s02e01" {
		t.Fatalf("re-entry id = %q", id)
	}
	if len(transport.apiCalls) != 1 {
		t.Fatalf("video API calls after re-entry = %#v", transport.apiCalls)
	}
}

func TestRegionSVTSeriesRejectedURLsMakeNoRequests(t *testing.T) {
	transport := &svtFixtureTransport{series: svtFixture(t, "series.json"), video: svtFixture(t, "video.json")}
	for _, rawURL := range []string{
		"http://www.svtplay.se/rederiet",
		"https://www.svtplay.se/rederiet?secret=token",
		"https://www.svtplay.se/rederiet?tab=season-2-jpmQYgn&tab=season-1-jpmQYgn",
		"https://www.svtplay.se/rederiet#season",
		"https://www.svtplay.se/rederiet?tab=",
		"https://www.svtplay.se/%72ederiet",
		"svt://host/svt-fixture-001",
		"svt:/svt-fixture-001",
	} {
		transport.apiCalls = nil
		transport.graphqlCalls = nil
		transport.credentialIsolatedCalls = 0
		_, err := NewRegionSVT().Extract(context.Background(), Request{URL: rawURL, Transport: transport})
		if !errors.Is(err, ErrUnsupported) {
			t.Fatalf("Extract(%q) error = %v, want ErrUnsupported", rawURL, err)
		}
		if len(transport.apiCalls) != 0 || len(transport.graphqlCalls) != 0 || transport.credentialIsolatedCalls != 0 {
			t.Fatalf("Extract(%q) made requests: api=%#v graphql=%#v isolated=%d", rawURL, transport.apiCalls, transport.graphqlCalls, transport.credentialIsolatedCalls)
		}
	}
}

func TestRegionSVTSeriesFailureCategories(t *testing.T) {
	secretBody := []byte(`{"data":{"listablesBySlug":[]},"secret":"svt-private-token"}`)
	tests := []struct {
		name         string
		series       []byte
		seriesStatus int
		rawURL       string
		want         error
	}{
		{name: "missing series", series: []byte(`{"data":{"listablesBySlug":[]}}`), rawURL: "https://www.svtplay.se/rederiet", want: ErrUnavailable},
		{name: "unknown season", series: svtFixture(t, "series.json"), rawURL: "https://www.svtplay.se/rederiet?tab=season-9-missing", want: ErrUnavailable},
		{name: "malformed JSON", series: []byte(`{`), rawURL: "https://www.svtplay.se/rederiet", want: ErrInvalidMetadata},
		{name: "graphql errors", series: []byte(`{"errors":[{"message":"denied"}]}`), rawURL: "https://www.svtplay.se/rederiet", want: ErrInvalidMetadata},
		{name: "geo status", seriesStatus: http.StatusForbidden, rawURL: "https://www.svtplay.se/rederiet", want: ErrRegionRestricted},
		{name: "gone status", seriesStatus: http.StatusGone, rawURL: "https://www.svtplay.se/rederiet", want: ErrUnavailable},
		{name: "secret body", series: secretBody, rawURL: "https://www.svtplay.se/rederiet", want: ErrUnavailable},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			transport := &svtFixtureTransport{series: test.series, seriesStatus: test.seriesStatus}
			_, err := NewRegionSVT().Extract(context.Background(), Request{URL: test.rawURL, Transport: transport})
			if !errors.Is(err, test.want) {
				t.Fatalf("Extract() error = %v, want %v", err, test.want)
			}
			if err != nil && strings.Contains(err.Error(), "svt-private-token") {
				t.Fatalf("secret leaked: %v", err)
			}
		})
	}
}

func TestRegionSVTSeriesBoundsAndCancellation(t *testing.T) {
	oversizedSeasons := make([]map[string]any, 0, svtMaxSeriesSeasons+1)
	for index := 0; index <= svtMaxSeriesSeasons; index++ {
		oversizedSeasons = append(oversizedSeasons, map[string]any{
			"id": fmt.Sprintf("season-%d", index), "name": "Season", "items": []any{},
		})
	}
	oversizedSeasonBody, _ := json.Marshal(map[string]any{
		"data": map[string]any{"listablesBySlug": []any{map[string]any{
			"id": "bound-series", "name": "Bound", "associatedContent": oversizedSeasons,
		}}},
	})
	transport := &svtFixtureTransport{series: oversizedSeasonBody}
	_, err := NewRegionSVT().Extract(context.Background(), Request{
		URL: "https://www.svtplay.se/rederiet", Transport: transport,
	})
	if !errors.Is(err, ErrPlaylistLimit) {
		t.Fatalf("season bound error = %v", err)
	}

	items := make([]map[string]any, 0, svtMaxSeriesItemsPerSeason+1)
	for index := 0; index <= svtMaxSeriesItemsPerSeason; index++ {
		items = append(items, map[string]any{"item": map[string]any{"videoSvtId": fmt.Sprintf("vid-%04d", index)}})
	}
	oversizedItemsBody, _ := json.Marshal(map[string]any{
		"data": map[string]any{"listablesBySlug": []any{map[string]any{
			"id": "bound-series", "name": "Bound",
			"associatedContent": []any{map[string]any{"id": "season-1", "name": "Season", "items": items}},
		}}},
	})
	transport = &svtFixtureTransport{series: oversizedItemsBody}
	_, err = NewRegionSVT().Extract(context.Background(), Request{
		URL: "https://www.svtplay.se/rederiet", Transport: transport,
	})
	if !errors.Is(err, ErrPlaylistLimit) {
		t.Fatalf("item bound error = %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = NewRegionSVT().Extract(ctx, Request{
		URL: "https://www.svtplay.se/rederiet", Transport: &svtFixtureTransport{series: svtFixture(t, "series.json"), wait: true},
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation error = %v", err)
	}

	longName := strings.Repeat("x", svtMaxSeriesMetadataNameLength+1)
	metadataBody, _ := json.Marshal(map[string]any{
		"data": map[string]any{"listablesBySlug": []any{map[string]any{
			"id": "jpmQYgn", "name": longName, "associatedContent": []any{},
		}}},
	})
	_, err = NewRegionSVT().Extract(context.Background(), Request{
		URL: "https://www.svtplay.se/rederiet", Transport: &svtFixtureTransport{series: metadataBody},
	})
	if !errors.Is(err, ErrInvalidMetadata) {
		t.Fatalf("metadata bound error = %v", err)
	}
}

func TestRegionSVTSeriesRequiresCredentialIsolation(t *testing.T) {
	_, err := NewRegionSVT().Extract(context.Background(), Request{
		URL: "https://www.svtplay.se/rederiet", Transport: svtNonIsolatedTransport{},
	})
	if !errors.Is(err, ErrTransportIsolation) {
		t.Fatalf("non-isolated transport error = %v", err)
	}

	cookiesOnly := &svtCookiesOnlyTransport{}
	_, err = NewRegionSVT().Extract(context.Background(), Request{
		URL: "https://www.svtplay.se/rederiet", Transport: cookiesOnly,
	})
	if !errors.Is(err, ErrTransportIsolation) {
		t.Fatalf("cookies-only transport error = %v", err)
	}
	if cookiesOnly.calls != 0 {
		t.Fatalf("cookies-only transport calls = %d", cookiesOnly.calls)
	}
}

type svtNonIsolatedTransport struct{}

func (svtNonIsolatedTransport) Do(context.Context, *http.Request) (*http.Response, error) {
	return nil, errors.New("unexpected request")
}

func (svtNonIsolatedTransport) ReadPage(context.Context, string) ([]byte, http.Header, error) {
	return nil, nil, errors.New("unexpected page read")
}

type svtCookiesOnlyTransport struct {
	calls int
}

func (transport *svtCookiesOnlyTransport) Do(context.Context, *http.Request) (*http.Response, error) {
	return nil, errors.New("unexpected request")
}

func (transport *svtCookiesOnlyTransport) ReadPage(context.Context, string) ([]byte, http.Header, error) {
	return nil, nil, errors.New("unexpected page read")
}

func (transport *svtCookiesOnlyTransport) DoWithoutCookies(context.Context, *http.Request) (*http.Response, error) {
	transport.calls++
	return nil, errors.New("cookies-only transport used")
}

func TestRegionSVTLiveUsesLiveHLSProtocol(t *testing.T) {
	var response svtVideoResponse
	if err := json.Unmarshal(svtFixture(t, "video.json"), &response); err != nil {
		t.Fatal(err)
	}
	response.Live = true
	result, err := normalizeSVTVideo(response, "svt-fixture-001", "https://www.svtplay.se/video/page-slug")
	if err != nil {
		t.Fatal(err)
	}
	if live, ok := result.Info.Lookup("is_live").Bool(); !ok || !live {
		t.Fatalf("is_live = %t, %t", live, ok)
	}
	formats, _ := result.Info.Formats()
	format, _ := formats[0].Object()
	if protocol, _ := format.Lookup("protocol").StringValue(); protocol != "m3u8_native" {
		t.Fatalf("live HLS protocol = %q", protocol)
	}
}

func TestRegionSVTCategorizesFailures(t *testing.T) {
	valid := svtFixture(t, "video.json")
	tests := []struct {
		name   string
		video  []byte
		status int
		page   []byte
		rawURL string
		want   error
	}{
		{name: "geo rights", video: svtFixture(t, "geo-blocked.json"), rawURL: "https://www.svtplay.se/video/x?modalId=geo-id", want: ErrRegionRestricted},
		{name: "geo status forbidden", status: http.StatusForbidden, rawURL: "https://www.svtplay.se/video/x?modalId=geo-id", want: ErrRegionRestricted},
		{name: "geo legal status", status: http.StatusUnavailableForLegalReasons, rawURL: "https://www.svtplay.se/video/x?modalId=geo-id", want: ErrRegionRestricted},
		{name: "gone", status: http.StatusGone, rawURL: "https://www.svtplay.se/video/x?modalId=gone-id", want: ErrUnavailable},
		{name: "no formats", video: []byte(`{"title":"No media","videoReferences":[]}`), rawURL: "https://www.svtplay.se/video/x?modalId=no-media", want: ErrUnavailable},
		{name: "missing title", video: []byte(`{"videoReferences":[{"url":"https://media.invalid/video.mp4"}]}`), rawURL: "https://www.svtplay.se/video/x?modalId=no-title", want: ErrInvalidMetadata},
		{name: "malformed JSON", video: []byte(`{`), rawURL: "https://www.svtplay.se/video/x?modalId=bad-json", want: ErrInvalidMetadata},
		{name: "invalid explicit ID", video: valid, rawURL: "https://www.svtplay.se/video/x?modalId=bad/id", want: ErrInvalidMetadata},
		{name: "missing page ID", video: valid, page: []byte(`<html lang="sv"></html>`), rawURL: "https://www.svtplay.se/video/page-slug", want: ErrInvalidMetadata},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			transport := &svtFixtureTransport{page: test.page, video: test.video, status: test.status}
			_, err := NewRegionSVT().Extract(context.Background(), Request{URL: test.rawURL, Transport: transport})
			if !errors.Is(err, test.want) {
				t.Fatalf("Extract() error = %v, want %v", err, test.want)
			}
		})
	}
}

func TestRegionSVTHonorsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := NewRegionSVT().Extract(ctx, Request{
		URL:       "https://www.svtplay.se/video/x?modalId=svt-fixture-001",
		Transport: &svtFixtureTransport{video: svtFixture(t, "video.json")},
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Extract() error = %v, want context.Canceled", err)
	}
}

func assertSVTExpected(t *testing.T, result Extraction) {
	t.Helper()
	var expected struct {
		ID                string   `json:"id"`
		Title             string   `json:"title"`
		Series            string   `json:"series"`
		SeasonNumber      int64    `json:"season_number"`
		Episode           string   `json:"episode"`
		EpisodeNumber     int64    `json:"episode_number"`
		Duration          int64    `json:"duration"`
		Timestamp         int64    `json:"timestamp"`
		AgeLimit          int64    `json:"age_limit"`
		IsLive            bool     `json:"is_live"`
		FormatCount       int      `json:"format_count"`
		SubtitleLanguages []string `json:"subtitle_languages"`
	}
	if err := json.Unmarshal(svtFixture(t, "expected.json"), &expected); err != nil {
		t.Fatal(err)
	}
	for key, want := range map[string]string{"id": expected.ID, "title": expected.Title, "series": expected.Series, "episode": expected.Episode} {
		if got, ok := result.Info.Lookup(key).StringValue(); !ok || got != want {
			t.Fatalf("%s = %q, %t; want %q", key, got, ok, want)
		}
	}
	for key, want := range map[string]int64{
		"season_number": expected.SeasonNumber, "episode_number": expected.EpisodeNumber,
		"duration": expected.Duration, "timestamp": expected.Timestamp, "age_limit": expected.AgeLimit,
	} {
		if got, ok := result.Info.Lookup(key).Int(); !ok || got != want {
			t.Fatalf("%s = %d, %t; want %d", key, got, ok, want)
		}
	}
	if live, ok := result.Info.Lookup("is_live").Bool(); !ok || live != expected.IsLive {
		t.Fatalf("is_live = %t, %t; want %t", live, ok, expected.IsLive)
	}
	formats, ok := result.Info.Formats()
	if !ok || len(formats) != expected.FormatCount {
		t.Fatalf("formats = %#v", formats)
	}
	wantProtocols := []string{"m3u8_native", "http_dash_segments", "https"}
	for index, want := range wantProtocols {
		format, _ := formats[index].Object()
		if got, _ := format.Lookup("protocol").StringValue(); got != want {
			t.Fatalf("format %d protocol = %q, want %q", index, got, want)
		}
	}
	subtitles, ok := result.Info.Lookup("subtitles").Object()
	if !ok {
		t.Fatal("subtitles missing")
	}
	for _, language := range expected.SubtitleLanguages {
		if entries, ok := subtitles.Lookup(language).ListValue(); !ok || len(entries) != 1 {
			t.Fatalf("subtitle %s = %#v", language, entries)
		}
	}
}

func assertSVTSeriesExpected(t *testing.T, result Extraction, seasonTab bool) {
	t.Helper()
	if !result.IsPlaylist() {
		t.Fatalf("result is not playlist")
	}
	var expected struct {
		SeriesID          string   `json:"series_id"`
		SeriesTitle       string   `json:"series_title"`
		SeriesDescription string   `json:"series_description"`
		SeriesWebpageURL  string   `json:"series_webpage_url"`
		AllEpisodeIDs     []string `json:"all_episode_ids"`
		SeasonID          string   `json:"season_id"`
		SeasonTitle       string   `json:"season_title"`
		SeasonWebpageURL  string   `json:"season_webpage_url"`
		SeasonEpisodeIDs  []string `json:"season_episode_ids"`
	}
	if err := json.Unmarshal(svtFixture(t, "series-expected.json"), &expected); err != nil {
		t.Fatal(err)
	}
	wantIDs := expected.AllEpisodeIDs
	wantID := expected.SeriesID
	wantTitle := expected.SeriesTitle
	wantWebpage := expected.SeriesWebpageURL
	if seasonTab {
		wantIDs = expected.SeasonEpisodeIDs
		wantID = expected.SeasonID
		wantTitle = expected.SeasonTitle
		wantWebpage = expected.SeasonWebpageURL
	}
	if id, _ := result.Info.ID(); id != wantID {
		t.Fatalf("playlist id = %q, want %q", id, wantID)
	}
	if title, _ := result.Info.Lookup("title").StringValue(); title != wantTitle {
		t.Fatalf("playlist title = %q, want %q", title, wantTitle)
	}
	if webpage, _ := result.Info.Lookup("webpage_url").StringValue(); webpage != wantWebpage {
		t.Fatalf("webpage_url = %q, want %q", webpage, wantWebpage)
	}
	if description, _ := result.Info.Lookup("description").StringValue(); description != expected.SeriesDescription {
		t.Fatalf("description = %q", description)
	}
	entries, err := CollectEntries(context.Background(), result.Entries, len(wantIDs)+1)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != len(wantIDs) {
		t.Fatalf("entries = %#v", entries)
	}
	for index, want := range wantIDs {
		if entries[index].ID != want || entries[index].URL != "svt:"+want {
			t.Fatalf("entry %d = %#v, want id/url for %q", index, entries[index], want)
		}
	}
}

func FuzzRegionSVTVideoResponse(f *testing.F) {
	f.Add(svtFixture(f, "video.json"), "svt-fixture-001")
	f.Add(svtFixture(f, "geo-blocked.json"), "geo-id")
	f.Add([]byte(`{`), "bad")
	f.Fuzz(func(t *testing.T, body []byte, videoID string) {
		if len(body) > 1<<20 || len(videoID) > 4096 {
			t.Skip()
		}
		var response svtVideoResponse
		if json.Unmarshal(body, &response) != nil {
			return
		}
		_, _ = normalizeSVTVideo(response, videoID, "https://www.svtplay.se/video/fixture")
	})
}

func FuzzParseSVTSeriesResponse(f *testing.F) {
	f.Add(svtFixture(f, "series.json"), "")
	f.Add([]byte(`{"errors":[{"message":"denied"}]}`), "season-2-jpmQYgn")
	f.Add([]byte(`{"data":{"listablesBySlug":[]}}`), "")
	f.Fuzz(func(t *testing.T, body []byte, seasonTab string) {
		if len(body) > 1<<20 || len(seasonTab) > 4096 {
			t.Skip()
		}
		var envelope svtSeriesGraphQLResponse
		if json.Unmarshal(body, &envelope) != nil {
			return
		}
		_, err := parseSVTSeriesResponse(context.Background(), envelope, seasonTab)
		if err != nil && strings.Contains(err.Error(), "fixture-secret") {
			t.Fatalf("secret leaked: %v", err)
		}
		switch {
		case errors.Is(err, ErrInvalidMetadata), errors.Is(err, ErrUnavailable),
			errors.Is(err, ErrPlaylistLimit), errors.Is(err, context.Canceled):
		case err == nil:
		default:
			if seasonTab != "" && !svtSeasonTabPattern.MatchString(seasonTab) {
				return
			}
		}
	})
}
