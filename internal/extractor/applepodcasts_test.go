package extractor

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

type applePodcastsFixtureTransport struct {
	mu       sync.Mutex
	page     []byte
	err      error
	requests []string
}

func (transport *applePodcastsFixtureTransport) ReadPage(ctx context.Context, rawURL string) ([]byte, http.Header, error) {
	if err := contextError(ctx); err != nil {
		return nil, nil, err
	}
	transport.mu.Lock()
	transport.requests = append(transport.requests, rawURL)
	transport.mu.Unlock()
	if transport.err != nil {
		return nil, nil, transport.err
	}
	return append([]byte(nil), transport.page...), make(http.Header), nil
}

func (transport *applePodcastsFixtureTransport) Do(context.Context, *http.Request) (*http.Response, error) {
	return nil, errors.New("unexpected Do")
}

func applePodcastsFixture(t testing.TB, name string) []byte {
	t.Helper()
	body, err := os.ReadFile(filepath.Join("testdata", "applepodcasts", name))
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func TestApplePodcastsRouting(t *testing.T) {
	tests := []struct {
		raw string
		id  string
		ok  bool
	}{
		{"https://podcasts.apple.com/us/podcast/urbana-podcast-724-by-david-penn/id1531349107?i=1000748574256", "1000748574256", true},
		{"https://podcasts.apple.com/us/podcast/207-whitney-webb-returns/id1135137367?i=1000482637777", "1000482637777", true},
		{"https://podcasts.apple.com/podcast/207-whitney-webb-returns/id1135137367?i=1000482637777", "1000482637777", true},
		{"https://podcasts.apple.com/podcast/207-whitney-webb-returns?i=1000482637777", "1000482637777", true},
		{"https://podcasts.apple.com/podcast/id1135137367?i=1000482637777", "1000482637777", true},
		{"http://podcasts.apple.com/podcast/id1135137367?i=1000482637777", "1000482637777", true},
		{"https://podcasts.apple.com/podcast/id1135137367", "", false},
		{"https://podcasts.apple.com/us/podcast/id1135137367", "", false},
		{"https://podcasts.apple.com/podcast/?i=1000482637777", "", false},
		{"https://podcasts.apple.com/podcast/a/b/c?i=1000482637777", "", false},
		{"https://podcasts.apple.com/podcast/id1135137367?i=1000482637777&i=1000482637778", "", false},
		{"https://podcasts.apple.com/podcast/id1135137367?i=1000482637777&i=1000482637777", "", false},
		{"https://podcasts.apple.com/podcast/id1135137367?i=abc", "", false},
		{"https://podcasts.apple.com/podcast/id1135137367?i=" + strings.Repeat("1", 33), "", false},
		{"https://evil.apple.com/podcast/id1135137367?i=1000482637777", "", false},
		{"https://podcasts.apple.com.evil.test/podcast/id1135137367?i=1000482637777", "", false},
		{"ftp://podcasts.apple.com/podcast/id1135137367?i=1000482637777", "", false},
		{"https://user:pass@podcasts.apple.com/podcast/id1135137367?i=1000482637777", "", false},
		{"https://podcasts.apple.com:443/podcast/id1135137367?i=1000482637777", "", false},
		{"https://podcasts.apple.com/podcast/id1135137367?i=1000482637777#frag", "", false},
		{"https://podcasts.apple.com/podcast/id%2f1135137367?i=1000482637777", "", false},
		{"https://podcasts.apple.com/podcast/id%001135137367?i=1000482637777", "", false},
	}
	for _, test := range tests {
		parsed, err := url.Parse(test.raw)
		if err != nil {
			t.Fatalf("parse %q: %v", test.raw, err)
		}
		target, ok := parseApplePodcastsURL(parsed)
		if ok != test.ok {
			t.Fatalf("parseApplePodcastsURL(%q) ok=%v want %v", test.raw, ok, test.ok)
		}
		if NewApplePodcasts().Suitable(parsed) != test.ok {
			t.Fatalf("Suitable(%q) = %v want %v", test.raw, !test.ok, test.ok)
		}
		if test.ok && target.id != test.id {
			t.Fatalf("id(%q) = %q want %q", test.raw, target.id, test.id)
		}
	}
}

func TestApplePodcastsExtractSuccess(t *testing.T) {
	transport := &applePodcastsFixtureTransport{page: applePodcastsFixture(t, "success.html")}
	result, err := NewApplePodcasts().Extract(context.Background(), Request{
		URL:       "https://podcasts.apple.com/us/podcast/207-whitney-webb-returns/id1135137367?i=1000482637777",
		Transport: transport,
	})
	if err != nil {
		t.Fatal(err)
	}
	info := result.Info
	for key, want := range map[string]string{
		"id":          "1000482637777",
		"title":       "207 - Whitney Webb Returns",
		"episode":     "207 - Whitney Webb Returns",
		"series":      "The Tim Dillon Show",
		"description": "Episode description with & entities and markup .",
		"ext":         "mp3",
		"vcodec":      "none",
		"webpage_url": "https://podcasts.apple.com/us/podcast/207-whitney-webb-returns/id1135137367?i=1000482637777",
		"thumbnail":   "https://is1-ssl.mzstatic.example.test/image/thumb/Podcasts/v4/fixture.jpg/1200x1200bb.jpg",
	} {
		got, _ := info.Lookup(key).StringValue()
		if got != want {
			t.Fatalf("%s = %q want %q", key, got, want)
		}
	}
	if ts, ok := info.Lookup("timestamp").Int(); !ok || ts != 1593932400 {
		t.Fatalf("timestamp = %v %v", ts, ok)
	}
	if duration, ok := info.Lookup("duration").Int(); !ok || duration != 5369 {
		t.Fatalf("duration = %v %v", duration, ok)
	}
	if number, ok := info.Lookup("episode_number").Int(); !ok || number != 207 {
		t.Fatalf("episode_number = %v %v", number, ok)
	}
	formats, ok := info.Lookup("formats").ListValue()
	if !ok || len(formats) != 1 {
		t.Fatalf("formats = %#v", info.Lookup("formats"))
	}
	format, _ := formats[0].Object()
	stream, _ := format.Lookup("url").StringValue()
	if stream != "https://traffic.megaphone.fm/fixture207.mp3" {
		t.Fatalf("stream = %q", stream)
	}
	if protocol, _ := format.Lookup("protocol").StringValue(); protocol != "https" {
		t.Fatalf("format protocol = %q", protocol)
	}
	if vcodec, _ := format.Lookup("vcodec").StringValue(); vcodec != "none" {
		t.Fatalf("format vcodec = %q", vcodec)
	}
	if !format.Lookup("acodec").IsMissing() {
		t.Fatalf("format acodec unexpectedly present: %#v", format.Lookup("acodec"))
	}
	if len(transport.requests) != 1 || !strings.Contains(transport.requests[0], "i=1000482637777") {
		t.Fatalf("requests = %#v", transport.requests)
	}
}

func TestApplePodcastsExtractAttributeOrderAndArrayRoot(t *testing.T) {
	transport := &applePodcastsFixtureTransport{page: applePodcastsFixture(t, "success_attr_order.html")}
	result, err := NewApplePodcasts().Extract(context.Background(), Request{
		URL:       "https://podcasts.apple.com/us/podcast/urbana-podcast-724-by-david-penn/id1531349107?i=1000748574256",
		Transport: transport,
	})
	if err != nil {
		t.Fatal(err)
	}
	title, _ := result.Info.Lookup("title").StringValue()
	if title != "URBANA PODCAST 724 BY DAVID PENN" {
		t.Fatalf("title = %q", title)
	}
	formats, _ := result.Info.Lookup("formats").ListValue()
	format, _ := formats[0].Object()
	stream, _ := format.Lookup("url").StringValue()
	if stream != "https://cdn.example.test/audio/724.m4a" {
		t.Fatalf("stream = %q", stream)
	}
	if ext, _ := format.Lookup("ext").StringValue(); ext != "m4a" {
		t.Fatalf("ext = %q", ext)
	}
	if protocol, _ := format.Lookup("protocol").StringValue(); protocol != "https" {
		t.Fatalf("protocol = %q", protocol)
	}
	if !format.Lookup("acodec").IsMissing() {
		t.Fatalf("acodec unexpectedly present")
	}
	thumb, _ := result.Info.Lookup("thumbnail").StringValue()
	if !strings.Contains(thumb, "alt.jpg") {
		t.Fatalf("thumbnail = %q", thumb)
	}
}

func TestApplePodcastsOptionalFieldsAbsent(t *testing.T) {
	transport := &applePodcastsFixtureTransport{page: applePodcastsFixture(t, "optional_absent.html")}
	result, err := NewApplePodcasts().Extract(context.Background(), Request{
		URL:       "https://podcasts.apple.com/podcast/id1?i=1000482637777",
		Transport: transport,
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"description", "timestamp", "duration", "episode_number", "series", "thumbnail"} {
		if !result.Info.Lookup(key).IsMissing() {
			t.Fatalf("optional %s unexpectedly present", key)
		}
	}
}

func TestApplePodcastsFailureCases(t *testing.T) {
	tests := []struct {
		name string
		page string
		want error
	}{
		{"missing model", "missing_model.html", ErrInvalidMetadata},
		{"malformed json", "malformed_json.html", ErrInvalidMetadata},
		{"unsafe stream", "unsafe_stream.html", ErrInvalidMetadata},
		{"missing script", "missing_script.html", ErrInvalidMetadata},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			transport := &applePodcastsFixtureTransport{page: applePodcastsFixture(t, test.page)}
			_, err := NewApplePodcasts().Extract(context.Background(), Request{
				URL:       "https://podcasts.apple.com/podcast/id1?i=1000482637777",
				Transport: transport,
			})
			if !errors.Is(err, test.want) {
				t.Fatalf("err = %v want %v", err, test.want)
			}
			if strings.Contains(err.Error(), "1000482637777") || strings.Contains(err.Error(), "streamUrl") {
				t.Fatalf("error leaked secrets: %v", err)
			}
		})
	}
}

func TestApplePodcastsOversizedPage(t *testing.T) {
	transport := &applePodcastsFixtureTransport{page: bytes.Repeat([]byte("a"), int(maxExtractorJSONBytes)+8)}
	_, err := NewApplePodcasts().Extract(context.Background(), Request{
		URL:       "https://podcasts.apple.com/podcast/id1?i=1000482637777",
		Transport: transport,
	})
	if !errors.Is(err, ErrJSONResponseTooLarge) {
		t.Fatalf("err = %v", err)
	}
}

func TestApplePodcastsCancellationAndTransportFailure(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := NewApplePodcasts().Extract(ctx, Request{
		URL:       "https://podcasts.apple.com/podcast/id1?i=1000482637777",
		Transport: &applePodcastsFixtureTransport{page: applePodcastsFixture(t, "success.html")},
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled err = %v", err)
	}
	transport := &applePodcastsFixtureTransport{err: errors.New("dial failed token=leaked-secret")}
	_, err = NewApplePodcasts().Extract(context.Background(), Request{
		URL:       "https://podcasts.apple.com/podcast/id1?i=1000482637777",
		Transport: transport,
	})
	if err == nil || strings.Contains(err.Error(), "leaked-secret") {
		t.Fatalf("transport err = %v", err)
	}
	if errors.Is(err, ErrInvalidMetadata) || errors.Is(err, ErrAuthentication) || errors.Is(err, ErrUnavailable) {
		t.Fatalf("generic transport must not map to metadata/auth/unavailable: %v", err)
	}
}

func TestApplePodcastsHTTPStatusCategorization(t *testing.T) {
	tests := []struct {
		name string
		code int
		want error
	}{
		{"unauthorized", http.StatusUnauthorized, ErrAuthentication},
		{"forbidden", http.StatusForbidden, ErrAuthentication},
		{"not found", http.StatusNotFound, ErrUnavailable},
		{"gone", http.StatusGone, ErrUnavailable},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			transport := &applePodcastsFixtureTransport{err: &HTTPStatusError{Code: test.code}}
			_, err := NewApplePodcasts().Extract(context.Background(), Request{
				URL:       "https://podcasts.apple.com/podcast/id1?i=1000482637777",
				Transport: transport,
			})
			if !errors.Is(err, test.want) {
				t.Fatalf("err = %v want %v", err, test.want)
			}
			if errors.Is(err, ErrInvalidMetadata) {
				t.Fatalf("must not wrap ErrInvalidMetadata: %v", err)
			}
		})
	}
	for _, code := range []int{http.StatusTooManyRequests, http.StatusInternalServerError, http.StatusBadGateway} {
		t.Run("network-"+http.StatusText(code), func(t *testing.T) {
			transport := &applePodcastsFixtureTransport{err: &HTTPStatusError{Code: code}}
			_, err := NewApplePodcasts().Extract(context.Background(), Request{
				URL:       "https://podcasts.apple.com/podcast/id1?i=1000482637777",
				Transport: transport,
			})
			if err == nil || errors.Is(err, ErrInvalidMetadata) || errors.Is(err, ErrAuthentication) || errors.Is(err, ErrUnavailable) {
				t.Fatalf("unhandled status %d err = %v", code, err)
			}
			var status *HTTPStatusError
			if !errors.As(err, &status) || status.Code != code {
				t.Fatalf("expected preserved HTTPStatusError(%d), got %v", code, err)
			}
			if strings.Contains(err.Error(), "leaked") || strings.Contains(err.Error(), "token=") {
				t.Fatalf("status error leaked secrets: %v", err)
			}
		})
	}
}

func TestApplePodcastsHTTPStreamProtocol(t *testing.T) {
	page := []byte(`<script id="serialized-server-data">{"data":[{"data":{"headerButtonItems":[{"$kind":"share","modelType":"EpisodeLockup","model":{"title":"HTTP Episode","playAction":{"episodeOffer":{"streamUrl":"http://cdn.example.test/audio/ep.mp3"}}}}]}}]}</script>`)
	result, err := parseApplePodcastsPage(page, applePodcastsTarget{
		id: "1", webpageURL: "https://podcasts.apple.com/podcast/id1?i=1",
	})
	if err != nil {
		t.Fatal(err)
	}
	formats, ok := result.Info.Lookup("formats").ListValue()
	if !ok || len(formats) != 1 {
		t.Fatalf("formats = %#v", result.Info.Lookup("formats"))
	}
	format, _ := formats[0].Object()
	stream, _ := format.Lookup("url").StringValue()
	if stream != "http://cdn.example.test/audio/ep.mp3" {
		t.Fatalf("stream = %q", stream)
	}
	if protocol, _ := format.Lookup("protocol").StringValue(); protocol != "http" {
		t.Fatalf("protocol = %q want http", protocol)
	}
	if !format.Lookup("acodec").IsMissing() {
		t.Fatal("acodec should be omitted")
	}
}

func TestApplePodcastsScriptTagBoundary(t *testing.T) {
	validModel := `{"data":[{"data":{"headerButtonItems":[{"$kind":"share","modelType":"EpisodeLockup","model":{"title":"Boundary Episode","playAction":{"episodeOffer":{"streamUrl":"https://cdn.example.test/a.mp3"}}}}]}}]}`
	t.Run("rejects scripture", func(t *testing.T) {
		page := []byte(`<scripture id="serialized-server-data">` + validModel + `</scripture>`)
		_, err := parseApplePodcastsPage(page, applePodcastsTarget{id: "1", webpageURL: "https://podcasts.apple.com/podcast/id1?i=1"})
		if !errors.Is(err, ErrInvalidMetadata) {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("rejects scriptx", func(t *testing.T) {
		page := []byte(`<scriptx id="serialized-server-data">` + validModel + `</scriptx>`)
		_, err := parseApplePodcastsPage(page, applePodcastsTarget{id: "1", webpageURL: "https://podcasts.apple.com/podcast/id1?i=1"})
		if !errors.Is(err, ErrInvalidMetadata) {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("accepts whitespace after script", func(t *testing.T) {
		page := []byte("<script\n\tid=\"serialized-server-data\">" + validModel + "</script>")
		result, err := parseApplePodcastsPage(page, applePodcastsTarget{id: "1", webpageURL: "https://podcasts.apple.com/podcast/id1?i=1"})
		if err != nil {
			t.Fatal(err)
		}
		title, _ := result.Info.Lookup("title").StringValue()
		if title != "Boundary Episode" {
			t.Fatalf("title = %q", title)
		}
	})
	t.Run("ignores scripture decoy before valid script", func(t *testing.T) {
		page := []byte(`<scripture id="serialized-server-data">{"bad":true}</scripture>` +
			`<script type="application/json" id="serialized-server-data">` + validModel + `</script>`)
		result, err := parseApplePodcastsPage(page, applePodcastsTarget{id: "1", webpageURL: "https://podcasts.apple.com/podcast/id1?i=1"})
		if err != nil {
			t.Fatal(err)
		}
		title, _ := result.Info.Lookup("title").StringValue()
		if title != "Boundary Episode" {
			t.Fatalf("title = %q", title)
		}
	})
}

func TestApplePodcastsJSONNestingDepth(t *testing.T) {
	t.Run("root over depth", func(t *testing.T) {
		deep := strings.Repeat(`{"x":`, applePodcastsMaxJSONDepth+1) + `1` + strings.Repeat(`}`, applePodcastsMaxJSONDepth+1)
		page := []byte(`<script id="serialized-server-data">` + deep + `</script>`)
		_, err := parseApplePodcastsPage(page, applePodcastsTarget{id: "1", webpageURL: "https://podcasts.apple.com/podcast/id1?i=1"})
		if !errors.Is(err, ErrInvalidMetadata) || !strings.Contains(err.Error(), "nesting") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("model over depth", func(t *testing.T) {
		deepModel := strings.Repeat(`{"x":`, applePodcastsMaxJSONDepth+1) + `"y"` + strings.Repeat(`}`, applePodcastsMaxJSONDepth+1)
		page := []byte(`<script id="serialized-server-data">{"data":[{"data":{"headerButtonItems":[{"$kind":"share","modelType":"EpisodeLockup","model":` +
			deepModel + `}]}}]}</script>`)
		_, err := parseApplePodcastsPage(page, applePodcastsTarget{id: "1", webpageURL: "https://podcasts.apple.com/podcast/id1?i=1"})
		if !errors.Is(err, ErrInvalidMetadata) || !strings.Contains(err.Error(), "nesting") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("boundary depth accepted", func(t *testing.T) {
		nested := strings.Repeat(`{"a":`, applePodcastsMaxJSONDepth) + `true` + strings.Repeat(`}`, applePodcastsMaxJSONDepth)
		if err := applePodcastsValidateJSONNesting([]byte(nested)); err != nil {
			t.Fatalf("boundary nesting rejected: %v", err)
		}
		over := strings.Repeat(`{"a":`, applePodcastsMaxJSONDepth+1) + `true` + strings.Repeat(`}`, applePodcastsMaxJSONDepth+1)
		if err := applePodcastsValidateJSONNesting([]byte(over)); !errors.Is(err, ErrInvalidMetadata) {
			t.Fatalf("over-boundary err = %v", err)
		}
		// Strings must not contribute brace nesting.
		decoy := `{"title":"` + strings.Repeat(`{[`, 100) + `","playAction":{"episodeOffer":{"streamUrl":"https://cdn.example.test/a.mp3"}}}`
		if err := applePodcastsValidateJSONNesting([]byte(decoy)); err != nil {
			t.Fatalf("string braces counted: %v", err)
		}
	})
}

func TestApplePodcastsOversizedRequiredTitle(t *testing.T) {
	oversized := strings.Repeat("T", applePodcastsMaxTitleBytes+1)
	page := []byte(`<script id="serialized-server-data">{"data":[{"data":{"headerButtonItems":[{"$kind":"share","modelType":"EpisodeLockup","model":{"title":"` +
		oversized + `","playAction":{"episodeOffer":{"streamUrl":"https://cdn.example.test/a.mp3"}}}}]}}]}</script>`)
	_, err := parseApplePodcastsPage(page, applePodcastsTarget{id: "1", webpageURL: "https://podcasts.apple.com/podcast/id1?i=1"})
	if !errors.Is(err, ErrInvalidMetadata) {
		t.Fatalf("err = %v", err)
	}
	if !strings.Contains(err.Error(), "title") {
		t.Fatalf("expected title rejection, got %v", err)
	}
}

func TestApplePodcastsCleanPodcastURL(t *testing.T) {
	tests := []struct {
		in, want string
		ok       bool
	}{
		{"https://www.podtrac.com/pts/redirect.mp3/chtbl.com/track/5899E/traffic.megaphone.fm/HSW7835899191.mp3", "https://traffic.megaphone.fm/HSW7835899191.mp3", true},
		{"https://play.podtrac.com/npr-344098539/edge1.pod.npr.org/x.mp3", "https://edge1.pod.npr.org/x.mp3", true},
		{"https://https://cdn.example.test/a.m4a#frag", "https://cdn.example.test/a.m4a", true},
		{"javascript:alert(1)", "", false},
		{"https://user:pass@cdn.example.test/a.mp3", "", false},
		{"https://127.0.0.1/a.mp3", "", false},
		{"https://cdn.example.test/a.mp3\x00", "", false},
	}
	for _, test := range tests {
		got, ok := cleanApplePodcastsURL(test.in)
		if ok != test.ok || got != test.want {
			t.Fatalf("clean(%q) = %q %v want %q %v", test.in, got, ok, test.want, test.ok)
		}
	}
}

func TestApplePodcastsDeterminismAndImmutability(t *testing.T) {
	page := applePodcastsFixture(t, "success.html")
	before := append([]byte(nil), page...)
	first, err := parseApplePodcastsPage(page, applePodcastsTarget{
		id: "1000482637777", webpageURL: "https://podcasts.apple.com/podcast/id1?i=1000482637777",
	})
	if err != nil {
		t.Fatal(err)
	}
	second, err := parseApplePodcastsPage(page, applePodcastsTarget{
		id: "1000482637777", webpageURL: "https://podcasts.apple.com/podcast/id1?i=1000482637777",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(page, before) {
		t.Fatal("page mutated")
	}
	firstTitle, _ := first.Info.Lookup("title").StringValue()
	secondTitle, _ := second.Info.Lookup("title").StringValue()
	if firstTitle != secondTitle {
		t.Fatalf("non-deterministic titles %q vs %q", firstTitle, secondTitle)
	}
}

func TestApplePodcastsUnsupportedExtract(t *testing.T) {
	_, err := NewApplePodcasts().Extract(context.Background(), Request{
		URL:       "https://example.test/not-apple",
		Transport: &applePodcastsFixtureTransport{page: []byte("x")},
	})
	if !errors.Is(err, ErrUnsupported) {
		t.Fatalf("err = %v", err)
	}
	_, err = NewApplePodcasts().Extract(context.Background(), Request{
		URL: "https://podcasts.apple.com/podcast/id1?i=1000482637777",
	})
	if !errors.Is(err, ErrUnsupported) {
		t.Fatalf("nil transport err = %v", err)
	}
}
