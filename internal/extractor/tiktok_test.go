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

type tiktokRedirectResponse struct {
	status   int
	location string
}

type tiktokRedirectFixtureTransport struct {
	redirects map[string]tiktokRedirectResponse
	calls     []string
	userAgent string
	wait      bool
	started   chan struct{}
	blockURL  string
}

func (transport *tiktokRedirectFixtureTransport) redirectResponse(request *http.Request) (*http.Response, error) {
	transport.calls = append(transport.calls, request.URL.String())
	if transport.userAgent == "" {
		transport.userAgent = request.Header.Get("User-Agent")
	}
	if transport.wait && transport.blockURL == request.URL.String() {
		if transport.started != nil {
			close(transport.started)
		}
		<-request.Context().Done()
		return nil, request.Context().Err()
	}
	step, ok := transport.redirects[request.URL.String()]
	if !ok {
		return nil, fmt.Errorf("unexpected redirect request: %s", request.URL.String())
	}
	header := make(http.Header)
	if step.location != "" {
		header.Set("Location", step.location)
	}
	return &http.Response{
		StatusCode: step.status,
		Header:     header,
		Body:       http.NoBody,
		Request:    request,
	}, nil
}

func (transport *tiktokRedirectFixtureTransport) DoWithoutCookies(ctx context.Context, request *http.Request) (*http.Response, error) {
	return transport.redirectResponse(request.WithContext(ctx))
}

func (transport *tiktokRedirectFixtureTransport) DoNoRedirect(ctx context.Context, request *http.Request) (*http.Response, error) {
	return transport.redirectResponse(request.WithContext(ctx))
}

// tiktokAutoFollowIsolatedTransport mimics production network.Client: it
// implements both CookieIsolatedTransport and DoNoRedirect, but
// DoWithoutCookies follows redirects to a final 200 without exposing Location.
type tiktokAutoFollowIsolatedTransport struct {
	redirects           map[string]tiktokRedirectResponse
	withoutCookiesCalls int
	noRedirectCalls     int
}

func (transport *tiktokAutoFollowIsolatedTransport) DoWithoutCookies(ctx context.Context, request *http.Request) (*http.Response, error) {
	transport.withoutCookiesCalls++
	current := request.URL.String()
	for hops := 0; hops < 8; hops++ {
		step, ok := transport.redirects[current]
		if !ok {
			return nil, fmt.Errorf("unexpected auto-follow request: %s", current)
		}
		if step.status >= 300 && step.status < 400 && step.location != "" {
			next, err := url.Parse(step.location)
			if err != nil {
				return nil, err
			}
			if !next.IsAbs() {
				base, _ := url.Parse(current)
				next = base.ResolveReference(next)
			}
			current = next.String()
			continue
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       http.NoBody,
			Request:    request,
		}, nil
	}
	return nil, errors.New("auto-follow hop limit")
}

func (transport *tiktokAutoFollowIsolatedTransport) DoNoRedirect(ctx context.Context, request *http.Request) (*http.Response, error) {
	transport.noRedirectCalls++
	step, ok := transport.redirects[request.URL.String()]
	if !ok {
		return nil, fmt.Errorf("unexpected no-redirect request: %s", request.URL.String())
	}
	header := make(http.Header)
	if step.location != "" {
		header.Set("Location", step.location)
	}
	return &http.Response{
		StatusCode: step.status,
		Header:     header,
		Body:       http.NoBody,
		Request:    request,
	}, nil
}

func (*tiktokAutoFollowIsolatedTransport) Do(context.Context, *http.Request) (*http.Response, error) {
	return nil, errors.New("unexpected TikTok API request")
}

func (*tiktokAutoFollowIsolatedTransport) ReadPage(context.Context, string) ([]byte, http.Header, error) {
	return nil, nil, errors.New("unexpected ReadPage")
}

func (*tiktokRedirectFixtureTransport) Do(context.Context, *http.Request) (*http.Response, error) {
	return nil, errors.New("unexpected TikTok redirect Do request")
}

func (*tiktokRedirectFixtureTransport) ReadPage(context.Context, string) ([]byte, http.Header, error) {
	return nil, nil, errors.New("unexpected TikTok redirect ReadPage request")
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
		"https://vm.tiktok.com/ZTR45GpSF/",
		"https://vt.tiktok.com/ZSe4FqkKd",
		"https://www.tiktok.com/t/ZTRC5xgJp",
		"https://tiktok.com/t/ZTRC5xgJp",
	} {
		parsed, _ := url.Parse(rawURL)
		if !NewTikTok().Suitable(parsed) {
			t.Fatalf("Suitable(%q) = false", rawURL)
		}
	}
	for _, rawURL := range []string{
		"https://example.com/@x/video/1",
		"https://www.tiktok.com/@x",
		"https://www.tiktok.com/live/1",
		"ftp://www.tiktok.com/@x/video/1",
		"https://evil.tiktok.com/t/ZTRC5xgJp",
		"https://vm.tiktok.com.evil.example/ZTR45GpSF",
		"https://vm.tiktok.com/@x/video/1",
	} {
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

func TestTikTokShortLinkRedirectPrefersDoNoRedirectOverAutoFollow(t *testing.T) {
	canonical := "https://www.tiktok.com/@fixture_creator/video/7460000000000000001"
	transport := &tiktokAutoFollowIsolatedTransport{redirects: map[string]tiktokRedirectResponse{
		"https://vm.tiktok.com/ZTR45GpSF": {status: http.StatusFound, location: canonical},
	}}
	result, err := NewTikTok().Extract(context.Background(), Request{
		URL:       "https://vm.tiktok.com/ZTR45GpSF",
		Transport: transport,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.IsURL() || result.Redirect == nil || result.Redirect.URL != canonical {
		t.Fatalf("redirect result = %#v", result.Redirect)
	}
	if transport.withoutCookiesCalls != 0 {
		t.Fatalf("DoWithoutCookies called %d times; hop-by-hop must use DoNoRedirect", transport.withoutCookiesCalls)
	}
	if transport.noRedirectCalls != 1 {
		t.Fatalf("DoNoRedirect calls = %d", transport.noRedirectCalls)
	}
}

func TestTikTokShortLinkRedirectMultiHopAndRelativeLocation(t *testing.T) {
	canonical := "https://www.tiktok.com/@fixture_creator/video/7460000000000000001"
	transport := &tiktokRedirectFixtureTransport{redirects: map[string]tiktokRedirectResponse{
		"https://vm.tiktok.com/ZTR45GpSF":    {status: http.StatusFound, location: "https://vt.tiktok.com/ZSe4FqkKd"},
		"https://vt.tiktok.com/ZSe4FqkKd":    {status: http.StatusFound, location: "https://www.tiktok.com/t/ZTRC5xgJp"},
		"https://www.tiktok.com/t/ZTRC5xgJp": {status: http.StatusFound, location: "/@fixture_creator/video/7460000000000000001"},
	}}
	result, err := NewTikTok().Extract(context.Background(), Request{
		URL:       "https://vm.tiktok.com/ZTR45GpSF?sig=secret-token",
		Transport: transport,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.IsURL() || result.Redirect == nil || result.Redirect.URL != canonical {
		t.Fatalf("redirect result = %#v", result.Redirect)
	}
	if transport.userAgent != tiktokRedirectUserAgent {
		t.Fatalf("redirect user-agent = %q", transport.userAgent)
	}
	if len(transport.calls) != 3 {
		t.Fatalf("redirect calls = %#v", transport.calls)
	}
}

func TestTikTokShortLinkRedirectDirectCanonical(t *testing.T) {
	canonical := "https://www.tiktok.com/@fixture_creator/video/7460000000000000001"
	transport := &tiktokRedirectFixtureTransport{redirects: map[string]tiktokRedirectResponse{
		"https://www.tiktok.com/t/ZTRC5xgJp": {status: http.StatusFound, location: canonical},
	}}
	result, err := NewTikTok().Extract(context.Background(), Request{
		URL:       "https://www.tiktok.com/t/ZTRC5xgJp",
		Transport: transport,
	})
	if err != nil || !result.IsURL() || result.Redirect.URL != canonical {
		t.Fatalf("result=%#v err=%v", result.Redirect, err)
	}
}

func TestTikTokShortLinkRedirectLoopAndOpenRedirect(t *testing.T) {
	loopTransport := &tiktokRedirectFixtureTransport{redirects: map[string]tiktokRedirectResponse{
		"https://vm.tiktok.com/loop-a": {status: http.StatusFound, location: "https://vt.tiktok.com/loop-b"},
		"https://vt.tiktok.com/loop-b": {status: http.StatusFound, location: "https://vm.tiktok.com/loop-a"},
	}}
	if _, err := NewTikTok().Extract(context.Background(), Request{
		URL: "https://vm.tiktok.com/loop-a", Transport: loopTransport,
	}); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("loop error = %v", err)
	}

	evilTransport := &tiktokRedirectFixtureTransport{redirects: map[string]tiktokRedirectResponse{
		"https://vm.tiktok.com/open": {status: http.StatusFound, location: "https://evil.example/@x/video/1?token=secret"},
	}}
	_, err := NewTikTok().Extract(context.Background(), Request{
		URL: "https://vm.tiktok.com/open", Transport: evilTransport,
	})
	if !errors.Is(err, ErrUnsupported) {
		t.Fatalf("open redirect error = %v", err)
	}
	if err != nil && strings.Contains(err.Error(), "secret") {
		t.Fatalf("error leaked redirect secret: %v", err)
	}

	stillShort := &tiktokRedirectFixtureTransport{redirects: map[string]tiktokRedirectResponse{
		"https://vm.tiktok.com/still": {status: http.StatusOK},
	}}
	if _, err := NewTikTok().Extract(context.Background(), Request{
		URL: "https://vm.tiktok.com/still", Transport: stillShort,
	}); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("still-short error = %v", err)
	}
}

func TestTikTokShortLinkRedirectRejectsUnsafeLocations(t *testing.T) {
	tests := map[string]tiktokRedirectResponse{
		"https://vm.tiktok.com/userinfo": {status: http.StatusFound, location: "https://user:secret@www.tiktok.com/t/ZTRC5xgJp"},
		"https://vm.tiktok.com/port":     {status: http.StatusFound, location: "https://www.tiktok.com:443/t/ZTRC5xgJp"},
		"https://vm.tiktok.com/ip":       {status: http.StatusFound, location: "https://127.0.0.1/t/ZTRC5xgJp"},
		"https://vm.tiktok.com/http":     {status: http.StatusFound, location: "http://www.tiktok.com/t/ZTRC5xgJp"},
	}
	for start, step := range tests {
		transport := &tiktokRedirectFixtureTransport{redirects: map[string]tiktokRedirectResponse{start: step}}
		_, err := NewTikTok().Extract(context.Background(), Request{URL: start, Transport: transport})
		if !errors.Is(err, ErrUnsupported) {
			t.Fatalf("%s error = %v", start, err)
		}
		if err != nil && strings.Contains(err.Error(), "secret") {
			t.Fatalf("%s leaked secret: %v", start, err)
		}
	}
}

func TestTikTokShortLinkRedirectCancellationAndTransport(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	transport := &tiktokRedirectFixtureTransport{
		redirects: map[string]tiktokRedirectResponse{
			"https://vm.tiktok.com/wait": {status: http.StatusFound, location: "https://www.tiktok.com/t/ZTRC5xgJp"},
		},
		wait: true, blockURL: "https://vm.tiktok.com/wait",
	}
	if _, err := NewTikTok().Extract(ctx, Request{
		URL: "https://vm.tiktok.com/wait", Transport: transport,
	}); !errors.Is(err, context.Canceled) {
		t.Fatalf("pre-cancel error = %v", err)
	}

	ctx, cancel = context.WithCancel(context.Background())
	started := make(chan struct{})
	transport = &tiktokRedirectFixtureTransport{
		redirects: map[string]tiktokRedirectResponse{
			"https://vm.tiktok.com/wait": {status: http.StatusFound, location: "https://www.tiktok.com/t/ZTRC5xgJp"},
		},
		wait: true, started: started, blockURL: "https://vm.tiktok.com/wait",
	}
	go func() { <-started; cancel() }()
	if _, err := NewTikTok().Extract(ctx, Request{
		URL: "https://vm.tiktok.com/wait", Transport: transport,
	}); !errors.Is(err, context.Canceled) {
		t.Fatalf("in-flight cancel error = %v", err)
	}

	if _, err := NewTikTok().Extract(context.Background(), Request{
		URL: "https://vm.tiktok.com/missing-transport", Transport: &tiktokFixtureTransport{},
	}); !errors.Is(err, ErrTransportIsolation) {
		t.Fatalf("missing redirect transport error = %v", err)
	}
}

func readTikTokFixture(t tiktokTestHelper, name string) []byte {
	t.Helper()
	data, err := os.ReadFile("../../conformance/extractors/tiktok/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return data
}
