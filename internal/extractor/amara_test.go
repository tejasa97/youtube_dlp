package extractor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const amaraFixtureRoot = "testdata/amara"

func amaraFixture(t testing.TB, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(amaraFixtureRoot, name))
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func amaraAPIURL(id string) string {
	return "https://amara.org/api/videos/" + url.PathEscape(id) + "/?format=json"
}

func TestAmaraRouting(t *testing.T) {
	t.Parallel()
	positive := []struct {
		rawURL string
		id     string
	}{
		{"https://amara.org/videos/jVx79ZKGK1ky/info/why-jury-trials/", "jVx79ZKGK1ky"},
		{"http://www.amara.org/videos/s8KL7I3jLmh6/", "s8KL7I3jLmh6"},
		{"https://amara.org/en/videos/kYkK1VUTWW5I/info/vimeo-at-ces-2011", "kYkK1VUTWW5I"},
		{"https://amara.org/videos/jVx79ZKGK1ky/info", "jVx79ZKGK1ky"},
	}
	for _, test := range positive {
		parsed, err := url.Parse(test.rawURL)
		if err != nil {
			t.Fatalf("Parse(%q): %v", test.rawURL, err)
		}
		if !NewAmara().Suitable(parsed) {
			t.Fatalf("Suitable(%q) = false", test.rawURL)
		}
		target, ok := parseAmaraURL(parsed)
		if !ok || target.id != test.id {
			t.Fatalf("parseAmaraURL(%q) = %#v", test.rawURL, target)
		}
		if !strings.HasPrefix(target.canonical, "https://amara.org/") {
			t.Fatalf("canonical=%q", target.canonical)
		}
	}
	negative := []string{
		"https://evilamara.org/videos/jVx79ZKGK1ky",
		"https://amara.org.evil/videos/jVx79ZKGK1ky",
		"https://amara.org:8443/videos/jVx79ZKGK1ky",
		"https://user:pass@amara.org/videos/jVx79ZKGK1ky",
		"https://amara.org/videos/jVx79ZKGK1ky#frag",
		"https://amara.org/videos/",
		"https://amara.org/en/videos",
		"https://amara.org/api/videos/jVx79ZKGK1ky",
		"https://amara.org/en/foo/videos/jVx79ZKGK1ky",
		"https://amara.org/videos/jVx79ZKGK1ky%2finfo",
		"https://amara.org/videos/jVx79ZKGK1ky%00",
		"ftp://amara.org/videos/jVx79ZKGK1ky",
		"https://amara.org/videos/" + strings.Repeat("a", amaraMaxVideoIDLen+1),
		"https://amara.org/videos/bad-id!",
		"https://www.amara.org/fr/videos/abc_123/extra/path",
		"https://amara.org/videos/jVx79ZKGK1ky/extra",
		"https://amara.org/videos/" + strings.Repeat("a", int(sharedHostingMaxURLBytes)),
		"not-a-url",
	}
	for _, rawURL := range negative {
		parsed, err := url.Parse(rawURL)
		if err != nil {
			continue
		}
		if NewAmara().Suitable(parsed) {
			t.Fatalf("Suitable(%q) = true", rawURL)
		}
	}
}

func TestAmaraYouTubeHandoffAndReentry(t *testing.T) {
	t.Parallel()
	registry := NewRegistry(NewAmara(), NewYouTube(), NewGeneric())
	id := "jVx79ZKGK1ky"
	transport := &sharedFixtureTransport{responses: map[string]fixtureHTTP{
		amaraAPIURL(id): {body: amaraFixture(t, "youtube.json")},
	}}
	result, err := NewAmara().Extract(context.Background(), Request{
		URL: "https://amara.org/en/videos/" + id + "/info/why-jury-trials/?tab=video", Transport: transport,
	})
	if err != nil || !result.IsURL() || result.Redirect.ExtractorKey != "youtube" || !result.Redirect.Transparent {
		t.Fatalf("handoff=%#v err=%v", result, err)
	}
	if result.Redirect.ID != "" || result.Redirect.Title == "" {
		t.Fatalf("overlay=%#v", result.Redirect)
	}
	desc, _ := result.Info.Lookup("description").StringValue()
	if desc == "" {
		t.Fatal("missing Amara description in parent info")
	}
	selected, err := registry.SelectFor(result.Redirect.URL, result.Redirect.ExtractorKey)
	if err != nil || selected.Name() != "youtube" {
		t.Fatalf("registry=%v err=%v", selected, err)
	}
}

func TestAmaraVimeoHandoffAndReentry(t *testing.T) {
	t.Parallel()
	registry := NewRegistry(NewAmara(), NewVimeo(), NewGeneric())
	id := "kYkK1VUTWW5I"
	transport := &sharedFixtureTransport{responses: map[string]fixtureHTTP{
		amaraAPIURL(id): {body: amaraFixture(t, "vimeo.json")},
	}}
	result, err := NewAmara().Extract(context.Background(), Request{
		URL: "https://amara.org/en/videos/" + id + "/info/vimeo-at-ces-2011", Transport: transport,
	})
	if err != nil || !result.IsURL() || result.Redirect.ExtractorKey != "vimeo" || !result.Redirect.Transparent {
		t.Fatalf("handoff=%#v err=%v", result, err)
	}
	selected, err := registry.SelectFor(result.Redirect.URL, "vimeo")
	if err != nil || selected.Name() != "vimeo" {
		t.Fatalf("registry=%v err=%v", selected, err)
	}
}

func TestAmaraDirectMediaExtraction(t *testing.T) {
	t.Parallel()
	id := "s8KL7I3jLmh6"
	transport := &sharedFixtureTransport{responses: map[string]fixtureHTTP{
		amaraAPIURL(id): {body: amaraFixture(t, "direct.json")},
	}}
	result, err := NewAmara().Extract(context.Background(), Request{
		URL: "https://amara.org/en/videos/" + id + "/info/the-danger-of-a-single-story/", Transport: transport,
	})
	if err != nil || result.IsURL() || result.IsPlaylist() {
		t.Fatalf("result=%#v err=%v", result, err)
	}
	formats, ok := result.Info.Formats()
	if !ok || len(formats) != 1 {
		t.Fatalf("formats=%v", formats)
	}
	ext, _ := result.Info.Lookup("ext").StringValue()
	if ext != "mp4" {
		t.Fatalf("ext=%q", ext)
	}
	if ts, ok := result.Info.Lookup("timestamp").Int(); !ok || ts != 1254918511 {
		t.Fatalf("timestamp=%v ok=%v", ts, ok)
	}
}

func TestAmaraSubtitlesFromFixturePayload(t *testing.T) {
	t.Parallel()
	var payload amaraVideoResponse
	if err := json.Unmarshal(amaraFixture(t, "direct.json"), &payload); err != nil {
		t.Fatal(err)
	}
	subs, err := amaraSubtitles(payload.Languages)
	if err != nil || subs == nil {
		t.Fatalf("languages=%#v", payload.Languages)
	}
}

func TestAmaraDirectMediaDoesNotHandoff(t *testing.T) {
	t.Parallel()
	var payload amaraVideoResponse
	if err := json.Unmarshal(amaraFixture(t, "direct.json"), &payload); err != nil {
		t.Fatal(err)
	}
	mediaURL, ok := amaraFirstMediaURL(payload.AllURLs)
	if !ok {
		t.Fatal("missing media url")
	}
	if _, ok := amaraTransparentHandoff(mediaURL, payload.Title, "", 0, 0); ok {
		t.Fatalf("unexpected handoff for %q", mediaURL)
	}
}

func TestAmaraNormalizeDirectFixture(t *testing.T) {
	t.Parallel()
	var payload amaraVideoResponse
	if err := json.Unmarshal(amaraFixture(t, "direct.json"), &payload); err != nil {
		t.Fatal(err)
	}
	result, err := normalizeAmara(payload, amaraTarget{id: "s8KL7I3jLmh6", canonical: "https://amara.org/videos/s8KL7I3jLmh6"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Info.Lookup("subtitles").IsMissing() {
		t.Fatalf("missing subtitles in normalized result")
	}
}

func TestAmaraPublishedSubtitleLanguages(t *testing.T) {
	t.Parallel()
	id := "s8KL7I3jLmh6"
	transport := &sharedFixtureTransport{responses: map[string]fixtureHTTP{
		amaraAPIURL(id): {body: amaraFixture(t, "direct.json")},
	}}
	result, err := NewAmara().Extract(context.Background(), Request{
		URL: "https://amara.org/videos/" + id, Transport: transport,
	})
	if err != nil {
		t.Fatal(err)
	}
	subtitles, ok := result.Info.Lookup("subtitles").Object()
	if !ok || subtitles.Len() != 2 || subtitles.Lookup("en").IsMissing() || subtitles.Lookup("fr").IsMissing() {
		t.Fatalf("subtitles=%#v", result.Info.Lookup("subtitles"))
	}
	en, ok := subtitles.Lookup("en").ListValue()
	if !ok || len(en) != 3 {
		t.Fatalf("en subtitles=%v", en)
	}
	for _, item := range en {
		object, ok := item.Object()
		if !ok {
			t.Fatal("subtitle entry not object")
		}
		rawURL, _ := object.Lookup("url").StringValue()
		ext, _ := object.Lookup("ext").StringValue()
		if !strings.Contains(rawURL, "format="+ext) || !strictValidHostedHTTPURL(rawURL) {
			t.Fatalf("subtitle url=%q ext=%q", rawURL, ext)
		}
	}
}

func TestAmaraSubtitleURLConstruction(t *testing.T) {
	t.Parallel()
	entries, ok := amaraSubtitleFormatsFor("https://amara.org/api/videos/fixture/subtitles/en/?token=abc")
	if !ok || len(entries) != 3 {
		t.Fatalf("entries=%v ok=%v", entries, ok)
	}
	want := map[string]bool{"json": false, "srt": false, "vtt": false}
	for _, item := range entries {
		object, _ := item.Object()
		ext, _ := object.Lookup("ext").StringValue()
		rawURL, _ := object.Lookup("url").StringValue()
		want[ext] = strings.Contains(rawURL, "format="+ext)
	}
	for ext, present := range want {
		if !present {
			t.Fatalf("missing format %q", ext)
		}
	}
}

func TestAmaraEmptyAndHostileMediaURLs(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name, fixture string
	}{
		{"empty", "empty_urls.json"},
		{"hostile", "hostile_urls.json"},
	} {
		t.Run(test.name, func(t *testing.T) {
			id := "fixture01"
			transport := &sharedFixtureTransport{responses: map[string]fixtureHTTP{
				amaraAPIURL(id): {body: amaraFixture(t, test.fixture)},
			}}
			_, err := NewAmara().Extract(context.Background(), Request{
				URL: "https://amara.org/videos/" + id, Transport: transport,
			})
			if !errors.Is(err, ErrInvalidMetadata) {
				t.Fatalf("err=%v", err)
			}
		})
	}
}

func TestAmaraRejectsUnsafeHostedMediaURLs(t *testing.T) {
	t.Parallel()
	unsafe := []string{
		"http://127.0.0.1/video.mp4",
		"https://localhost/video.mp4",
		"https://cdn.amara.org:8443/video.mp4",
		"https://cdn.amara.org/video.mp4#fragment",
		"https://cdn.amara.org/a/../video.mp4",
	}
	for _, rawURL := range unsafe {
		_, err := normalizeAmara(amaraVideoResponse{
			Title:   "unsafe",
			AllURLs: []string{rawURL},
		}, amaraTarget{id: "unsafe", canonical: "https://amara.org/videos/unsafe"})
		if !errors.Is(err, ErrInvalidMetadata) {
			t.Fatalf("normalizeAmara(%q) err=%v", rawURL, err)
		}
	}
}

func TestAmaraSkipsUnsafeHostedMediaURL(t *testing.T) {
	t.Parallel()
	result, err := normalizeAmara(amaraVideoResponse{
		Title:   "safe fallback",
		AllURLs: []string{"http://127.0.0.1/video.mp4", "https://cdn.amara.org/media/video.webm"},
	}, amaraTarget{id: "safe", canonical: "https://amara.org/videos/safe"})
	if err != nil {
		t.Fatal(err)
	}
	formats, ok := result.Info.Formats()
	if !ok || len(formats) != 1 {
		t.Fatalf("formats=%v", formats)
	}
	format, _ := formats[0].Object()
	rawURL, _ := format.Lookup("url").StringValue()
	if rawURL != "https://cdn.amara.org/media/video.webm" {
		t.Fatalf("media url=%q", rawURL)
	}
}

func TestAmaraHTTPStatusCategorization(t *testing.T) {
	t.Parallel()
	id := "fixture01"
	tests := []struct {
		status int
		want   error
	}{
		{http.StatusUnauthorized, ErrAuthentication},
		{http.StatusForbidden, ErrAuthentication},
		{http.StatusNotFound, ErrUnavailable},
		{http.StatusGone, ErrUnavailable},
		{http.StatusTooManyRequests, ErrAmaraRateLimited},
		{http.StatusInternalServerError, ErrAmaraNetwork},
	}
	for _, test := range tests {
		transport := &sharedFixtureTransport{responses: map[string]fixtureHTTP{
			amaraAPIURL(id): {status: test.status, body: []byte(`{"title":"x","all_urls":["https://cdn.amara.org/media/x.mp4"]}`)},
		}}
		_, err := NewAmara().Extract(context.Background(), Request{
			URL: "https://amara.org/videos/" + id, Transport: transport,
		})
		if !errors.Is(err, test.want) {
			t.Fatalf("status=%d err=%v want %v", test.status, err, test.want)
		}
	}
}

func TestAmaraSecretSafeErrors(t *testing.T) {
	t.Parallel()
	secret := "must-not-leak-secret-token"
	transport := &sharedFixtureTransport{responses: map[string]fixtureHTTP{
		amaraAPIURL("fixture01"): {status: http.StatusForbidden, body: []byte(secret)},
	}}
	_, err := NewAmara().Extract(context.Background(), Request{
		URL: "https://amara.org/videos/fixture01", Transport: transport,
	})
	if !errors.Is(err, ErrAuthentication) || strings.Contains(err.Error(), secret) {
		t.Fatalf("err=%v", err)
	}
}

func TestAmaraCancellation(t *testing.T) {
	t.Parallel()
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	transport := &sharedFixtureTransport{responses: map[string]fixtureHTTP{
		amaraAPIURL("fixture01"): {body: amaraFixture(t, "direct.json")},
	}}
	if _, err := NewAmara().Extract(canceled, Request{
		URL: "https://amara.org/videos/fixture01", Transport: transport,
	}); !errors.Is(err, context.Canceled) {
		t.Fatalf("before transport err=%v", err)
	}
	blocking := &amaraBlockingTransport{release: make(chan struct{})}
	go func() {
		time.Sleep(20 * time.Millisecond)
		close(blocking.release)
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Millisecond)
	defer cancel()
	_, err := NewAmara().Extract(ctx, Request{
		URL: "https://amara.org/videos/fixture01", Transport: blocking,
	})
	if !errors.Is(err, context.DeadlineExceeded) && !errors.Is(err, context.Canceled) {
		t.Fatalf("during transport err=%v", err)
	}
}

type amaraBlockingTransport struct {
	release chan struct{}
}

func (transport *amaraBlockingTransport) Do(ctx context.Context, request *http.Request) (*http.Response, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-transport.release:
		return (&sharedFixtureTransport{responses: map[string]fixtureHTTP{
			request.URL.String(): {body: []byte(`{"title":"x","all_urls":["https://cdn.amara.org/media/x.mp4"]}`)},
		}}).Do(ctx, request)
	}
}

func (transport *amaraBlockingTransport) ReadPage(context.Context, string) ([]byte, http.Header, error) {
	return nil, nil, errors.New("unexpected page read")
}

func TestAmaraMalformedAndOversizedJSON(t *testing.T) {
	t.Parallel()
	id := "fixture01"
	for _, body := range []string{
		`{"title":"x"`,
		`{"title":"x","all_urls":["https://cdn.amara.org/media/x.mp4"]} trailing`,
		strings.Repeat(" ", int(maxExtractorJSONBytes)+1),
	} {
		transport := &sharedFixtureTransport{responses: map[string]fixtureHTTP{
			amaraAPIURL(id): {body: []byte(body)},
		}}
		_, err := NewAmara().Extract(context.Background(), Request{
			URL: "https://amara.org/videos/" + id, Transport: transport,
		})
		if err == nil || errors.Is(err, ErrUnsupported) {
			t.Fatalf("body len=%d err=%v", len(body), err)
		}
	}
}

func TestAmaraExcessiveLanguagesAndLongStrings(t *testing.T) {
	t.Parallel()
	languages := make([]map[string]any, 0, amaraMaxLanguages+1)
	for i := 0; i < amaraMaxLanguages+1; i++ {
		languages = append(languages, map[string]any{
			"code": "en", "published": true, "subtitles_uri": "https://amara.org/api/videos/x/subtitles/en/",
		})
	}
	payload, err := json.Marshal(map[string]any{
		"title": "x", "all_urls": []string{"https://cdn.amara.org/media/x.mp4"}, "languages": languages,
	})
	if err != nil {
		t.Fatal(err)
	}
	transport := &sharedFixtureTransport{responses: map[string]fixtureHTTP{
		amaraAPIURL("fixture01"): {body: payload},
	}}
	_, err = NewAmara().Extract(context.Background(), Request{
		URL: "https://amara.org/videos/fixture01", Transport: transport,
	})
	if !errors.Is(err, ErrInvalidMetadata) {
		t.Fatalf("overflow err=%v", err)
	}
	longTitle := strings.Repeat("a", amaraMaxTitleBytes+1)
	payload, err = json.Marshal(map[string]any{
		"title": longTitle, "all_urls": []string{"https://cdn.amara.org/media/x.mp4"},
	})
	if err != nil {
		t.Fatal(err)
	}
	transport = &sharedFixtureTransport{responses: map[string]fixtureHTTP{
		amaraAPIURL("fixture02"): {body: payload},
	}}
	_, err = NewAmara().Extract(context.Background(), Request{
		URL: "https://amara.org/videos/fixture02", Transport: transport,
	})
	if !errors.Is(err, ErrInvalidMetadata) {
		t.Fatalf("long title err=%v", err)
	}
}

func TestAmaraRegistryOrderingAndIntegration(t *testing.T) {
	t.Parallel()
	registry := NewRegistry(NewAmara(), NewGeneric())
	names := registry.Names()
	found := false
	amaraBeforeGeneric := false
	for _, name := range names {
		if name == "amara" {
			found = true
		}
		if name == "generic" {
			if found {
				amaraBeforeGeneric = true
			}
			break
		}
	}
	if !found || !amaraBeforeGeneric {
		t.Fatalf("registry order=%v", names)
	}
	selected, err := registry.Select("https://amara.org/videos/jVx79ZKGK1ky")
	if err != nil || selected.Name() != "amara" {
		t.Fatalf("select=%v err=%v", selected, err)
	}
	parsed, _ := url.Parse("https://amara.org/en/series/1")
	if NewAmara().Suitable(parsed) {
		t.Fatal("non-video route must not match Amara")
	}
}

func TestAmaraConcurrentExtractionSafety(t *testing.T) {
	t.Parallel()
	transport := &sharedFixtureTransport{responses: map[string]fixtureHTTP{
		amaraAPIURL("s8KL7I3jLmh6"): {body: amaraFixture(t, "direct.json")},
	}}
	var wg sync.WaitGroup
	var failures atomic.Int32
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			result, err := NewAmara().Extract(context.Background(), Request{
				URL: "https://amara.org/videos/s8KL7I3jLmh6", Transport: transport,
			})
			if err != nil || result.IsURL() {
				failures.Add(1)
			}
		}()
	}
	wg.Wait()
	if failures.Load() != 0 {
		t.Fatalf("failures=%d", failures.Load())
	}
}

func FuzzParseAmaraURL(f *testing.F) {
	f.Add("https://amara.org/videos/jVx79ZKGK1ky/info/")
	f.Add("https://amara.org/en/videos/kYkK1VUTWW5I")
	f.Add("http://www.amara.org/videos/abc_123")
	f.Add("https://evilamara.org/videos/abc")
	f.Add("https://amara.org/videos/abc#x")
	f.Add("")
	f.Fuzz(func(t *testing.T, raw string) {
		if len(raw) > sharedHostingMaxURLBytes {
			return
		}
		parsed, err := url.Parse(raw)
		if err != nil {
			return
		}
		target, ok := parseAmaraURL(parsed)
		if ok {
			if !amaraVideoIDPattern.MatchString(target.id) || !strings.HasPrefix(target.canonical, "https://amara.org/") {
				t.Fatalf("parseAmaraURL(%q) = %#v", raw, target)
			}
			if reparsed, err := url.Parse(target.canonical); err != nil {
				t.Fatalf("canonical parse: %v", err)
			} else if again, ok := parseAmaraURL(reparsed); !ok || again.id != target.id || again.canonical != target.canonical {
				t.Fatalf("canonical round-trip failed for %q: %#v", target.canonical, again)
			}
		}
	})
}

func FuzzDecodeAmaraVideoResponse(f *testing.F) {
	f.Add(`{"title":"x","all_urls":["https://cdn.amara.org/media/x.mp4"],"languages":[{"code":"en","published":true,"subtitles_uri":"https://amara.org/api/videos/x/subtitles/en/"}]}`)
	f.Add(`{"title":"","all_urls":[]}`)
	f.Fuzz(func(t *testing.T, body string) {
		if len(body) > int(maxExtractorJSONBytes) {
			return
		}
		var payload amaraVideoResponse
		if err := amaraDecodeJSON([]byte(body), &payload); err != nil {
			return
		}
		if _, err := normalizeAmara(payload, amaraTarget{id: "fixture01", canonical: "https://amara.org/videos/fixture01"}); err != nil {
			return
		}
	})
}

func TestAmaraTimestampNormalization(t *testing.T) {
	t.Parallel()
	if got := amaraTimestamp("2016-08-13T00:00:00Z"); got <= 0 {
		t.Fatalf("timestamp=%d", got)
	}
	if got := amaraTimestamp("not-a-date"); got != 0 {
		t.Fatalf("invalid timestamp=%d", got)
	}
}

func TestAmaraUnpublishedSubtitlesExcluded(t *testing.T) {
	t.Parallel()
	id := "jVx79ZKGK1ky"
	transport := &sharedFixtureTransport{responses: map[string]fixtureHTTP{
		amaraAPIURL(id): {body: amaraFixture(t, "youtube.json")},
	}}
	result, err := NewAmara().Extract(context.Background(), Request{
		URL: "https://amara.org/videos/" + id, Transport: transport,
	})
	if err != nil {
		t.Fatal(err)
	}
	subtitles, ok := result.Info.Lookup("subtitles").Object()
	if !ok || !subtitles.Lookup("es").IsMissing() {
		t.Fatalf("unpublished subtitles leaked: %#v", subtitles)
	}
}

func amaraMustParseURL(t *testing.T, raw string) *url.URL {
	t.Helper()
	parsed, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return parsed
}

func TestAmaraSubtitleBaseURIValidation(t *testing.T) {
	t.Parallel()
	raw := "https://amara.org/api/videos/s8KL7I3jLmh6/subtitles/en/"
	if !strictValidHostedHTTPURL(raw) {
		t.Fatalf("rejected valid subtitle uri %q", raw)
	}
	entries, ok := amaraSubtitleFormatsFor(raw)
	if !ok || len(entries) != 3 {
		t.Fatalf("entries=%v ok=%v", entries, ok)
	}
	for _, unsafe := range []string{
		"javascript:alert(1)",
		"data:text/plain,subtitle",
		"https://user:pass@amara.org/api/videos/x/subtitles/en/",
		"https://localhost/api/videos/x/subtitles/en/",
		"https://amara.org:8443/api/videos/x/subtitles/en/",
		"https://amara.org/api/videos/x/subtitles/en/#fragment",
		"https://amara.org/api/videos/x/../subtitles/en/",
	} {
		if entries, ok := amaraSubtitleFormatsFor(unsafe); ok || entries != nil {
			t.Fatalf("unsafe subtitle uri accepted: %q", unsafe)
		}
	}
}

func TestAmaraProductRegistryIncludesRoute(t *testing.T) {
	t.Parallel()
	// Covered in pkg/ytdlp/client_test.go; keep extractor-level invariant here.
	if parse, ok := parseAmaraURL(amaraMustParseURL(t, "https://amara.org/videos/demo_id")); !ok || parse.id != "demo_id" {
		t.Fatalf("parse=%#v ok=%v", parse, ok)
	}
}

func TestAmaraAllURLsOverflow(t *testing.T) {
	t.Parallel()
	urls := make([]string, 0, amaraMaxAllURLs+1)
	for i := 0; i < amaraMaxAllURLs+1; i++ {
		urls = append(urls, fmt.Sprintf("https://cdn.amara.org/media/%d.mp4", i))
	}
	payload, err := json.Marshal(map[string]any{"title": "x", "all_urls": urls})
	if err != nil {
		t.Fatal(err)
	}
	transport := &sharedFixtureTransport{responses: map[string]fixtureHTTP{
		amaraAPIURL("overflow"): {body: payload},
	}}
	_, err = NewAmara().Extract(context.Background(), Request{
		URL: "https://amara.org/videos/overflow", Transport: transport,
	})
	if !errors.Is(err, ErrInvalidMetadata) {
		t.Fatalf("err=%v", err)
	}
}

func TestAmaraDeepJSONNestingRejected(t *testing.T) {
	t.Parallel()
	nested := strings.Repeat(`{"x":`, amaraMaxJSONDepth+1) + `"leaf"` + strings.Repeat(`}`, amaraMaxJSONDepth+1)
	body := fmt.Sprintf(`{"title":"x","all_urls":["https://cdn.amara.org/media/x.mp4"],"unknown":%s}`, nested)
	transport := &sharedFixtureTransport{responses: map[string]fixtureHTTP{
		amaraAPIURL("fixture01"): {body: []byte(body)},
	}}
	_, err := NewAmara().Extract(context.Background(), Request{
		URL: "https://amara.org/videos/fixture01", Transport: transport,
	})
	if !errors.Is(err, ErrInvalidMetadata) {
		t.Fatalf("deep nesting err=%v", err)
	}
}

func TestAmaraExcessiveSubtitleEntries(t *testing.T) {
	t.Parallel()
	languages := make([]map[string]any, 0, amaraMaxSubtitleEntriesPerLanguage+1)
	for i := 0; i < amaraMaxSubtitleEntriesPerLanguage+1; i++ {
		languages = append(languages, map[string]any{
			"code": "en", "published": true, "subtitles_uri": "https://amara.org/api/videos/x/subtitles/en/",
		})
	}
	payload, err := json.Marshal(map[string]any{
		"title": "x", "all_urls": []string{"https://cdn.amara.org/media/x.mp4"}, "languages": languages,
	})
	if err != nil {
		t.Fatal(err)
	}
	transport := &sharedFixtureTransport{responses: map[string]fixtureHTTP{
		amaraAPIURL("overflow"): {body: payload},
	}}
	_, err = NewAmara().Extract(context.Background(), Request{
		URL: "https://amara.org/videos/overflow", Transport: transport,
	})
	if !errors.Is(err, ErrInvalidMetadata) {
		t.Fatalf("subtitle overflow err=%v", err)
	}
}

func TestAmaraAggregateSubtitleOverflow(t *testing.T) {
	t.Parallel()
	languageCount := amaraMaxSubtitleEntries/amaraSubtitleFormats + 1
	languages := make([]map[string]any, 0, languageCount)
	for i := 0; i < languageCount; i++ {
		languages = append(languages, map[string]any{
			"code": fmt.Sprintf("x-%d", i), "published": true,
			"subtitles_uri": fmt.Sprintf("https://amara.org/api/videos/x/subtitles/x-%d/", i),
		})
	}
	payload, err := json.Marshal(map[string]any{
		"title": "x", "all_urls": []string{"https://cdn.amara.org/media/x.mp4"}, "languages": languages,
	})
	if err != nil {
		t.Fatal(err)
	}
	transport := &sharedFixtureTransport{responses: map[string]fixtureHTTP{
		amaraAPIURL("overflow"): {body: payload},
	}}
	_, err = NewAmara().Extract(context.Background(), Request{
		URL: "https://amara.org/videos/overflow", Transport: transport,
	})
	if !errors.Is(err, ErrInvalidMetadata) {
		t.Fatalf("aggregate subtitle overflow err=%v", err)
	}
}

func TestAmaraStringLengthPolicy(t *testing.T) {
	t.Parallel()
	base := map[string]any{
		"title": "ok", "all_urls": []string{"https://cdn.amara.org/media/x.mp4"},
	}
	cases := []struct {
		name string
		mut  func(map[string]any)
	}{
		{"description", func(m map[string]any) { m["description"] = strings.Repeat("d", amaraMaxDescriptionBytes+1) }},
		{"thumbnail", func(m map[string]any) { m["thumbnail"] = strings.Repeat("t", amaraMaxThumbnailBytes+1) }},
		{"created", func(m map[string]any) { m["created"] = strings.Repeat("c", amaraMaxCreatedBytes+1) }},
		{"all_urls entry", func(m map[string]any) {
			m["all_urls"] = []string{strings.Repeat("u", amaraMaxMediaURLBytes+1)}
		}},
		{"language code", func(m map[string]any) {
			m["languages"] = []map[string]any{{
				"code": strings.Repeat("l", amaraMaxLangCodeLen+1), "published": true,
				"subtitles_uri": "https://amara.org/api/videos/x/subtitles/en/",
			}}
		}},
		{"subtitles_uri", func(m map[string]any) {
			m["languages"] = []map[string]any{{
				"code": "en", "published": true, "subtitles_uri": strings.Repeat("s", amaraMaxSubtitlesURIBytes+1),
			}}
		}},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			payload := make(map[string]any, len(base)+2)
			for key, value := range base {
				payload[key] = value
			}
			test.mut(payload)
			body, err := json.Marshal(payload)
			if err != nil {
				t.Fatal(err)
			}
			transport := &sharedFixtureTransport{responses: map[string]fixtureHTTP{
				amaraAPIURL("fixture01"): {body: body},
			}}
			_, err = NewAmara().Extract(context.Background(), Request{
				URL: "https://amara.org/videos/fixture01", Transport: transport,
			})
			if !errors.Is(err, ErrInvalidMetadata) {
				t.Fatalf("err=%v", err)
			}
		})
	}
}

func TestAmaraOversizedAuthAndRateLimitBodies(t *testing.T) {
	t.Parallel()
	secret := strings.Repeat("x", int(maxExtractorJSONBytes)+128)
	for _, test := range []struct {
		status int
		want   error
	}{
		{http.StatusUnauthorized, ErrAuthentication},
		{http.StatusTooManyRequests, ErrAmaraRateLimited},
	} {
		transport := &sharedFixtureTransport{responses: map[string]fixtureHTTP{
			amaraAPIURL("fixture01"): {status: test.status, body: []byte(secret)},
		}}
		_, err := NewAmara().Extract(context.Background(), Request{
			URL: "https://amara.org/videos/fixture01", Transport: transport,
		})
		if !errors.Is(err, test.want) || strings.Contains(err.Error(), secret[:64]) {
			t.Fatalf("status=%d err=%v", test.status, err)
		}
	}
}

func TestAmaraDirectHTTPMedia(t *testing.T) {
	t.Parallel()
	id := "httpfixture"
	transport := &sharedFixtureTransport{responses: map[string]fixtureHTTP{
		amaraAPIURL(id): {body: amaraFixture(t, "direct_http.json")},
	}}
	result, err := NewAmara().Extract(context.Background(), Request{
		URL: "https://amara.org/videos/" + id, Transport: transport,
	})
	if err != nil || result.IsURL() {
		t.Fatalf("result=%#v err=%v", result, err)
	}
	formats, ok := result.Info.Formats()
	if !ok || len(formats) != 1 {
		t.Fatalf("formats=%v", formats)
	}
	format, ok := formats[0].Object()
	if !ok {
		t.Fatal("format not object")
	}
	protocol, _ := format.Lookup("protocol").StringValue()
	if protocol != "http" {
		t.Fatalf("protocol=%q", protocol)
	}
}

func TestAmaraSkipsInvalidAllURLsBeforeValid(t *testing.T) {
	t.Parallel()
	id := "skipinvalid"
	transport := &sharedFixtureTransport{responses: map[string]fixtureHTTP{
		amaraAPIURL(id): {body: amaraFixture(t, "skip_invalid_urls.json")},
	}}
	result, err := NewAmara().Extract(context.Background(), Request{
		URL: "https://amara.org/videos/" + id, Transport: transport,
	})
	if err != nil || result.IsURL() {
		t.Fatalf("result=%#v err=%v", result, err)
	}
	ext, _ := result.Info.Lookup("ext").StringValue()
	if ext != "webm" {
		t.Fatalf("ext=%q", ext)
	}
}

func TestAmaraOverlongExtensionFallsBack(t *testing.T) {
	t.Parallel()
	id := "overlongext"
	transport := &sharedFixtureTransport{responses: map[string]fixtureHTTP{
		amaraAPIURL(id): {body: amaraFixture(t, "overlong_extension.json")},
	}}
	result, err := NewAmara().Extract(context.Background(), Request{
		URL: "https://amara.org/videos/" + id, Transport: transport,
	})
	if err != nil || result.IsURL() {
		t.Fatalf("result=%#v err=%v", result, err)
	}
	ext, _ := result.Info.Lookup("ext").StringValue()
	if ext != "mp4" {
		t.Fatalf("ext=%q", ext)
	}
	formats, ok := result.Info.Formats()
	if !ok || len(formats) != 1 {
		t.Fatal("missing formats")
	}
	format, ok := formats[0].Object()
	if !ok {
		t.Fatal("format not object")
	}
	protocol, _ := format.Lookup("protocol").StringValue()
	if protocol != "https" {
		t.Fatalf("protocol=%q", protocol)
	}
}

func TestAmaraDuplicatePublishedLanguagesAggregate(t *testing.T) {
	t.Parallel()
	languages := []map[string]any{
		{"code": "en", "published": true, "subtitles_uri": "https://amara.org/api/videos/x/subtitles/en/"},
		{"code": "en", "published": true, "subtitles_uri": "https://amara.org/api/videos/x/subtitles/en/?variant=2"},
	}
	payload, err := json.Marshal(map[string]any{
		"title": "x", "all_urls": []string{"https://cdn.amara.org/media/x.mp4"}, "languages": languages,
	})
	if err != nil {
		t.Fatal(err)
	}
	transport := &sharedFixtureTransport{responses: map[string]fixtureHTTP{
		amaraAPIURL("dup"): {body: payload},
	}}
	result, err := NewAmara().Extract(context.Background(), Request{
		URL: "https://amara.org/videos/dup", Transport: transport,
	})
	if err != nil {
		t.Fatal(err)
	}
	subtitles, ok := result.Info.Lookup("subtitles").Object()
	if !ok {
		t.Fatal("missing subtitles")
	}
	en, ok := subtitles.Lookup("en").ListValue()
	if !ok || len(en) != 6 {
		t.Fatalf("duplicate language aggregate=%d", len(en))
	}
	for group := 0; group < 2; group++ {
		for index, wantExt := range []string{"json", "srt", "vtt"} {
			object, ok := en[group*3+index].Object()
			if !ok {
				t.Fatalf("entry %d not object", group*3+index)
			}
			ext, _ := object.Lookup("ext").StringValue()
			if ext != wantExt {
				t.Fatalf("group %d index %d ext=%q want %q", group, index, ext, wantExt)
			}
		}
	}
}
