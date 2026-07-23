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
	"path"
	"reflect"
	"strings"
	"testing"
	"time"
)

type vimeoFixtureTransport struct {
	page       []byte
	config     []byte
	status     int
	profile    string
	err        error
	pageReads  int
	configGets int
}

type vimeoCancelAfterContext struct {
	calls, cancelAt int
}

func (*vimeoCancelAfterContext) Deadline() (time.Time, bool) { return time.Time{}, false }
func (*vimeoCancelAfterContext) Done() <-chan struct{}       { return nil }
func (*vimeoCancelAfterContext) Value(any) any               { return nil }
func (ctx *vimeoCancelAfterContext) Err() error {
	ctx.calls++
	if ctx.calls >= ctx.cancelAt {
		return context.Canceled
	}
	return nil
}

func (*vimeoFixtureTransport) ReadPage(context.Context, string) ([]byte, http.Header, error) {
	return nil, nil, errors.New("unexpected native page request")
}

func (transport *vimeoFixtureTransport) ReadPageProfile(_ context.Context, rawURL, profile string) ([]byte, http.Header, error) {
	transport.profile, transport.pageReads = profile, transport.pageReads+1
	if rawURL != "https://vimeo.com/123456789" {
		return nil, nil, fmt.Errorf("unexpected webpage URL %q", rawURL)
	}
	if transport.err != nil {
		return nil, nil, transport.err
	}
	return append([]byte(nil), transport.page...), make(http.Header), nil
}

func (transport *vimeoFixtureTransport) Do(_ context.Context, request *http.Request) (*http.Response, error) {
	transport.configGets++
	if request.Method != http.MethodGet || request.URL.Scheme != "https" || request.URL.Host != "player.vimeo.com" || request.URL.Path != "/video/123456789/config" || request.URL.RawQuery != "token=fixture&ref=offline" || request.Header.Get("Referer") != "https://vimeo.com/123456789" || request.Header.Get("Origin") != "" || len(request.Header) != 1 {
		return nil, fmt.Errorf("unexpected config request: %s %s %v", request.Method, request.URL, request.Header)
	}
	status := transport.status
	if status == 0 {
		status = http.StatusOK
	}
	return &http.Response{StatusCode: status, Body: io.NopCloser(bytes.NewReader(transport.config)), Header: make(http.Header), Request: request}, nil
}

func (transport *vimeoFixtureTransport) DoProfile(ctx context.Context, request *http.Request, profile string) (*http.Response, error) {
	transport.profile = profile
	return transport.Do(ctx, request)
}

func TestVimeoExtractsProgressiveHLSAndDASHWithProfile(t *testing.T) {
	transport := &vimeoFixtureTransport{page: readVimeoFixture(t, "page.html"), config: readVimeoFixture(t, "config.json")}
	result, err := NewVimeo().Extract(context.Background(), Request{URL: "https://vimeo.com/123456789?caller_token=do-not-forward", Transport: transport})
	if err != nil {
		t.Fatal(err)
	}
	if transport.profile != vimeoImpersonationProfile || transport.pageReads != 1 || transport.configGets != 1 {
		t.Fatalf("profile=%q pageReads=%d configGets=%d", transport.profile, transport.pageReads, transport.configGets)
	}
	actual, err := json.Marshal(result.Info.Fields())
	if err != nil {
		t.Fatal(err)
	}
	var actualDocument, expectedDocument any
	if json.Unmarshal(actual, &actualDocument) != nil || json.Unmarshal(readVimeoFixture(t, "expected.json"), &expectedDocument) != nil {
		t.Fatal("decode comparison documents")
	}
	if !reflect.DeepEqual(actualDocument, expectedDocument) {
		t.Fatalf("metadata mismatch\nactual: %s\nexpected: %#v", actual, expectedDocument)
	}
}

func TestVimeoSuitableAndPlayerConfig(t *testing.T) {
	for _, rawURL := range []string{"https://vimeo.com/123456789", "https://player.vimeo.com/video/123456789"} {
		parsed, _ := url.Parse(rawURL)
		if !NewVimeo().Suitable(parsed) {
			t.Fatalf("Suitable(%q) = false", rawURL)
		}
	}
	parsed, _ := url.Parse("https://vimeo.com/channels/fixture")
	if NewVimeo().Suitable(parsed) {
		t.Fatal("unsupported channel URL is suitable")
	}
	page := append([]byte("window.playerConfig = "), readVimeoFixture(t, "config.json")...)
	page = append(page, ';')
	transport := &vimeoFixtureTransport{page: page}
	result, err := NewVimeo().Extract(context.Background(), Request{URL: "https://vimeo.com/123456789", Transport: transport})
	if err != nil || transport.configGets != 0 {
		t.Fatalf("player config result=%#v err=%v gets=%d", result, err, transport.configGets)
	}
}

func TestVimeoFailuresAreCategorized(t *testing.T) {
	base := vimeoConfig{}
	base.Video.Title = "Fixture"
	base.Video.Files.Progressive = append(base.Video.Files.Progressive, struct {
		URL     string `json:"url"`
		Quality string `json:"quality"`
		Width   int64  `json:"width"`
		Height  int64  `json:"height"`
		FPS     int64  `json:"fps"`
		Bitrate int64  `json:"bitrate"`
	}{URL: "https://media.example/video.mp4", Quality: "source"})
	auth := base
	auth.View = 4
	if _, err := parseVimeoConfig(auth, "1", "https://vimeo.com/1"); !errors.Is(err, ErrAuthentication) {
		t.Fatalf("auth error = %v", err)
	}
	upcoming := vimeoConfig{}
	upcoming.Video.Title = "Upcoming"
	upcoming.Video.LiveEvent.Status = "pending"
	if _, err := parseVimeoConfig(upcoming, "1", "https://vimeo.com/1"); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("upcoming error = %v", err)
	}
	if _, err := parseVimeoConfig(vimeoConfig{}, "1", "https://vimeo.com/1"); !errors.Is(err, ErrInvalidMetadata) {
		t.Fatalf("malformed error = %v", err)
	}
	transport := &vimeoFixtureTransport{page: readVimeoFixture(t, "page.html"), status: http.StatusForbidden}
	if _, err := NewVimeo().Extract(context.Background(), Request{URL: "https://vimeo.com/123456789", Transport: transport}); !errors.Is(err, ErrAuthentication) {
		t.Fatalf("HTTP auth error = %v", err)
	}
	withoutProfile := &memoryTransport{pages: map[string][]byte{"https://vimeo.com/123456789": readVimeoFixture(t, "page.html")}}
	if _, err := NewVimeo().Extract(context.Background(), Request{URL: "https://vimeo.com/123456789", Transport: withoutProfile}); !errors.Is(err, ErrTransportProfile) {
		t.Fatalf("profile error = %v", err)
	}
	initialNetwork := &vimeoFixtureTransport{err: errors.New("offline")}
	if _, err := NewVimeo().Extract(context.Background(), Request{URL: "https://vimeo.com/123456789", Transport: initialNetwork}); err == nil || strings.Contains(err.Error(), "fixture") {
		t.Fatalf("initial network error = %v", err)
	}
	badConfig := &vimeoFixtureTransport{page: readVimeoFixture(t, "page.html"), config: []byte("{")}
	if _, err := NewVimeo().Extract(context.Background(), Request{URL: "https://vimeo.com/123456789", Transport: badConfig}); !errors.Is(err, ErrInvalidMetadata) {
		t.Fatalf("config error = %v", err)
	}
	oversizedConfig := &vimeoFixtureTransport{page: readVimeoFixture(t, "page.html"), config: bytes.Repeat([]byte(" "), int(maxExtractorJSONBytes+1))}
	if _, err := NewVimeo().Extract(context.Background(), Request{URL: "https://vimeo.com/123456789", Transport: oversizedConfig}); !errors.Is(err, ErrJSONResponseTooLarge) {
		t.Fatalf("oversized config error = %v", err)
	}
}

func TestVimeoConfigURLFailsClosedWithoutRequests(t *testing.T) {
	for _, configURL := range []string{
		"http://player.vimeo.com/video/123456789/config?token=secret",
		"https://user:secret@player.vimeo.com/video/123456789/config",
		"https://player.vimeo.com:443/video/123456789/config",
		"https://player.vimeo.com/video/%2e%2e/config",
		"https://player.vimeo.com/video/%252fconfig",
		"https://player.vimeo.com/video/123456789/config#token=secret",
		"https://evil.example/video/123456789/config?token=secret",
		"https://player.vimeo.com.evil.example/video/123456789/config",
		"https://player.vimeo.com/video/123456789/config\x00",
	} {
		page := strings.Replace(string(readVimeoFixture(t, "page.html")), "https://player.vimeo.com/video/123456789/config?token=fixture&amp;ref=offline", configURL, 1)
		transport := &vimeoFixtureTransport{page: []byte(page)}
		_, err := NewVimeo().Extract(context.Background(), Request{URL: "https://vimeo.com/123456789?caller_token=do-not-forward", Transport: transport})
		if !errors.Is(err, ErrInvalidMetadata) || strings.Contains(fmt.Sprint(err), "secret") || transport.configGets != 0 {
			t.Fatalf("config URL %q: error=%v requests=%d", configURL, err, transport.configGets)
		}
	}
	accepted, ok := normalizeVimeoConfigURL("https://player.vimeo.com/video/123456789/config?token=fixture&ref=offline")
	if !ok || accepted != "https://player.vimeo.com/video/123456789/config?token=fixture&ref=offline" {
		t.Fatalf("accepted config URL = %q, %v", accepted, ok)
	}
	accepted, ok = normalizeVimeoConfigURL("https://player.vimeo.com/video/123456789/config?token=fixture%2Fencoded")
	if !ok || accepted != "https://player.vimeo.com/video/123456789/config?token=fixture%2Fencoded" {
		t.Fatalf("encoded-token config URL = %q, %v", accepted, ok)
	}
}

func TestVimeoTextTracksAreBoundedAndFailClosed(t *testing.T) {
	var mixed, empty vimeoConfig
	if err := json.Unmarshal(readVimeoFixture(t, "text_tracks_mixed.json"), &mixed); err != nil {
		t.Fatal(err)
	}
	result, err := parseVimeoConfig(mixed, "1", "https://vimeo.com/1")
	if err != nil {
		t.Fatal(err)
	}
	subtitles, ok := result.Info.Lookup("subtitles").Object()
	if !ok || subtitles.Len() != 2 || subtitles.Lookup("fr").IsMissing() || subtitles.Lookup("pt-BR").IsMissing() {
		t.Fatalf("mixed subtitles = %#v", result.Info.Lookup("subtitles"))
	}
	if err := json.Unmarshal(readVimeoFixture(t, "text_tracks_empty.json"), &empty); err != nil {
		t.Fatal(err)
	}
	result, err = parseVimeoConfig(empty, "1", "https://vimeo.com/1")
	if err != nil || !result.Info.Lookup("subtitles").IsMissing() {
		t.Fatalf("all-invalid subtitles result=%#v err=%v", result.Info.Lookup("subtitles"), err)
	}
	absent := mixed
	absent.Request.TextTracks = nil
	result, err = parseVimeoConfig(absent, "1", "https://vimeo.com/1")
	if err != nil || !result.Info.Lookup("subtitles").IsMissing() {
		t.Fatalf("absent subtitles result=%#v err=%v", result.Info.Lookup("subtitles"), err)
	}
	tooMany := mixed
	for len(tooMany.Request.TextTracks) <= vimeoMaxTextTracks {
		tooMany.Request.TextTracks = append(tooMany.Request.TextTracks, mixed.Request.TextTracks[0])
	}
	if _, err := parseVimeoConfig(tooMany, "1", "https://vimeo.com/1"); !errors.Is(err, ErrInvalidMetadata) || strings.Contains(fmt.Sprint(err), "fixture") {
		t.Fatalf("track bound error = %v", err)
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := parseVimeoConfigContext(cancelled, mixed, "1", "https://vimeo.com/1"); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled parse error = %v", err)
	}
	large := mixed
	for len(large.Request.TextTracks) < vimeoMaxTextTracks {
		large.Request.TextTracks = append(large.Request.TextTracks, mixed.Request.TextTracks[0])
	}
	interrupt := &vimeoCancelAfterContext{cancelAt: 5}
	if _, err := parseVimeoConfigContext(interrupt, large, "1", "https://vimeo.com/1"); !errors.Is(err, context.Canceled) || interrupt.calls < interrupt.cancelAt {
		t.Fatalf("large-list cancellation calls=%d err=%v", interrupt.calls, err)
	}
}

func TestNormalizeVimeoTextTrackURLRejectsHostileInputs(t *testing.T) {
	for _, rawURL := range []string{
		"http://player.vimeo.com/texttrack/a.vtt", "https://user@player.vimeo.com/texttrack/a.vtt", "https://player.vimeo.com:444/texttrack/a.vtt",
		"https://evil.example/texttrack/a.vtt?token=secret", "//evil.example/texttrack/a.vtt",
		"/texttrack%2fa.vtt", "/texttrack%5ca.vtt", "/texttrack%00a.vtt", "/texttrack%2ea.vtt",
		"/texttrack%252fa.vtt", "/texttrack%255ca.vtt", "/texttrack%2500a.vtt", "/texttrack%252ea.vtt",
		"/texttrack%25252fa.vtt", "/texttrack%25255ca.vtt", "/texttrack%252500a.vtt", "/texttrack%25252ea.vtt",
		"/texttrack/../a.vtt", "/texttrack/./a.vtt", "/texttrack/a.vtt\x00", "/texttrack/a.vtt#fragment", "javascript:alert(1)",
	} {
		if got := normalizeVimeoTextTrackURL(rawURL); got != "" {
			t.Fatalf("accepted hostile URL %q as %q", rawURL, got)
		}
	}
	if got := normalizeVimeoTextTrackURL("/texttrack/a.vtt?token=fixture"); got != "https://player.vimeo.com/texttrack/a.vtt?token=fixture" {
		t.Fatalf("relative normalization = %q", got)
	}
	if got := normalizeVimeoTextTrackURL("/texttrack/a.vtt?token=fixture%2Fencoded"); got != "https://player.vimeo.com/texttrack/a.vtt?token=fixture%2Fencoded" {
		t.Fatalf("signed query normalization = %q", got)
	}
}

func FuzzParseVimeoConfig(f *testing.F) {
	f.Add(readVimeoFixture(f, "config.json"))
	f.Add([]byte(`{"view":4}`))
	f.Add([]byte(`{"video":{"title":"x"}}`))
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 1<<20 {
			t.Skip()
		}
		var config vimeoConfig
		if json.Unmarshal(data, &config) == nil {
			result, err := parseVimeoConfig(config, "1", "https://vimeo.com/1")
			if err != nil && !errors.Is(err, ErrAuthentication) && !errors.Is(err, ErrUnavailable) && !errors.Is(err, ErrInvalidMetadata) {
				t.Fatalf("unstable parser error category: %v", err)
			}
			assertVimeoSubtitleInvariants(t, result)
		}
	})
}

func FuzzNormalizeVimeoTextTrackURL(f *testing.F) {
	f.Add("/texttrack/a.vtt?token=fixture")
	f.Add("https://evil.example/a.vtt?token=secret")
	f.Add("/texttrack%2fa.vtt")
	f.Add("/texttrack%252fa.vtt")
	f.Add("/texttrack/../a.vtt")
	f.Fuzz(func(t *testing.T, rawURL string) {
		if len(rawURL) > vimeoMaxTextURL+1 {
			t.Skip()
		}
		got := normalizeVimeoTextTrackURL(rawURL)
		if got == "" {
			return
		}
		parsed, err := url.Parse(got)
		if err != nil || parsed.Scheme != "https" || !strings.EqualFold(parsed.Hostname(), "player.vimeo.com") || parsed.User != nil || parsed.Port() != "" || parsed.Fragment != "" || vimeoUnsafePath(parsed) || len(got) > vimeoMaxTextURL {
			t.Fatalf("unsafe accepted URL %q", got)
		}
	})
}

func FuzzNormalizeVimeoConfigURL(f *testing.F) {
	f.Add("https://player.vimeo.com/video/1/config?token=fixture")
	f.Add("https://evil.example/video/1/config?token=secret")
	f.Add("https://player.vimeo.com/video/%252fconfig")
	f.Fuzz(func(t *testing.T, rawURL string) {
		if len(rawURL) > vimeoMaxConfigURL+1 {
			t.Skip()
		}
		got, ok := normalizeVimeoConfigURL(rawURL)
		if !ok {
			return
		}
		parsed, err := url.Parse(got)
		if err != nil {
			t.Fatalf("accepted malformed config URL %q", got)
		}
		escapedPath := strings.ToLower(parsed.EscapedPath())
		if parsed.Scheme != "https" || !strings.EqualFold(parsed.Hostname(), "player.vimeo.com") || parsed.User != nil || parsed.Port() != "" || parsed.Fragment != "" || parsed.RawPath != "" || parsed.Path == "" || path.Clean(parsed.Path) != parsed.Path || strings.ContainsAny(got, "\\\x00\r\n") || strings.Contains(escapedPath, "%00") || strings.Contains(escapedPath, "%2f") || strings.Contains(escapedPath, "%5c") || strings.Contains(escapedPath, "%2e") || strings.Contains(escapedPath, "%25") {
			t.Fatalf("unsafe accepted config URL %q", got)
		}
	})
}

func assertVimeoSubtitleInvariants(t *testing.T, result Extraction) {
	t.Helper()
	subtitles, ok := result.Info.Lookup("subtitles").Object()
	if !ok {
		return
	}
	for _, language := range subtitles.Fields() {
		if !validVimeoLanguage(language.Key) {
			t.Fatalf("invalid language %q", language.Key)
		}
		entries, _ := language.Value.ListValue()
		for _, entry := range entries {
			object, _ := entry.Object()
			rawURL, _ := object.Lookup("url").StringValue()
			if normalizeVimeoTextTrackURL(rawURL) != rawURL {
				t.Fatalf("unsafe subtitle URL %q", rawURL)
			}
			if extension, _ := object.Lookup("ext").StringValue(); extension != "vtt" {
				t.Fatalf("subtitle ext = %q", extension)
			}
			if name, ok := object.Lookup("name").StringValue(); ok && (name == "" || len(name) > vimeoMaxTextName) {
				t.Fatalf("invalid name %q", name)
			}
		}
	}
}

type vimeoTestHelper interface {
	Helper()
	Fatal(...any)
}

func readVimeoFixture(t vimeoTestHelper, name string) []byte {
	t.Helper()
	data, err := os.ReadFile("../../conformance/extractors/vimeo/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return data
}
