package extractor

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

const tedFixtureRoot = "../../conformance/extractors/risk/ted"

func tedFixture(t testing.TB, name string) []byte {
	t.Helper()
	body, err := os.ReadFile(filepath.Join(tedFixtureRoot, name))
	if err != nil {
		t.Fatal(err)
	}
	return body
}

type tedFixtureTransport struct {
	mu        sync.Mutex
	responses map[string]fixtureHTTP
	requests  []string
	headers   []http.Header
	wait      chan struct{}
	started   chan struct{}
	startOnce sync.Once
}

func (transport *tedFixtureTransport) response(ctx context.Context, request *http.Request) (*http.Response, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	transport.mu.Lock()
	transport.requests = append(transport.requests, request.Method+" "+request.URL.String())
	transport.headers = append(transport.headers, request.Header.Clone())
	response, ok := transport.responses[request.URL.String()]
	wait := transport.wait
	transport.mu.Unlock()
	if transport.started != nil {
		transport.startOnce.Do(func() { close(transport.started) })
	}
	if wait != nil {
		select {
		case <-wait:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if !ok {
		return &http.Response{StatusCode: http.StatusNotFound, Header: make(http.Header), Body: io.NopCloser(bytes.NewReader(nil)), Request: request}, nil
	}
	status := response.status
	if status == 0 {
		status = http.StatusOK
	}
	return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(bytes.NewReader(response.body)), Request: request}, nil
}

func (transport *tedFixtureTransport) Do(ctx context.Context, request *http.Request) (*http.Response, error) {
	return transport.response(ctx, request)
}

func (transport *tedFixtureTransport) ReadPage(ctx context.Context, rawURL string) ([]byte, http.Header, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, nil, err
	}
	response, err := transport.response(ctx, request)
	if err != nil {
		return nil, nil, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, nil, &HTTPStatusError{Code: response.StatusCode}
	}
	body, err := io.ReadAll(response.Body)
	return body, response.Header.Clone(), err
}

func (transport *tedFixtureTransport) DoWithoutCredentialsNoRedirect(ctx context.Context, request *http.Request) (*http.Response, error) {
	for _, key := range []string{"Authorization", "Cookie", "Proxy-Authorization", "Referer"} {
		if request.Header.Get(key) != "" {
			return nil, ErrTransportIsolation
		}
	}
	return transport.response(ctx, request)
}

func tedTalkTransport(t *testing.T) *tedFixtureTransport {
	t.Helper()
	return &tedFixtureTransport{responses: map[string]fixtureHTTP{
		"https://www.ted.com/talks/fixture_talk":                           {body: tedFixture(t, "talk_page.html")},
		"https://hls.ted.com/talks/fixture/master.m3u8?sig=master-1":       {body: tedFixture(t, "master.m3u8")},
		"https://download.ted.com/talks/fixture/800k.m3u8?sig=variant-800": {body: tedFixture(t, "800k.m3u8")},
	}}
}

func TestTedFamilyRoutingAndExactNonOverlap(t *testing.T) {
	t.Parallel()
	registry := NewRegistry(NewTedEmbed(), NewTedTalk(), NewTedSeries(), NewTedPlaylist())
	positive := []struct{ raw, want string }{
		{"https://www.ted.com/talks/fixture_talk", "ted_talk"},
		{"http://ted.com/talks/lang/en/fixture_talk?tracking=one", "ted_talk"},
		{"https://www.ted.com/series/lang/en/fixture_series", "ted_series"},
		{"https://www.ted.com/series/fixture_series#season_2", "ted_series"},
		{"https://www.ted.com/playlists/fixture_playlist", "ted_playlist"},
		{"https://www.ted.com/playlists/171/fixture_playlist", "ted_playlist"},
		{"https://www.ted.com/playlists/lang/en/fixture_playlist", "ted_playlist"},
		{"https://www.ted.com/playlists/171/lang/en/fixture_playlist", "ted_playlist"},
		{"https://embed.ted.com/talks/fixture_talk", "ted_embed"},
		{"https://embed-ssl.ted.com/talks/lang/en/fixture_talk?tracking=embed", "ted_embed"},
	}
	for _, test := range positive {
		selected, err := registry.Select(test.raw)
		if err != nil || selected.Name() != test.want {
			t.Fatalf("Select(%q)=%v,%v want %q", test.raw, selected, err, test.want)
		}
	}
	negative := []string{
		"ftp://www.ted.com/talks/fixture_talk",
		"https://evil.ted.com/talks/fixture_talk",
		"https://www.ted.com.evil/talks/fixture_talk",
		"https://user:secret@www.ted.com/talks/fixture_talk",
		"https://www.ted.com:443/talks/fixture_talk",
		"https://www.ted.com/talks/fixture_talk#fragment",
		"https://embed.ted.com/talks/fixture_talk#fragment",
		"https://www.ted.com/talks/fixture%5Ftalk",
		"https://www.ted.com/talks/fixture%2Ftalk",
		"https://www.ted.com/talks/fixture_talk?x=1&x=2",
		"https://embed.ted.com/talks/fixture_talk?x=1&x=2",
		"https://embed.ted.com/series/fixture_series",
		"https://embed-ssl.ted.com/playlists/171/fixture_playlist",
		"https://embed.ted.com/other/fixture_talk",
		"https://www.ted.com/talks/lang/en",
		"https://www.ted.com/series/lang/en/fixture_series/extra",
		"https://www.ted.com/playlists/lang/en",
		"https://www.ted.com/playlists/171/lang/en",
		"https://www.ted.com/playlists/not_numeric/lang/en/fixture_playlist",
		"https://www.ted.com/playlists/171/fixture_playlist/extra",
		"https://www.ted.com/talks/fixture_talk/",
		"https://www.ted.com/series/fixture_series/",
		"https://www.ted.com/series/fixture_series#season%5F2",
		"https://www.ted.com/series/fixture_series#season_x",
	}
	for _, raw := range negative {
		if _, err := registry.Select(raw); err == nil {
			t.Fatalf("Select(%q) accepted unsafe or ambiguous URL", raw)
		}
	}
}

func TestTedAttributableURLRolePolicy(t *testing.T) {
	for _, test := range []struct {
		role string
		url  string
		want bool
	}{
		{role: "manifest", url: "https://hls.ted.com/talks/fixture/master.m3u8?sig=manifest", want: true},
		{role: "manifest", url: "https://download.ted.com/talks/fixture/master.m3u8?sig=manifest", want: true},
		{role: "media", url: "https://download.ted.com/talks/fixture/500k.mp4?sig=media", want: true},
		{role: "segment", url: "https://download.ted.com/talks/fixture/seg-0.ts?sig=segment", want: true},
		{role: "subtitle", url: "https://download.ted.com/talks/fixture/captions/en.vtt?sig=subtitle", want: true},
		{role: "thumbnail", url: "https://pi.tedcdn.com/images/fixture.jpg?sig=thumbnail", want: true},
		{role: "manifest", url: "https://pi.tedcdn.com/images/fixture.jpg?sig=wrong-role", want: false},
		{role: "media", url: "https://hls.ted.com/talks/fixture/seg-0.ts?sig=wrong-role", want: false},
		{role: "segment", url: "https://hls.ted.com/talks/fixture/seg-0.ts?sig=wrong-role", want: false},
		{role: "thumbnail", url: "https://download.ted.com/talks/fixture/500k.mp4?sig=wrong-role", want: false},
		{role: "manifest", url: "https://evil.ted.com/talks/fixture/master.m3u8?sig=evil", want: false},
		{role: "unknown", url: "https://download.ted.com/talks/fixture/500k.mp4?sig=unknown", want: false},
	} {
		if got := TedAttributableURL(test.url, test.role); got != test.want {
			t.Errorf("TedAttributableURL(%q, %q)=%t, want %t", test.url, test.role, got, test.want)
		}
	}
}

func TestTedRouteGrammarAndCanonicalTargets(t *testing.T) {
	tests := []struct {
		raw                     string
		kind                    tedKind
		slug, language, numeric string
		season                  string
		pageURL                 string
	}{
		{raw: "https://www.ted.com/talks/lang/en/fixture_talk?sig=talk", kind: tedTalkKind, slug: "fixture_talk", language: "en", pageURL: "https://www.ted.com/talks/lang/en/fixture_talk?sig=talk"},
		{raw: "https://www.ted.com/series/lang/en/fixture_series#season_2", kind: tedSeriesKind, slug: "fixture_series", language: "en", season: "2", pageURL: "https://www.ted.com/series/lang/en/fixture_series"},
		{raw: "https://www.ted.com/playlists/fixture_playlist", kind: tedPlaylistKind, slug: "fixture_playlist", pageURL: "https://www.ted.com/playlists/fixture_playlist"},
		{raw: "https://www.ted.com/playlists/171/fixture_playlist", kind: tedPlaylistKind, slug: "fixture_playlist", numeric: "171", pageURL: "https://www.ted.com/playlists/171/fixture_playlist"},
		{raw: "https://www.ted.com/playlists/lang/en/fixture_playlist", kind: tedPlaylistKind, slug: "fixture_playlist", language: "en", pageURL: "https://www.ted.com/playlists/lang/en/fixture_playlist"},
		{raw: "https://www.ted.com/playlists/171/lang/en/fixture_playlist", kind: tedPlaylistKind, slug: "fixture_playlist", language: "en", numeric: "171", pageURL: "https://www.ted.com/playlists/171/lang/en/fixture_playlist"},
	}
	for _, test := range tests {
		parsed, err := url.Parse(test.raw)
		if err != nil {
			t.Fatal(err)
		}
		got, ok := parseTedTarget(parsed, test.kind, test.kind == tedSeriesKind)
		if !ok || got.kind != test.kind || got.slug != test.slug || got.language != test.language || got.numeric != test.numeric || got.pageURL != test.pageURL {
			t.Fatalf("parseTedTarget(%q)=%#v,%t", test.raw, got, ok)
		}
		if test.kind == tedSeriesKind && got.season != "2" {
			t.Fatalf("series season=%q", got.season)
		}
	}
}

func TestTedTalkMetadataFormatsSubtitlesThumbnailsAndIsolation(t *testing.T) {
	transport := tedTalkTransport(t)
	result, err := NewTedTalk().Extract(context.Background(), Request{
		URL: "https://www.ted.com/talks/fixture_talk", Transport: transport,
	})
	if err != nil {
		t.Fatal(err)
	}
	if id, _ := result.Info.Lookup("id").StringValue(); id != "86532" {
		t.Fatalf("id=%q", id)
	}
	if title, _ := result.Info.Lookup("title").StringValue(); title != "Fixture TED Talk" {
		t.Fatalf("title=%q", title)
	}
	formats, ok := result.Info.Formats()
	if !ok || len(formats) < 3 {
		t.Fatalf("formats=%#v", formats)
	}
	for _, raw := range formats {
		object, _ := raw.Object()
		isolated, _ := object.Lookup("_credential_isolated").Bool()
		if !isolated {
			t.Fatalf("format is not isolated: %#v", object)
		}
		if rawURL, _ := object.Lookup("url").StringValue(); strings.Contains(rawURL, "sig=") == false {
			t.Fatalf("signed format query was not preserved: %q", rawURL)
		}
	}
	subs, ok := result.Info.Lookup("subtitles").Object()
	if !ok || subs.Lookup("en").IsMissing() {
		t.Fatalf("subtitles=%#v", result.Info.Lookup("subtitles"))
	}
	thumbs, ok := result.Info.Lookup("thumbnails").ListValue()
	if !ok || len(thumbs) != 1 {
		t.Fatalf("thumbnails=%#v", result.Info.Lookup("thumbnails"))
	}
	thumb, _ := thumbs[0].Object()
	if rawURL, _ := thumb.Lookup("url").StringValue(); rawURL != "https://pi.tedcdn.com/images/fixture-talk.jpg?sig=thumb-1" {
		t.Fatalf("thumbnail=%q", rawURL)
	}
	chapters, ok := result.Info.Lookup("chapters").ListValue()
	if !ok || len(chapters) != 2 {
		t.Fatalf("chapters=%#v", result.Info.Lookup("chapters"))
	}
	transport.mu.Lock()
	defer transport.mu.Unlock()
	if len(transport.requests) < 2 {
		t.Fatalf("requests=%v", transport.requests)
	}
	for index, headers := range transport.headers {
		for _, key := range []string{"Authorization", "Cookie", "Proxy-Authorization", "Referer"} {
			if headers.Get(key) != "" {
				t.Fatalf("request %d leaked %s", index, key)
			}
		}
	}
}

func TestTedSeriesPlaylistSeasonFilteringAndReusableChildren(t *testing.T) {
	seriesURL := "https://www.ted.com/series/fixture_series#season_2"
	playlistURL := "https://www.ted.com/playlists/171/fixture_playlist"
	transport := &tedFixtureTransport{responses: map[string]fixtureHTTP{
		"https://www.ted.com/series/fixture_series": {body: tedFixture(t, "series_page.html")},
		playlistURL: {body: tedFixture(t, "playlist_page.html")},
	}}
	series, err := NewTedSeries().Extract(context.Background(), Request{URL: seriesURL, Transport: transport})
	if err != nil {
		t.Fatal(err)
	}
	first, err := CollectEntries(context.Background(), series.Entries, 10)
	if err != nil || len(first) != 1 || first[0].ID != "1002" || first[0].Title != "Season Two Talk" {
		t.Fatalf("series first=%#v err=%v", first, err)
	}
	second, err := CollectEntries(context.Background(), series.Entries, 10)
	if err != nil || len(second) != 1 || second[0].URL != first[0].URL {
		t.Fatalf("series reuse=%#v err=%v", second, err)
	}
	playlist, err := NewTedPlaylist().Extract(context.Background(), Request{URL: playlistURL, Transport: transport})
	if err != nil {
		t.Fatal(err)
	}
	entries, err := CollectEntries(context.Background(), playlist.Entries, 10)
	if err != nil || len(entries) != 2 || entries[1].ID != "2002" || entries[1].Duration != 9 {
		t.Fatalf("playlist entries=%#v err=%v", entries, err)
	}
}

func TestTedEmbedTransparentCanonicalReentry(t *testing.T) {
	raw := "https://embed-ssl.ted.com/talks/fixture_talk?sig=embed-1"
	result, err := NewTedEmbed().Extract(context.Background(), Request{URL: raw})
	if err != nil || !result.IsURL() || result.Redirect == nil {
		t.Fatalf("result=%#v err=%v", result, err)
	}
	if result.Redirect.URL != "https://www.ted.com/talks/fixture_talk?sig=embed-1" || result.Redirect.ExtractorKey != "ted_talk" || !result.Redirect.Transparent {
		t.Fatalf("redirect=%#v", result.Redirect)
	}
}

func TestTedStatusCancellationAndIsolationErrorsAreTypedAndSecretSafe(t *testing.T) {
	statusURL := "https://www.ted.com/talks/fixture_talk"
	for _, test := range []struct {
		status int
		want   error
	}{
		{http.StatusTooManyRequests, ErrTedRateLimited},
		{http.StatusNotFound, ErrUnavailable},
		{http.StatusFound, ErrTedRedirect},
	} {
		transport := &tedFixtureTransport{responses: map[string]fixtureHTTP{statusURL: {status: test.status, body: []byte("token=must-not-leak")}}}
		_, err := NewTedTalk().Extract(context.Background(), Request{URL: statusURL, Transport: transport})
		if !errors.Is(err, test.want) || strings.Contains(err.Error(), "must-not-leak") {
			t.Fatalf("status=%d err=%v want=%v", test.status, err, test.want)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	transport := &tedFixtureTransport{responses: map[string]fixtureHTTP{statusURL: {body: tedFixture(t, "talk_page.html")}}, wait: make(chan struct{}), started: make(chan struct{})}
	done := make(chan error, 1)
	go func() {
		_, extractErr := NewTedTalk().Extract(ctx, Request{URL: statusURL, Transport: transport})
		done <- extractErr
	}()
	<-transport.started
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation err=%v", err)
	}
}

func TestTedRouteParserSeedCases(t *testing.T) {
	for _, raw := range []string{
		"https://ted.com/talks/a_b-c?x=1",
		"https://www.ted.com/series/a#season_12",
		"https://www.ted.com/playlists/171/a_b",
		"https://embed.ted.com/talks/a_b",
	} {
		parsed, err := url.Parse(raw)
		if err != nil {
			t.Fatal(err)
		}
		if strings.HasPrefix(parsed.Hostname(), "embed") {
			if tedEmbedCanonical(parsed) == "" {
				t.Fatalf("embed seed rejected: %q", raw)
			}
		} else if !(TedTalkIE{}).Suitable(parsed) && !(TedSeriesIE{}).Suitable(parsed) && !(TedPlaylistIE{}).Suitable(parsed) {
			t.Fatalf("main seed rejected: %q", raw)
		}
	}
}

func FuzzTedRouteParser(f *testing.F) {
	for _, seed := range []string{
		"https://ted.com/talks/a_b-c?x=1",
		"https://www.ted.com/series/a#season_12",
		"https://www.ted.com/playlists/171/a_b",
		"https://embed.ted.com/talks/a_b",
		"https://embed-ssl.ted.com/talks/lang/en/a?tracking=1",
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, raw string) {
		if len(raw) > tedMaxURLBytes*2 {
			return
		}
		parsed, err := url.Parse(raw)
		if err != nil {
			return
		}
		// Suitability is deliberately a total predicate over parsed URLs.
		_ = (TedTalkIE{}).Suitable(parsed)
		_ = (TedSeriesIE{}).Suitable(parsed)
		_ = (TedPlaylistIE{}).Suitable(parsed)
		canonical := tedEmbedCanonical(parsed)
		if canonical == "" {
			return
		}
		canonicalParsed, err := url.Parse(canonical)
		if err != nil {
			t.Fatalf("canonical URL did not parse: %v", err)
		}
		if _, ok := parseTedTarget(canonicalParsed, tedTalkKind, false); !ok {
			t.Fatalf("canonical URL is outside the public route matrix: %q", canonical)
		}
	})
}
