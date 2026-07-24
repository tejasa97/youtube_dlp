package extractor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path"
	"reflect"
	"strings"
	"testing"
)

type tiktokFixtureTransport struct {
	page       []byte
	profile    string
	requested  string
	nativeRead bool
	wait       bool
	started    chan struct{}
}

func (transport *tiktokFixtureTransport) ReadPage(ctx context.Context, _ string) ([]byte, http.Header, error) {
	transport.nativeRead = true
	return nil, nil, errors.New("unexpected native TikTok request")
}

func (transport *tiktokFixtureTransport) ReadPageProfile(ctx context.Context, rawURL, profile string) ([]byte, http.Header, error) {
	transport.profile, transport.requested = profile, rawURL
	if transport.wait {
		if transport.started != nil {
			close(transport.started)
		}
		<-ctx.Done()
		return nil, nil, ctx.Err()
	}
	return append([]byte(nil), transport.page...), make(http.Header), nil
}

func (*tiktokFixtureTransport) Do(context.Context, *http.Request) (*http.Response, error) {
	return nil, errors.New("unexpected TikTok API request")
}

func (*tiktokFixtureTransport) DoProfile(context.Context, *http.Request, string) (*http.Response, error) {
	return nil, errors.New("unexpected profiled TikTok API request")
}

func TestTikTokExtractsProtectedHydrationFormats(t *testing.T) {
	transport := &tiktokFixtureTransport{page: readTikTokFixture(t, "page.html")}
	result, err := NewTikTok().Extract(context.Background(), Request{
		URL:       "https://www.tiktok.com/@fixture_creator/video/7460000000000000001?lang=en&token=not-forwarded",
		Transport: transport,
	})
	if err != nil {
		t.Fatal(err)
	}
	if transport.nativeRead || transport.profile != tiktokImpersonationProfile {
		t.Fatalf("native=%v profile=%q", transport.nativeRead, transport.profile)
	}
	if transport.requested != "https://www.tiktok.com/@fixture_creator/video/7460000000000000001" {
		t.Fatalf("profiled request URL = %q", transport.requested)
	}
	actual, err := json.Marshal(result.Info.Fields())
	if err != nil {
		t.Fatal(err)
	}
	var actualDocument, expectedDocument any
	if json.Unmarshal(actual, &actualDocument) != nil || json.Unmarshal(readTikTokFixture(t, "expected.json"), &expectedDocument) != nil {
		t.Fatal("decode TikTok comparison documents")
	}
	if !reflect.DeepEqual(actualDocument, expectedDocument) {
		t.Fatalf("metadata mismatch\nactual: %s\nexpected: %s", actual, readTikTokFixture(t, "expected.json"))
	}
}

func TestTikTokSuitableAndEmbedCanonicalization(t *testing.T) {
	for _, rawURL := range []string{
		"https://www.tiktok.com/@fixture.creator/video/7460000000000000001",
		"https://tiktok.com/embed/7460000000000000001",
	} {
		parsed, _ := url.Parse(rawURL)
		if !NewTikTok().Suitable(parsed) {
			t.Fatalf("Suitable(%q) = false", rawURL)
		}
	}
	for _, rawURL := range []string{"https://example.com/@x/video/1", "https://www.tiktok.com/@x", "https://www.tiktok.com/live/1", "ftp://www.tiktok.com/@x/video/1"} {
		parsed, _ := url.Parse(rawURL)
		if NewTikTok().Suitable(parsed) {
			t.Fatalf("Suitable(%q) = true", rawURL)
		}
	}
	transport := &tiktokFixtureTransport{page: readTikTokFixture(t, "page.html")}
	_, err := NewTikTok().Extract(context.Background(), Request{URL: "https://www.tiktok.com/embed/7460000000000000001", Transport: transport})
	if err != nil || transport.requested != "https://www.tiktok.com/@_/video/7460000000000000001" {
		t.Fatalf("embed request=%q error=%v", transport.requested, err)
	}
}

func TestTikTokFailuresAreCategorizedAndSecretSafe(t *testing.T) {
	tests := []struct {
		name string
		page []byte
		want error
	}{
		{"private", readTikTokFixture(t, "private.html"), ErrAuthentication},
		{"private-account", []byte("<script id=\"__UNIVERSAL_DATA_FOR_REHYDRATION__\">{\"__DEFAULT_SCOPE__\":{\"webapp.video-detail\":{\"statusCode\":10222}}}</script>"), ErrAuthentication},
		{"blocked", readTikTokFixture(t, "unavailable.html"), ErrUnavailable},
		{"expired-cookie", []byte("<html><title>Session expired - Log in</title></html>"), ErrAuthentication},
		{"challenge", []byte("<html><title>Please wait...</title><div id=\"cs\"></div></html>"), ErrChallengeSolver},
		{"malformed", []byte("<script id=\"__UNIVERSAL_DATA_FOR_REHYDRATION__\">{\"secret\":\"private-cookie-value\"</script>"), ErrInvalidMetadata},
		{"no-formats", []byte("<script id=\"__UNIVERSAL_DATA_FOR_REHYDRATION__\">{\"__DEFAULT_SCOPE__\":{\"webapp.video-detail\":{\"statusCode\":0,\"itemInfo\":{\"itemStruct\":{\"id\":\"7460000000000000001\",\"desc\":\"x\"}}}}}</script>"), ErrInvalidMetadata},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			transport := &tiktokFixtureTransport{page: test.page}
			_, err := NewTikTok().Extract(context.Background(), Request{URL: "https://www.tiktok.com/@fixture/video/7460000000000000001", Transport: transport})
			if !errors.Is(err, test.want) {
				t.Fatalf("error = %v, want %v", err, test.want)
			}
			if err != nil && strings.Contains(err.Error(), "private-cookie-value") {
				t.Fatalf("error leaked fixture secret: %v", err)
			}
		})
	}
	withoutProfile := &memoryTransport{pages: map[string][]byte{}}
	if _, err := NewTikTok().Extract(context.Background(), Request{URL: "https://www.tiktok.com/@fixture/video/7460000000000000001", Transport: withoutProfile}); !errors.Is(err, ErrTransportProfile) {
		t.Fatalf("missing profile error = %v", err)
	}
}

func TestTikTokCancellationAndHydrationBound(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	transport := &tiktokFixtureTransport{wait: true}
	if _, err := NewTikTok().Extract(ctx, Request{URL: "https://www.tiktok.com/@fixture/video/7460000000000000001", Transport: transport}); !errors.Is(err, context.Canceled) {
		t.Fatalf("pre-cancel error = %v", err)
	}
	ctx, cancel = context.WithCancel(context.Background())
	started := make(chan struct{})
	transport = &tiktokFixtureTransport{wait: true, started: started}
	go func() { <-started; cancel() }()
	if _, err := NewTikTok().Extract(ctx, Request{URL: "https://www.tiktok.com/@fixture/video/7460000000000000001", Transport: transport}); !errors.Is(err, context.Canceled) {
		t.Fatalf("in-flight cancel error = %v", err)
	}
	oversized := fmt.Sprintf("<script id=\"__UNIVERSAL_DATA_FOR_REHYDRATION__\">%s</script>", strings.Repeat(" ", int(maxExtractorJSONBytes)+1))
	if _, err := parseTikTokPage([]byte(oversized), "1", "https://www.tiktok.com/@x/video/1"); !errors.Is(err, ErrJSONResponseTooLarge) {
		t.Fatalf("oversized hydration error = %v", err)
	}
}

func TestTikTokCaptionValidationBoundsAndCoreMetadata(t *testing.T) {
	base := tiktokItem{ID: "7460000000000000001", Description: "stable"}
	base.Video.PlayAddr.URLs = []string{"https://media.tiktok.example/video.mp4"}
	base.Video.SubtitleInfos = []tiktokCaption{
		{URL: "https://v16-webapp.tiktok.com/captions/good.vtt?sig=redacted", Language: "PT_BR", Name: "Português", Format: "webvtt"},
		{URL: "https://v16-webapp.tiktok.com/captions/fallback.srt", Language: "   ", Format: "srt"},
		{URL: "https://v16-webapp.tiktok.com/captions/bad-locale.srt", Language: "not a locale!", Format: "srt"},
		{URL: "http://v16-webapp.tiktok.com/captions/http.vtt", Language: "en", Format: "webvtt"},
		{URL: "https://evil.example/captions/x.vtt?token=do-not-leak", Language: "en", Format: "webvtt"},
		{URL: "https://v16-webapp.tiktok.com:443/captions/port.vtt", Language: "en", Format: "webvtt"},
		{URL: "https://v16-webapp.tiktok.com/captions/%2fescape.vtt", Language: "en", Format: "webvtt"},
		{URL: "https://v16-webapp.tiktok.com/captions/unknown.bin", Language: "en", Format: "wat"},
	}
	result, err := parseTikTokItem(context.Background(), base, base.ID, "https://www.tiktok.com/@x/video/7460000000000000001")
	if err != nil {
		t.Fatal(err)
	}
	id, _ := result.Info.Lookup("id").StringValue()
	title, _ := result.Info.Lookup("title").StringValue()
	if id != base.ID || title != "stable" {
		t.Fatalf("captions changed core metadata: %#v", result.Info.Fields())
	}
	subs, ok := result.Info.Lookup("subtitles").Object()
	if !ok || subs.Len() != 2 {
		t.Fatalf("subtitles = %#v", result.Info.Lookup("subtitles"))
	}
	entries, _ := subs.Lookup("pt-br").ListValue()
	entryJSON, _ := json.Marshal(entries)
	if len(entries) != 1 || !strings.Contains(string(entryJSON), "sig=redacted") {
		t.Fatalf("caption entries = %#v", entries)
	}
	if entries, _ := subs.Lookup("en").ListValue(); len(entries) != 1 {
		t.Fatalf("blank locale did not fall back to en or malformed locale was retained: %#v", entries)
	}
	allInvalid := base
	allInvalid.Video.SubtitleInfos = []tiktokCaption{{URL: "https://evil.example/captions/x.vtt?token=do-not-leak", Language: "en", Format: "webvtt"}}
	result, err = parseTikTokItem(context.Background(), allInvalid, allInvalid.ID, "https://www.tiktok.com/@x/video/7460000000000000001")
	if err != nil || !result.Info.Lookup("subtitles").IsMissing() {
		t.Fatalf("all-invalid result=%#v err=%v", result.Info.Fields(), err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	large := base
	large.Video.SubtitleInfos = make([]tiktokCaption, tiktokMaxCaptions)
	for i := range large.Video.SubtitleInfos {
		large.Video.SubtitleInfos[i] = tiktokCaption{URL: "https://v16-webapp.tiktok.com/captions/en.vtt", Language: "en", Format: "webvtt"}
	}
	cancel()
	if _, err := parseTikTokItem(ctx, large, large.ID, "https://www.tiktok.com/@x/video/7460000000000000001"); !errors.Is(err, context.Canceled) {
		t.Fatalf("caption-loop cancellation = %v", err)
	}
	overflow := base
	overflow.Video.SubtitleInfos = make([]tiktokCaption, tiktokMaxCaptions+1)
	for i := range overflow.Video.SubtitleInfos {
		overflow.Video.SubtitleInfos[i] = tiktokCaption{URL: fmt.Sprintf("https://v16-webapp.tiktok.com/captions/%03d.vtt", i), Language: "en", Format: "webvtt"}
	}
	result, err = parseTikTokItem(context.Background(), overflow, overflow.ID, "https://www.tiktok.com/@x/video/7460000000000000001")
	if err != nil {
		t.Fatal(err)
	}
	subs, _ = result.Info.Lookup("subtitles").Object()
	entries, _ = subs.Lookup("en").ListValue()
	if len(entries) != tiktokMaxCaptions {
		t.Fatalf("caption overflow was not deterministically truncated: %d", len(entries))
	}
}

func TestTikTokCaptionURLsRejectSecretsWithoutDiagnostics(t *testing.T) {
	for _, rawURL := range []string{
		"https://user:secret@v16-webapp.tiktok.com/captions/a.vtt",
		"https://v16-webapp.tiktok.com/captions/a.vtt#token=secret",
		"https://v16-webapp.tiktok.com/captions/%00a.vtt",
		"https://v16-webapp.tiktok.com/captions/%252fa.vtt",
		"https://v16-webapp.tiktok.com/captions/%255Ca.vtt",
		"https://v16-webapp.tiktok.com/captions/%2500a.vtt",
		"https://v16-webapp.tiktok.com/captions/%25252Fa.vtt",
		"https://v16-webapp.tiktok.com.evil.example/captions/a.vtt",
		"https://www.tiktok.com/captions/a.vtt",
		"https://api-v2.tiktok.com/captions/a.vtt",
		"https://v16-webapp.tiktokv.com/captions/a.vtt",
		"//v16-webapp.tiktok.com/captions/a.vtt",
		strings.Repeat("x", tiktokMaxCaptionURLBytes+1),
	} {
		if parsed, ok := parseTikTokCaptionURL(rawURL); ok || parsed != nil {
			t.Fatalf("accepted hostile caption URL %q", rawURL)
		}
	}
	signed := "https://v16-webapp.tiktok.com/captions/a.vtt?signature=a%2Fb%2500&expires=1"
	parsed, ok := parseTikTokCaptionURL(signed)
	if !ok || parsed.String() != signed {
		t.Fatalf("signed query was not preserved: %#v %v", parsed, ok)
	}
}

func FuzzParseTikTokPage(f *testing.F) {
	f.Add(readTikTokFixture(f, "page.html"))
	f.Add(readTikTokFixture(f, "private.html"))
	f.Add([]byte("<script id=\"__UNIVERSAL_DATA_FOR_REHYDRATION__\">{}</script>"))
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 1<<20 {
			t.Skip()
		}
		result, err := parseTikTokPage(data, "7460000000000000001", "https://www.tiktok.com/@fixture/video/7460000000000000001")
		id, _ := result.Info.Lookup("id").StringValue()
		if err == nil && !result.Info.Lookup("id").IsMissing() && id != "7460000000000000001" {
			t.Fatalf("accepted result changed requested id: %#v", result.Info.Fields())
		}
		if err == nil {
			webpageURL, _ := result.Info.Lookup("webpage_url").StringValue()
			if id != "7460000000000000001" || webpageURL != "https://www.tiktok.com/@fixture/video/7460000000000000001" {
				t.Fatalf("caption parsing changed core identity: %#v", result.Info.Fields())
			}
			assertTikTokSubtitleInvariants(t, result)
		}
	})
}

func FuzzParseTikTokCaptionURL(f *testing.F) {
	f.Add("https://v16-webapp.tiktok.com/captions/en.vtt?signature=redacted")
	f.Add("https://evil.example/a.vtt?token=secret")
	f.Fuzz(func(t *testing.T, rawURL string) {
		parsed, ok := parseTikTokCaptionURL(rawURL)
		if !ok {
			return
		}
		if len(parsed.String()) > tiktokMaxCaptionURLBytes || parsed.Scheme != "https" || parsed.User != nil || parsed.Port() != "" || parsed.Fragment != "" || parsed.RawPath != "" || path.Clean(parsed.Path) != parsed.Path || !tiktokCaptionHost(strings.ToLower(parsed.Hostname())) || !tiktokCaptionPathSafe(parsed.EscapedPath()) {
			t.Fatalf("unsafe accepted caption URL %q", parsed)
		}
		if ext := tiktokCaptionExtension("", parsed.Path); ext != "" && ext != "vtt" && ext != "srt" && ext != "json" {
			t.Fatalf("unexpected extension %q", ext)
		}
	})
}

func assertTikTokSubtitleInvariants(t *testing.T, result Extraction) {
	subtitles, ok := result.Info.Lookup("subtitles").Object()
	if !ok {
		return
	}
	if subtitles.Len() > tiktokMaxCaptions {
		t.Fatalf("too many subtitle languages: %d", subtitles.Len())
	}
	output := 0
	for _, field := range subtitles.Fields() {
		language := field.Key
		if language != "en" && normalizeTikTokCaptionLanguage(language) != language {
			t.Fatalf("unsafe subtitle language %q", language)
		}
		entries, ok := field.Value.ListValue()
		if !ok {
			t.Fatalf("subtitle entries are not a list: %q", language)
		}
		for _, value := range entries {
			entry, ok := value.Object()
			if !ok {
				t.Fatal("subtitle entry is not an object")
			}
			rawURL, ok := entry.Lookup("url").StringValue()
			parsed, valid := parseTikTokCaptionURL(rawURL)
			if !ok || !valid {
				t.Fatalf("unsafe emitted subtitle URL %q", rawURL)
			}
			extension, ok := entry.Lookup("ext").StringValue()
			if !ok || (extension != "vtt" && extension != "srt" && extension != "json") {
				t.Fatalf("unsafe emitted subtitle format %q", extension)
			}
			name, _ := entry.Lookup("name").StringValue()
			if len(name) > tiktokMaxCaptionText || strings.ContainsAny(name, "\x00\r\n") {
				t.Fatalf("unsafe emitted subtitle name %q", name)
			}
			output += len(language) + len(extension) + len(parsed.String()) + len(name)
		}
	}
	if output > tiktokMaxCaptionOutput {
		t.Fatalf("subtitle output bound exceeded: %d", output)
	}
}

type tiktokTestHelper interface {
	Helper()
	Fatal(...any)
}

func readTikTokFixture(t tiktokTestHelper, name string) []byte {
	t.Helper()
	data, err := os.ReadFile("../../conformance/extractors/tiktok/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return data
}
