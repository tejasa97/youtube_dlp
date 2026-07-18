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
	mu       sync.Mutex
	page     []byte
	video    []byte
	status   int
	apiCalls []string
}

func (transport *svtFixtureTransport) Do(ctx context.Context, request *http.Request) (*http.Response, error) {
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
		{"https://www.svtplay.se/series-name", false},
		{"https://www.svt.se/news/article", false},
		{"ftp://www.svtplay.se/video/id", false},
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
