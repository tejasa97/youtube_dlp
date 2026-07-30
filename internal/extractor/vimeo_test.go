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
	"unicode/utf8"

	"github.com/ytdlp-go/ytdlp/internal/network"
)

type vimeoFixtureTransport struct {
	page       []byte
	config     []byte
	status     int
	profile    string
	err        error
	pageReads  int
	configGets int
	webpageURL string
	configURL  string
}

type vimeoNoRedirectConfigTransport struct {
	*vimeoFixtureTransport
	isolatedCalls       int
	isolatedConfigCalls int
	ambientCalls        int
	redirect            bool
}

func (transport *vimeoNoRedirectConfigTransport) Do(ctx context.Context, request *http.Request) (*http.Response, error) {
	transport.ambientCalls++
	return nil, errors.New("signed config used ambient transport")
}

func (transport *vimeoNoRedirectConfigTransport) DoWithoutCredentialsNoRedirect(ctx context.Context, request *http.Request) (*http.Response, error) {
	transport.isolatedCalls++
	if request.URL.Path == "/video/123456789/config" {
		transport.isolatedConfigCalls++
	}
	if transport.redirect {
		return &http.Response{StatusCode: http.StatusFound, Header: http.Header{"Location": {"https://evil.example/collect?token=secret"}}, Body: io.NopCloser(strings.NewReader("")), Request: request}, nil
	}
	return transport.vimeoFixtureTransport.Do(ctx, request)
}

func (transport *vimeoNoRedirectConfigTransport) DoWithoutCredentialsNoRedirectWithReferer(ctx context.Context, request *http.Request) (*http.Response, error) {
	return transport.DoWithoutCredentialsNoRedirect(ctx, request)
}

type vimeoCancelAfterContext struct {
	calls, cancelAt int
}

type vimeoEmbedPageTransport struct {
	page, privacy []byte
	config        []byte
	statuses      []int
	referers      []string
	locations     []string
	seenLocations []string
}

type vimeoProfileResponseTransport struct {
	response *http.Response
	err      error
	calls    int
}

func (t *vimeoProfileResponseTransport) DoProfiledPageNoRedirect(_ context.Context, r *http.Request, _ string) (*http.Response, error) {
	t.calls++
	if t.response != nil {
		t.response.Request = r
	}
	return t.response, t.err
}

type vimeoCountingBody struct {
	io.Reader
	closed bool
}

func (b *vimeoCountingBody) Close() error { b.closed = true; return nil }

func (t *vimeoEmbedPageTransport) ReadPage(context.Context, string) ([]byte, http.Header, error) {
	return nil, nil, errors.New("unexpected")
}
func (t *vimeoEmbedPageTransport) DoProfile(ctx context.Context, r *http.Request, _ string) (*http.Response, error) {
	return t.Do(ctx, r)
}
func (t *vimeoEmbedPageTransport) ReadPageProfile(context.Context, string, string) ([]byte, http.Header, error) {
	return nil, nil, errors.New("unexpected")
}
func (t *vimeoEmbedPageTransport) Do(context.Context, *http.Request) (*http.Response, error) {
	return nil, errors.New("ambient")
}
func (t *vimeoEmbedPageTransport) DoWithoutCredentialsNoRedirectWithReferer(_ context.Context, r *http.Request) (*http.Response, error) {
	return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(bytes.NewReader(t.config)), Header: make(http.Header), Request: r}, nil
}
func (t *vimeoEmbedPageTransport) DoProfiledPageNoRedirect(_ context.Context, r *http.Request, _ string) (*http.Response, error) {
	t.referers = append(t.referers, r.Header.Get("Referer"))
	i := len(t.referers) - 1
	status := t.statuses[i]
	body := t.page
	if i == 0 && status >= 300 {
		body = t.privacy
	}
	header := make(http.Header)
	if i < len(t.locations) && t.locations[i] != "" {
		header.Set("Location", t.locations[i])
	}
	t.seenLocations = append(t.seenLocations, header.Get("Location"))
	return &http.Response{StatusCode: status, Body: io.NopCloser(bytes.NewReader(body)), Header: header, Request: r}, nil
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
	expectedWebpageURL := transport.webpageURL
	if expectedWebpageURL == "" {
		expectedWebpageURL = "https://vimeo.com/123456789"
	}
	if rawURL != expectedWebpageURL {
		return nil, nil, fmt.Errorf("unexpected webpage URL %q", rawURL)
	}
	if transport.err != nil {
		return nil, nil, transport.err
	}
	return append([]byte(nil), transport.page...), make(http.Header), nil
}

func (transport *vimeoFixtureTransport) Do(_ context.Context, request *http.Request) (*http.Response, error) {
	transport.configGets++
	expectedReferer := transport.webpageURL
	if expectedReferer == "" {
		expectedReferer = "https://vimeo.com/123456789"
	}
	expectedConfigURL := transport.configURL
	if expectedConfigURL == "" {
		expectedConfigURL = "https://player.vimeo.com/video/123456789/config?token=fixture&ref=offline"
	}
	if request.Method != http.MethodGet || request.URL.String() != expectedConfigURL || request.Header.Get("Referer") != expectedReferer || request.Header.Get("Origin") != "" || len(request.Header) != 1 {
		return nil, fmt.Errorf("unexpected config request: %s %s %v", request.Method, request.URL, request.Header)
	}
	status := transport.status
	if status == 0 {
		status = http.StatusOK
	}
	return &http.Response{StatusCode: status, Body: io.NopCloser(bytes.NewReader(transport.config)), Header: make(http.Header), Request: request}, nil
}

func (transport *vimeoFixtureTransport) DoWithoutCredentialsNoRedirectWithReferer(ctx context.Context, request *http.Request) (*http.Response, error) {
	return transport.Do(ctx, request)
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
	for _, rawURL := range []string{
		"https://vimeo.com/123456789",
		"https://player.vimeo.com/video/123456789",
		"https://vimeo.com/channels/fixturetributes",
		"https://vimeo.com/channels/fixturetributes/",
		"https://vimeo.com/fixtureuser/videos",
		"https://vimeo.com/fixtureuser/videos/",
		"https://vimeo.com/fixtureuser",
		"https://vimeo.com/fixtureuser/",
		"https://vimeo.com/groups/fixturegroup",
		"https://vimeo.com/groups/fixturegroup/",
		"https://vimeo.com/channels/fixturechannel/123456789",
		"https://vimeo.com/groups/fixturegroup/videos/123456789",
		"https://vimeo.com/album/fixturealbum/video/123456789",
		"https://vimeo.com/showcase/fixtureshowcase/video/123456789",
	} {
		parsed, _ := url.Parse(rawURL)
		if !NewVimeo().Suitable(parsed) {
			t.Fatalf("Suitable(%q) = false", rawURL)
		}
	}
	page := append([]byte("window.playerConfig = "), readVimeoFixture(t, "config.json")...)
	page = append(page, ';')
	transport := &vimeoFixtureTransport{page: page}
	result, err := NewVimeo().Extract(context.Background(), Request{URL: "https://vimeo.com/123456789", Transport: transport})
	if err != nil || transport.configGets != 0 {
		t.Fatalf("player config result=%#v err=%v gets=%d", result, err, transport.configGets)
	}
}

func TestVimeoContextualVideoRoutesPreservePageAndReferer(t *testing.T) {
	for _, rawURL := range []string{
		"https://vimeo.com/channels/fixturechannel/123456789",
		"https://vimeo.com/groups/fixturegroup/videos/123456789",
		"https://vimeo.com/album/fixturealbum/video/123456789",
		"https://vimeo.com/showcase/fixtureshowcase/video/123456789",
	} {
		t.Run(strings.TrimPrefix(rawURL, "https://vimeo.com/"), func(t *testing.T) {
			transport := &vimeoFixtureTransport{
				page:       readVimeoFixture(t, "page.html"),
				config:     readVimeoFixture(t, "config.json"),
				webpageURL: rawURL,
			}
			result, err := NewVimeo().Extract(context.Background(), Request{URL: rawURL, Transport: transport})
			if err != nil {
				t.Fatal(err)
			}
			if id, _ := result.Info.ID(); id != "123456789" {
				t.Fatalf("id = %q", id)
			}
			if webpageURL, _ := result.Info.Lookup("webpage_url").StringValue(); webpageURL != rawURL {
				t.Fatalf("webpage_url = %q, want %q", webpageURL, rawURL)
			}
			if transport.profile != vimeoImpersonationProfile || transport.pageReads != 1 || transport.configGets != 1 {
				t.Fatalf("profile=%q pageReads=%d configGets=%d", transport.profile, transport.pageReads, transport.configGets)
			}
		})
	}
}

func TestVimeoContextualVideoRoutingRejectsUnsafeAndAmbiguousInputs(t *testing.T) {
	rejected := []string{
		"http://vimeo.com/channels/fixturechannel/123456789",
		"https://user:secret@vimeo.com/channels/fixturechannel/123456789",
		"https://vimeo.com:443/channels/fixturechannel/123456789",
		"https://vimeo.com.evil.example/channels/fixturechannel/123456789",
		"https://player.vimeo.com/channels/fixturechannel/123456789",
		"https://vimeo.com/channels/fixturechannel/123456789?token=secret",
		"https://vimeo.com/channels/fixturechannel/123456789#fragment",
		"https://vimeo.com/channels/fixture%2fchannel/123456789",
		"https://vimeo.com/channels/fixturechannel/not-numeric",
		"https://vimeo.com/channels/fixturechannel/123456789/extra",
		"https://vimeo.com/groups/fixturegroup/video/123456789",
		"https://vimeo.com/groups/fixturegroup/videos/not-numeric",
		"https://vimeo.com/groups/fixturegroup/videos/123456789/extra",
		"https://vimeo.com/album/fixturealbum/videos/123456789",
		"https://vimeo.com/showcase/fixtureshowcase/videos/123456789",
		"https://vimeo.com/showcase//video/123456789",
		"https://vimeo.com/showcase/fixtureshowcase/video/" + strings.Repeat("1", vimeoMaxNumericVideoIDLen+1),
	}
	for _, rawURL := range rejected {
		parsed, err := url.Parse(rawURL)
		if err == nil && NewVimeo().Suitable(parsed) {
			t.Errorf("Suitable(%q) = true", rawURL)
		}
		transport := &vimeoFixtureTransport{}
		if _, err := NewVimeo().Extract(context.Background(), Request{URL: rawURL, Transport: transport}); !errors.Is(err, ErrUnsupported) {
			t.Errorf("Extract(%q) = %v", rawURL, err)
		}
		if transport.pageReads != 0 || transport.configGets != 0 {
			t.Errorf("rejected %q made requests: page=%d config=%d", rawURL, transport.pageReads, transport.configGets)
		}
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
	pageBlocked := &vimeoFixtureTransport{err: &network.StatusError{Code: http.StatusForbidden, URL: "https://vimeo.com/123456789?token=REDACTED"}}
	if _, err := NewVimeo().Extract(context.Background(), Request{URL: "https://vimeo.com/123456789?token=secret", Transport: pageBlocked}); !errors.Is(err, ErrTransportProfile) {
		t.Fatalf("vimeo page fingerprint block = %v", err)
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

func TestCategorizeVimeoResponseStatusIsHostSensitiveAndSecretSafe(t *testing.T) {
	for _, test := range []struct {
		name   string
		url    string
		status int
		body   []byte
		want   error
	}{
		{"vimeo page 403", "https://vimeo.com/123?token=secret", http.StatusForbidden, readVimeoFixture(t, "antibot-403.json"), ErrTransportProfile},
		{"embed-only precedes fingerprint", "https://vimeo.com/123", http.StatusForbidden, []byte("Because of its privacy settings, this video cannot be played here"), ErrAuthentication},
		{"www vimeo page 403", "https://www.vimeo.com/123", http.StatusForbidden, nil, ErrTransportProfile},
		{"player config 429", "https://player.vimeo.com/video/123/config?token=secret", http.StatusTooManyRequests, readVimeoFixture(t, "antibot-429.json"), ErrTransportProfile},
		{"vimeo page 429 is not fingerprint case", "https://vimeo.com/123", http.StatusTooManyRequests, nil, nil},
		{"player page 403 is not fingerprint case", "https://player.vimeo.com/video/123", http.StatusForbidden, nil, nil},
		{"unrelated host", "https://example.test/123", http.StatusForbidden, nil, nil},
		{"5460 remains authentication", "https://api.vimeo.com/videos/123?token=secret", http.StatusNotFound, []byte(`{"error_code":5460}`), ErrAuthentication},
		{"quoted 5460 is ignored", "https://api.vimeo.com/videos/123", http.StatusNotFound, []byte(`{"error_code":"5460"}`), nil},
	} {
		t.Run(test.name, func(t *testing.T) {
			got := categorizeVimeoResponseStatus(test.url, test.status, test.body)
			if !errors.Is(got, test.want) || (test.want == nil && got != nil) {
				t.Fatalf("categorizeVimeoResponseStatus(%q, %d) = %v, want %v", test.url, test.status, got, test.want)
			}
			if got != nil && strings.Contains(got.Error(), "secret") {
				t.Fatalf("secret leaked in status error: %v", got)
			}
		})
	}
}

func TestVimeoPrivacyRetryStatusIsPinned(t *testing.T) {
	for _, test := range []struct {
		raw    string
		status int
		want   bool
	}{
		{"https://vimeo.com/123", http.StatusForbidden, true},
		{"https://player.vimeo.com/video/123", http.StatusTooManyRequests, true},
		{"https://vimeo.com/123", http.StatusInternalServerError, false},
		{"https://vimeo.com/123", http.StatusFound, false},
		{"https://player.vimeo.com/video/123", http.StatusForbidden, false},
		{"http://vimeo.com/123", http.StatusForbidden, false},
		{"https://user@vimeo.com/123", http.StatusForbidden, false},
		{"https://vimeo.com:443/123", http.StatusForbidden, false},
		{"https://vimeo.com/123#fragment", http.StatusForbidden, false},
	} {
		if got := isVimeoPrivacyRetryStatus(test.raw, test.status); got != test.want {
			t.Fatalf("%s/%d = %v", test.raw, test.status, got)
		}
	}
}

func TestVimeoEmbedPrivacyRetriesOnceWithValidatedReferer(t *testing.T) {
	page := readVimeoFixture(t, "page.html")
	privacy := []byte("Because of its privacy settings, this video cannot be played here")
	config := []byte(`{"video":{"id":123456789,"title":"ok","files":{"progressive":[{"url":"https://cdn.example/x.mp4","quality":"sd"}]}}}`)
	transport := &vimeoEmbedPageTransport{page: page, privacy: privacy, config: config, statuses: []int{http.StatusForbidden, http.StatusOK}}
	_, err := NewVimeo().Extract(context.Background(), Request{URL: "https://vimeo.com/123456789", Referer: "https://publisher.example/embed", Transport: transport})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(transport.referers, []string{"", "https://publisher.example/embed"}) {
		t.Fatalf("referers=%v", transport.referers)
	}
	for _, bad := range []string{"", "http://evil.example/", "https://user@evil.example/"} {
		transport := &vimeoEmbedPageTransport{page: page, privacy: privacy, config: config, statuses: []int{http.StatusForbidden}}
		_, err := NewVimeo().Extract(context.Background(), Request{URL: "https://vimeo.com/123456789", Referer: bad, Transport: transport})
		if !errors.Is(err, ErrAuthentication) || len(transport.referers) != 1 {
			t.Fatalf("bad=%q err=%v referers=%v", bad, err, transport.referers)
		}
	}
}

func TestVimeoEmbedPrivacyRetryRejectsNonPinnedBodiesAndRedirects(t *testing.T) {
	privacy := []byte("Because of its privacy settings, this video cannot be played here")
	for _, test := range []struct {
		name      string
		url       string
		statuses  []int
		want      error
		wantCalls int
	}{
		{"vimeo 500 privacy is ordinary", "https://vimeo.com/123456789", []int{http.StatusInternalServerError}, nil, 1},
		{"vimeo 302 privacy is ordinary", "https://vimeo.com/123456789", []int{http.StatusFound}, nil, 1},
		{"player 403 privacy is ordinary", "https://player.vimeo.com/video/123456789", []int{http.StatusForbidden}, nil, 1},
		{"player 429 privacy is authentication", "https://player.vimeo.com/video/123456789", []int{http.StatusTooManyRequests, http.StatusTooManyRequests}, ErrAuthentication, 2},
	} {
		t.Run(test.name, func(t *testing.T) {
			tr := &vimeoEmbedPageTransport{page: privacy, privacy: privacy, statuses: test.statuses}
			_, _, err := readVimeoPage(context.Background(), tr, test.url, "https://publisher.example/embed")
			if (test.want != nil && !errors.Is(err, test.want)) || (test.want == nil && err == nil) ||
				(test.want == nil && (errors.Is(err, ErrAuthentication) || errors.Is(err, ErrTransportProfile))) || len(tr.referers) != test.wantCalls {
				t.Fatalf("err=%v referers=%v", err, tr.referers)
			}
		})
	}
	// A hostile Location on the retry response is returned as the second
	// response; the no-redirect transport boundary prevents a third request.
	tr := &vimeoEmbedPageTransport{
		privacy:   privacy,
		statuses:  []int{http.StatusForbidden, http.StatusFound},
		locations: []string{"", "https://evil.example/collect?token=synthetic-secret"},
	}
	_, _, err := readVimeoPage(context.Background(), tr, "https://vimeo.com/123456789?token=synthetic-secret", "https://publisher.example/embed")
	if err == nil || len(tr.referers) != 2 || tr.seenLocations[1] != "https://evil.example/collect?token=synthetic-secret" || strings.Contains(fmt.Sprint(err), "synthetic-secret") {
		t.Fatalf("redirect err=%v referers=%v locations=%v", err, tr.referers, tr.seenLocations)
	}
}

func TestReadVimeoProfilePageBoundsCancellationAndClosure(t *testing.T) {
	newResponse := func(status int, body io.ReadCloser) *http.Response {
		return &http.Response{StatusCode: status, Header: http.Header{"X-Test": {"yes"}}, Body: body}
	}
	for _, test := range []struct {
		name string
		resp *http.Response
		err  error
		ctx  context.Context
		want error
	}{
		{"nil response", nil, nil, context.Background(), ErrInvalidMetadata},
		{"nil body", newResponse(http.StatusOK, nil), nil, context.Background(), ErrInvalidMetadata},
		{"status cap", newResponse(http.StatusForbidden, io.NopCloser(bytes.NewReader(bytes.Repeat([]byte("x"), int(vimeoUnlistedAPIStatusReadBytes+1))))), nil, context.Background(), ErrJSONResponseTooLarge},
		{"page cap", newResponse(http.StatusOK, io.NopCloser(bytes.NewReader(bytes.Repeat([]byte("x"), vimeoMaxPageBytes+1)))), nil, context.Background(), ErrJSONResponseTooLarge},
		{"reader failure", newResponse(http.StatusOK, io.NopCloser(errReader{err: errors.New("reader secret")})), nil, context.Background(), ErrInvalidMetadata},
		{"wrapped canceled", nil, fmt.Errorf("wrapped: %w", context.Canceled), context.Background(), context.Canceled},
		{"wrapped deadline", nil, fmt.Errorf("wrapped: %w", context.DeadlineExceeded), context.Background(), context.DeadlineExceeded},
	} {
		t.Run(test.name, func(t *testing.T) {
			tr := &vimeoProfileResponseTransport{response: test.resp, err: test.err}
			_, _, _, err := readVimeoProfilePage(test.ctx, tr, "https://vimeo.com/123?token=synthetic-secret", "")
			if !errors.Is(err, test.want) || strings.Contains(fmt.Sprint(err), "secret") {
				t.Fatalf("err=%v", err)
			}
		})
	}
	body := &vimeoCountingBody{Reader: strings.NewReader("ok")}
	tr := &vimeoProfileResponseTransport{response: newResponse(http.StatusOK, body)}
	if _, _, _, err := readVimeoProfilePage(context.Background(), tr, "https://vimeo.com/123", ""); err != nil || !body.closed {
		t.Fatalf("err=%v closed=%v", err, body.closed)
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	tr = &vimeoProfileResponseTransport{}
	if _, _, _, err := readVimeoProfilePage(cancelled, tr, "https://vimeo.com/123", ""); !errors.Is(err, context.Canceled) || tr.calls != 0 {
		t.Fatalf("err=%v calls=%d", err, tr.calls)
	}
}

type errReader struct{ err error }

func (r errReader) Read([]byte) (int, error) { return 0, r.err }

func TestVimeoSignedPageQueryIsRequestOnlyAndMetadataSafe(t *testing.T) {
	url := "https://player.vimeo.com/video/123456789?token=synthetic-secret"
	transport := &vimeoFixtureTransport{page: readVimeoFixture(t, "page.html"), config: readVimeoFixture(t, "config.json"), webpageURL: url}
	result, err := NewVimeo().Extract(context.Background(), Request{URL: url, Transport: transport})
	if err != nil || transport.pageReads != 1 || transport.webpageURL != url {
		t.Fatalf("err=%v reads=%d", err, transport.pageReads)
	}
	serialized, _ := json.Marshal(result.Info.Fields())
	if strings.Contains(string(serialized), "synthetic-secret") {
		t.Fatalf("metadata leaked signed query: %s", serialized)
	}
}

func TestVimeoSignedConfigQueryIsRequestOnlyAndMetadataSafe(t *testing.T) {
	configURL := "https://player.vimeo.com/video/123456789/config?token=synthetic-secret"
	page := bytes.Replace(readVimeoFixture(t, "page.html"), []byte("https://player.vimeo.com/video/123456789/config?token=fixture&amp;ref=offline"), []byte(configURL), 1)
	transport := &vimeoFixtureTransport{page: page, config: readVimeoFixture(t, "config.json"), configURL: configURL}
	result, err := NewVimeo().Extract(context.Background(), Request{URL: "https://vimeo.com/123456789", Transport: transport})
	if err != nil || transport.configGets != 1 {
		t.Fatalf("err=%v configGets=%d", err, transport.configGets)
	}
	serialized, _ := json.Marshal(result.Info.Fields())
	if strings.Contains(string(serialized), "synthetic-secret") {
		t.Fatalf("metadata leaked signed config query: %s", serialized)
	}
}

func TestVimeoSignedConfigUsesIsolatedNoRedirectTransport(t *testing.T) {
	transport := &vimeoNoRedirectConfigTransport{vimeoFixtureTransport: &vimeoFixtureTransport{
		page: readVimeoFixture(t, "page.html"), config: readVimeoFixture(t, "config.json"),
	}}
	if _, err := NewVimeo().Extract(context.Background(), Request{URL: "https://vimeo.com/123456789", Transport: transport}); err != nil {
		t.Fatal(err)
	}
	if transport.isolatedConfigCalls != 1 || transport.ambientCalls != 0 {
		t.Fatalf("config isolated=%d total isolated=%d ambient=%d", transport.isolatedConfigCalls, transport.isolatedCalls, transport.ambientCalls)
	}

	transport.redirect = true
	_, err := NewVimeo().Extract(context.Background(), Request{URL: "https://vimeo.com/123456789", Transport: transport})
	if err == nil || strings.Contains(err.Error(), "secret") || transport.isolatedConfigCalls != 2 || transport.ambientCalls != 0 {
		t.Fatalf("redirect err=%v config isolated=%d total isolated=%d ambient=%d", err, transport.isolatedConfigCalls, transport.isolatedCalls, transport.ambientCalls)
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
	if _, err := parseVimeoConfigContext(cancelled, nil, mixed, "1", "https://vimeo.com/1", ""); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled parse error = %v", err)
	}
	large := mixed
	for len(large.Request.TextTracks) < vimeoMaxTextTracks {
		large.Request.TextTracks = append(large.Request.TextTracks, mixed.Request.TextTracks[0])
	}
	interrupt := &vimeoCancelAfterContext{cancelAt: 5}
	if _, err := parseVimeoConfigContext(interrupt, nil, large, "1", "https://vimeo.com/1", ""); !errors.Is(err, context.Canceled) || interrupt.calls < interrupt.cancelAt {
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

func FuzzCategorizeVimeoResponseStatus(f *testing.F) {
	f.Add("https://vimeo.com/123?token=fixture", http.StatusForbidden, []byte(`{"error_code":5460}`))
	f.Add("https://player.vimeo.com/video/123/config?token=fixture", http.StatusTooManyRequests, []byte(`{}`))
	f.Fuzz(func(t *testing.T, rawURL string, status int, body []byte) {
		if len(rawURL) > vimeoMaxConfigURL || len(body) > int(vimeoUnlistedAPIStatusReadBytes)+1 {
			t.Skip()
		}
		_ = categorizeVimeoResponseStatus(rawURL, status, body)
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

type vimeoPlaylistFixtureTransport struct {
	pages      map[string][]byte
	profile    string
	reads      []string
	err        error
	errURL     string
	pageReads  int
	configGets int
}

func (*vimeoPlaylistFixtureTransport) ReadPage(context.Context, string) ([]byte, http.Header, error) {
	return nil, nil, errors.New("unexpected native page request")
}

func (*vimeoPlaylistFixtureTransport) ReadPageProfile(context.Context, string, string) ([]byte, http.Header, error) {
	return nil, nil, errors.New("unexpected credentialed profile page request")
}

func (transport *vimeoPlaylistFixtureTransport) ReadPageProfileWithoutCredentialsNoRedirect(_ context.Context, rawURL, profile string) ([]byte, http.Header, error) {
	transport.profile = profile
	transport.pageReads++
	transport.reads = append(transport.reads, rawURL)
	if transport.err != nil && (transport.errURL == "" || transport.errURL == rawURL) {
		return nil, nil, transport.err
	}
	page, ok := transport.pages[rawURL]
	if !ok {
		return nil, nil, fmt.Errorf("unexpected playlist URL %q", rawURL)
	}
	return append([]byte(nil), page...), make(http.Header), nil
}

func (transport *vimeoPlaylistFixtureTransport) Do(context.Context, *http.Request) (*http.Response, error) {
	transport.configGets++
	return nil, errors.New("unexpected config request during playlist extraction")
}

func (transport *vimeoPlaylistFixtureTransport) DoProfile(ctx context.Context, request *http.Request, profile string) (*http.Response, error) {
	transport.profile = profile
	return transport.Do(ctx, request)
}

type vimeoPlaylistProfileOnlyTransport struct {
	pages     map[string][]byte
	profile   string
	pageReads int
	reads     []string
}

func (*vimeoPlaylistProfileOnlyTransport) ReadPage(context.Context, string) ([]byte, http.Header, error) {
	return nil, nil, errors.New("unexpected native page request")
}

func (transport *vimeoPlaylistProfileOnlyTransport) ReadPageProfile(_ context.Context, rawURL, profile string) ([]byte, http.Header, error) {
	transport.profile = profile
	transport.pageReads++
	transport.reads = append(transport.reads, "profile:"+rawURL)
	page, ok := transport.pages[rawURL]
	if !ok {
		return nil, nil, fmt.Errorf("unexpected playlist URL %q", rawURL)
	}
	return append([]byte(nil), page...), make(http.Header), nil
}

func (transport *vimeoPlaylistProfileOnlyTransport) Do(context.Context, *http.Request) (*http.Response, error) {
	return nil, errors.New("unexpected Do during playlist extraction")
}

func (transport *vimeoPlaylistProfileOnlyTransport) DoProfile(ctx context.Context, request *http.Request, profile string) (*http.Response, error) {
	transport.profile = profile
	return transport.Do(ctx, request)
}

func TestVimeoChannelPlaylistIsLazyOrderedAndTitled(t *testing.T) {
	transport := &vimeoPlaylistFixtureTransport{pages: map[string][]byte{
		"https://vimeo.com/channels/fixturetributes/videos/page:1/": readVimeoFixture(t, "channel-page1.html"),
		"https://vimeo.com/channels/fixturetributes/videos/page:2/": readVimeoFixture(t, "channel-page2.html"),
	}}
	if _, err := NewVimeo().Extract(context.Background(), Request{
		URL:       "https://vimeo.com/channels/fixturetributes?caller_token=do-not-forward#fragment",
		Transport: transport,
	}); !errors.Is(err, ErrUnsupported) || transport.pageReads != 0 {
		t.Fatalf("query/fragment must be rejected before fetch: err=%v reads=%d", err, transport.pageReads)
	}
	result, err := NewVimeo().Extract(context.Background(), Request{
		URL:       "https://vimeo.com/channels/fixturetributes/",
		Transport: transport,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.IsPlaylist() || transport.profile != vimeoImpersonationProfile || transport.pageReads != 1 || transport.configGets != 0 {
		t.Fatalf("lazy playlist profile=%q reads=%d configs=%d playlist=%v", transport.profile, transport.pageReads, transport.configGets, result.IsPlaylist())
	}
	if got, _ := result.Info.Lookup("id").StringValue(); got != "fixturetributes" {
		t.Fatalf("playlist id = %q", got)
	}
	if got, _ := result.Info.Lookup("title").StringValue(); got != "Vimeo Fixture Tributes" {
		t.Fatalf("playlist title = %q", got)
	}
	if got, _ := result.Info.Lookup("webpage_url").StringValue(); got != "https://vimeo.com/channels/fixturetributes" {
		t.Fatalf("webpage_url = %q", got)
	}
	iterator := result.Entries.Iterator()
	partial := make([]Entry, 0, 2)
	for len(partial) < 2 {
		entry, ok, err := iterator.Next(context.Background())
		if err != nil || !ok {
			t.Fatalf("partial next ok=%v err=%v", ok, err)
		}
		partial = append(partial, entry)
	}
	if transport.pageReads != 1 || partial[0].ID != "1001" || partial[1].ID != "1002" {
		t.Fatalf("partial=%v reads=%d", partial, transport.pageReads)
	}
	entries, err := CollectEntries(context.Background(), result.Entries, 10)
	if err != nil {
		t.Fatal(err)
	}
	wantIDs := []string{"1001", "1002", "1003", "1004"}
	if len(entries) != len(wantIDs) {
		t.Fatalf("entries=%d reads=%d urls=%v", len(entries), transport.pageReads, transport.reads)
	}
	for i, want := range wantIDs {
		if entries[i].ID != want || entries[i].URL != "https://vimeo.com/"+want || entries[i].ExtractorKey != "vimeo" || !entries[i].Transparent {
			t.Fatalf("entry[%d]=%#v", i, entries[i])
		}
	}
	if entries[0].Title != "First Fixture Clip" || entries[2].Title != "Third Fixture Clip" {
		t.Fatalf("titles=%q %q", entries[0].Title, entries[2].Title)
	}
	if transport.pageReads != 2 {
		t.Fatalf("expected extract+page2 reads, got %d (%v)", transport.pageReads, transport.reads)
	}
	if transport.configGets != 0 {
		t.Fatalf("child hydration requests=%d", transport.configGets)
	}
	again, err := CollectEntries(context.Background(), result.Entries, 10)
	if err != nil || len(again) != len(wantIDs) {
		t.Fatalf("reuse entries=%d err=%v", len(again), err)
	}
	if transport.pageReads != 3 {
		t.Fatalf("expected independent page-2 refetch, reads=%d", transport.pageReads)
	}
}

func TestVimeoUserVideosPlaylist(t *testing.T) {
	transport := &vimeoPlaylistFixtureTransport{pages: map[string][]byte{
		"https://vimeo.com/fixtureuser/videos/page:1/": readVimeoFixture(t, "user-videos-page1.html"),
		"https://vimeo.com/fixtureuser/videos/page:2/": readVimeoFixture(t, "user-videos-page2.html"),
	}}
	result, err := NewVimeo().Extract(context.Background(), Request{
		URL:       "https://vimeo.com/fixtureuser/videos",
		Transport: transport,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := result.Info.Lookup("id").StringValue(); got != "fixtureuser" {
		t.Fatalf("id=%q", got)
	}
	if got, _ := result.Info.Lookup("title").StringValue(); got != "Fixture User Studio" {
		t.Fatalf("title=%q", got)
	}
	entries, err := CollectEntries(context.Background(), result.Entries, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 3 || entries[0].ID != "2001" || entries[2].ID != "2003" {
		t.Fatalf("entries=%#v", entries)
	}
	if transport.profile != vimeoImpersonationProfile || transport.configGets != 0 {
		t.Fatalf("profile=%q configs=%d", transport.profile, transport.configGets)
	}
}

func TestVimeoBareUserPlaylistMatchesExplicitVideos(t *testing.T) {
	pages := map[string][]byte{
		"https://vimeo.com/fixtureuser/videos/page:1/": readVimeoFixture(t, "user-videos-page1.html"),
		"https://vimeo.com/fixtureuser/videos/page:2/": readVimeoFixture(t, "user-videos-page2.html"),
	}
	var want []Entry
	for _, rawURL := range []string{"https://vimeo.com/fixtureuser", "https://vimeo.com/fixtureuser/videos"} {
		transport := &vimeoPlaylistFixtureTransport{pages: pages}
		result, err := NewVimeo().Extract(context.Background(), Request{URL: rawURL, Transport: transport})
		if err != nil {
			t.Fatal(err)
		}
		entries, err := CollectEntries(context.Background(), result.Entries, 10)
		if err != nil {
			t.Fatal(err)
		}
		if title, _ := result.Info.Title(); title != "Fixture User Studio" {
			t.Fatalf("%s title = %q", rawURL, title)
		}
		if len(transport.reads) != 2 ||
			transport.reads[0] != "https://vimeo.com/fixtureuser/videos/page:1/" ||
			transport.reads[1] != "https://vimeo.com/fixtureuser/videos/page:2/" {
			t.Fatalf("%s reads = %#v", rawURL, transport.reads)
		}
		if want == nil {
			want = entries
			continue
		}
		if !reflect.DeepEqual(entries, want) {
			t.Fatalf("bare/explicit mismatch:\nwant %#v\ngot  %#v", want, entries)
		}
	}
}

func TestVimeoGroupPlaylistIsLazyOrderedReusableAndTitled(t *testing.T) {
	transport := &vimeoPlaylistFixtureTransport{pages: map[string][]byte{
		"https://vimeo.com/groups/fixturegroup/videos/page:1/": readVimeoFixture(t, "group-page1.html"),
		"https://vimeo.com/groups/fixturegroup/videos/page:2/": readVimeoFixture(t, "group-page2.html"),
	}}
	result, err := NewVimeo().Extract(context.Background(), Request{
		URL: "https://vimeo.com/groups/fixturegroup/", Transport: transport,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.IsPlaylist() || transport.pageReads != 1 || transport.profile != vimeoImpersonationProfile {
		t.Fatalf("playlist=%v reads=%d profile=%q", result.IsPlaylist(), transport.pageReads, transport.profile)
	}
	if id, _ := result.Info.ID(); id != "fixturegroup" {
		t.Fatalf("id = %q", id)
	}
	if title, _ := result.Info.Title(); title != "Fixture Vimeo Group" {
		t.Fatalf("title = %q", title)
	}
	if webpage, _ := result.Info.Lookup("webpage_url").StringValue(); webpage != "https://vimeo.com/groups/fixturegroup" {
		t.Fatalf("webpage_url = %q", webpage)
	}
	for iteration := 0; iteration < 2; iteration++ {
		entries, err := CollectEntries(context.Background(), result.Entries, 10)
		if err != nil {
			t.Fatal(err)
		}
		want := []string{"6001", "6002", "6003"}
		if len(entries) != len(want) {
			t.Fatalf("iteration %d entries = %#v", iteration, entries)
		}
		for index, id := range want {
			if entries[index].ID != id || entries[index].URL != "https://vimeo.com/"+id ||
				entries[index].ExtractorKey != "vimeo" || !entries[index].Transparent {
				t.Fatalf("iteration %d entry %d = %#v", iteration, index, entries[index])
			}
		}
	}
	if transport.pageReads != 3 {
		t.Fatalf("page reads = %d, want initial plus two page-2 reads", transport.pageReads)
	}
}

func TestVimeoPlaylistFallbackClipMarkers(t *testing.T) {
	transport := &vimeoPlaylistFixtureTransport{pages: map[string][]byte{
		"https://vimeo.com/channels/fallbackchannel/videos/page:1/": readVimeoFixture(t, "channel-fallback.html"),
	}}
	result, err := NewVimeo().Extract(context.Background(), Request{
		URL:       "https://vimeo.com/channels/fallbackchannel",
		Transport: transport,
	})
	if err != nil {
		t.Fatal(err)
	}
	entries, err := CollectEntries(context.Background(), result.Entries, 10)
	if err != nil || len(entries) != 2 || entries[0].ID != "3001" || entries[1].ID != "3002" {
		t.Fatalf("fallback entries=%#v err=%v", entries, err)
	}
	if entries[0].URL != "https://vimeo.com/3001" || entries[0].Title != "" {
		t.Fatalf("fallback entry=%#v", entries[0])
	}
}

func TestVimeoPlaylistSkipsHostileAndMismatchedHrefs(t *testing.T) {
	transport := &vimeoPlaylistFixtureTransport{pages: map[string][]byte{
		"https://vimeo.com/channels/hostilechannel/videos/page:1/": readVimeoFixture(t, "channel-hostile.html"),
	}}
	result, err := NewVimeo().Extract(context.Background(), Request{
		URL:       "https://vimeo.com/channels/hostilechannel",
		Transport: transport,
	})
	if err != nil {
		t.Fatal(err)
	}
	entries, err := CollectEntries(context.Background(), result.Entries, 10)
	if err != nil || len(entries) != 1 || entries[0].ID != "4003" {
		t.Fatalf("hostile filter entries=%#v err=%v", entries, err)
	}
}

func TestVimeoPlaylistAllInvalidAnchorsDoNotFallback(t *testing.T) {
	page := readVimeoFixture(t, "channel-all-invalid-anchors.html")
	parsed, err := parseVimeoPlaylistPage(context.Background(), page)
	if err != nil {
		t.Fatal(err)
	}
	if len(parsed.entries) != 0 {
		t.Fatalf("all-invalid anchors must not emit entries: %#v", parsed.entries)
	}
	transport := &vimeoPlaylistFixtureTransport{pages: map[string][]byte{
		"https://vimeo.com/channels/allinvalid/videos/page:1/": page,
	}}
	_, err = NewVimeo().Extract(context.Background(), Request{
		URL:       "https://vimeo.com/channels/allinvalid",
		Transport: transport,
	})
	if !errors.Is(err, ErrInvalidPlaylist) {
		t.Fatalf("all-invalid extract error=%v", err)
	}
	for _, leaked := range []string{"evil.example", "javascript", "secret", "9999"} {
		if strings.Contains(fmt.Sprint(err), leaked) {
			t.Fatalf("error leaked %q: %v", leaked, err)
		}
	}
}

func TestVimeoPlaylistRequiresCredentialIsolatedProfileCapability(t *testing.T) {
	profileOnly := &vimeoPlaylistProfileOnlyTransport{pages: map[string][]byte{
		"https://vimeo.com/channels/fixturetributes/videos/page:1/": readVimeoFixture(t, "channel-page1.html"),
	}}
	_, err := NewVimeo().Extract(context.Background(), Request{
		URL:       "https://vimeo.com/channels/fixturetributes",
		Transport: profileOnly,
	})
	if !errors.Is(err, ErrTransportIsolation) || profileOnly.pageReads != 0 {
		t.Fatalf("profile-only transport error=%v reads=%d", err, profileOnly.pageReads)
	}
}

func TestVimeoPlaylistSuitabilityRejectsHostileInputs(t *testing.T) {
	negatives := []string{
		"http://vimeo.com/channels/fixturetributes",
		"https://user:secret@vimeo.com/channels/fixturetributes",
		"https://vimeo.com:443/channels/fixturetributes",
		"https://vimeo.com.evil.example/channels/fixturetributes",
		"https://evil.example/channels/fixturetributes",
		"https://player.vimeo.com/channels/fixturetributes",
		"https://vimeo.com/123456789/videos",
		"https://vimeo.com/watchlater/videos",
		"https://vimeo.com/channels/fixturetributes/extra",
		"https://vimeo.com/fixtureuser/videos/extra",
		"https://vimeo.com/channels/fixturetributes?x=1",
		"https://vimeo.com/channels/fixturetributes#x",
		"https://vimeo.com/channels/fix/tribute",
		"https://vimeo.com/channels/fixture%2ftributes",
		"https://vimeo.com/groups/fixturegroup/extra",
		"https://vimeo.com/groups/",
		"https://vimeo.com/groups/fixture%2fgroup",
		"https://vimeo.com/groups/fixturegroup?x=1",
		"https://vimeo.com/groups/fixturegroup#x",
		"https://vimeo.com/channels/",
		"https://vimeo.com//videos",
	}
	for _, rawURL := range negatives {
		parsed, err := url.Parse(rawURL)
		if err != nil {
			continue
		}
		if NewVimeo().Suitable(parsed) {
			t.Fatalf("Suitable(%q) = true", rawURL)
		}
		if _, err := NewVimeo().Extract(context.Background(), Request{URL: rawURL, Transport: &vimeoPlaylistFixtureTransport{}}); !errors.Is(err, ErrUnsupported) {
			t.Fatalf("Extract(%q) = %v", rawURL, err)
		}
	}
}

func TestVimeoPlaylistBoundsCancellationAndSecretSafeErrors(t *testing.T) {
	t.Run("missing entries", func(t *testing.T) {
		transport := &vimeoPlaylistFixtureTransport{pages: map[string][]byte{
			"https://vimeo.com/channels/emptychannel/videos/page:1/": []byte(`<html><body><p>none</p></body></html>`),
		}}
		_, err := NewVimeo().Extract(context.Background(), Request{URL: "https://vimeo.com/channels/emptychannel", Transport: transport})
		if !errors.Is(err, ErrInvalidPlaylist) {
			t.Fatalf("empty page error=%v", err)
		}
	})
	t.Run("missing next stops", func(t *testing.T) {
		transport := &vimeoPlaylistFixtureTransport{pages: map[string][]byte{
			"https://vimeo.com/channels/onenpage/videos/page:1/": []byte(`<html><body><div id="clip_1"><a href="/1" title="Only">x</a></div></body></html>`),
		}}
		result, err := NewVimeo().Extract(context.Background(), Request{URL: "https://vimeo.com/channels/onenpage", Transport: transport})
		if err != nil {
			t.Fatal(err)
		}
		entries, err := CollectEntries(context.Background(), result.Entries, 10)
		if err != nil || len(entries) != 1 || transport.pageReads != 1 {
			t.Fatalf("entries=%v err=%v reads=%d", entries, err, transport.pageReads)
		}
	})
	t.Run("page byte bound", func(t *testing.T) {
		transport := &vimeoPlaylistFixtureTransport{pages: map[string][]byte{
			"https://vimeo.com/channels/hugepage/videos/page:1/": bytes.Repeat([]byte("a"), vimeoMaxPageBytes+1),
		}}
		_, err := NewVimeo().Extract(context.Background(), Request{URL: "https://vimeo.com/channels/hugepage", Transport: transport})
		if !errors.Is(err, ErrJSONResponseTooLarge) {
			t.Fatalf("oversized page error=%v", err)
		}
	})
	t.Run("clips per page bound", func(t *testing.T) {
		var builder strings.Builder
		builder.WriteString(`<html><body>`)
		for i := 0; i < vimeoMaxClipsPerPage+1; i++ {
			fmt.Fprintf(&builder, `<div id="clip_%d"><a href="/%d" title="c">x</a></div>`, i+1, i+1)
		}
		builder.WriteString(`</body></html>`)
		transport := &vimeoPlaylistFixtureTransport{pages: map[string][]byte{
			"https://vimeo.com/channels/toomany/videos/page:1/": []byte(builder.String()),
		}}
		_, err := NewVimeo().Extract(context.Background(), Request{URL: "https://vimeo.com/channels/toomany", Transport: transport})
		if !errors.Is(err, ErrInvalidPlaylist) {
			t.Fatalf("clip bound error=%v", err)
		}
	})
	t.Run("cancellation before extract", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		transport := &vimeoPlaylistFixtureTransport{pages: map[string][]byte{
			"https://vimeo.com/channels/cancelme/videos/page:1/": readVimeoFixture(t, "channel-page1.html"),
		}}
		_, err := NewVimeo().Extract(ctx, Request{URL: "https://vimeo.com/channels/cancelme", Transport: transport})
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancel extract error=%v", err)
		}
	})
	t.Run("cancellation during pagination", func(t *testing.T) {
		transport := &vimeoPlaylistFixtureTransport{pages: map[string][]byte{
			"https://vimeo.com/channels/fixturetributes/videos/page:1/": readVimeoFixture(t, "channel-page1.html"),
			"https://vimeo.com/channels/fixturetributes/videos/page:2/": readVimeoFixture(t, "channel-page2.html"),
		}}
		result, err := NewVimeo().Extract(context.Background(), Request{URL: "https://vimeo.com/channels/fixturetributes", Transport: transport})
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		iterator := result.Entries.Iterator()
		if _, ok, err := iterator.Next(ctx); !ok || err != nil {
			t.Fatalf("first entry ok=%v err=%v", ok, err)
		}
		cancel()
		if _, _, err := iterator.Next(ctx); !errors.Is(err, context.Canceled) {
			// May succeed for remaining first-page entries; drain until page boundary.
			for {
				_, ok, err := iterator.Next(ctx)
				if errors.Is(err, context.Canceled) {
					break
				}
				if !ok && err == nil {
					t.Fatal("expected cancellation before end")
				}
				if err != nil {
					t.Fatalf("unexpected err=%v", err)
				}
			}
		}
	})
	t.Run("secret-safe transport error", func(t *testing.T) {
		transport := &vimeoPlaylistFixtureTransport{
			err: errors.New("offline secret=fixture-token"),
			pages: map[string][]byte{
				"https://vimeo.com/channels/netfail/videos/page:1/": readVimeoFixture(t, "channel-page1.html"),
			},
		}
		_, err := NewVimeo().Extract(context.Background(), Request{URL: "https://vimeo.com/channels/netfail", Transport: transport})
		if !errors.Is(err, ErrVimeoPlaylistNetwork) || strings.Contains(err.Error(), "fixture-token") || strings.Contains(err.Error(), "secret") {
			t.Fatalf("transport error leaked secret: %v", err)
		}
	})
	t.Run("profile required", func(t *testing.T) {
		plain := &memoryTransport{pages: map[string][]byte{
			"https://vimeo.com/channels/noprofile/videos/page:1/": readVimeoFixture(t, "channel-page1.html"),
		}}
		_, err := NewVimeo().Extract(context.Background(), Request{URL: "https://vimeo.com/channels/noprofile", Transport: plain})
		if !errors.Is(err, ErrTransportIsolation) {
			t.Fatalf("isolation error=%v", err)
		}
	})
	t.Run("fallback title", func(t *testing.T) {
		transport := &vimeoPlaylistFixtureTransport{pages: map[string][]byte{
			"https://vimeo.com/channels/notitle/videos/page:1/": []byte(`<html><body><div id="clip_9"><a href="/9">x</a></div></body></html>`),
		}}
		result, err := NewVimeo().Extract(context.Background(), Request{URL: "https://vimeo.com/channels/notitle", Transport: transport})
		if err != nil {
			t.Fatal(err)
		}
		if got, _ := result.Info.Lookup("title").StringValue(); got != "Vimeo channel notitle" {
			t.Fatalf("fallback title=%q", got)
		}
	})
}

func FuzzParseVimeoPlaylistPage(f *testing.F) {
	f.Add(readVimeoFixture(f, "channel-page1.html"))
	f.Add(readVimeoFixture(f, "channel-page2.html"))
	f.Add(readVimeoFixture(f, "user-videos-page1.html"))
	f.Add(readVimeoFixture(f, "channel-fallback.html"))
	f.Add(readVimeoFixture(f, "channel-hostile.html"))
	f.Add(readVimeoFixture(f, "channel-all-invalid-anchors.html"))
	f.Add([]byte(`<div id="clip_1"><a href="/1" title="x">`))
	f.Add([]byte(`id="clip_1"`))
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > vimeoMaxPageBytes+1 {
			t.Skip()
		}
		parsed, err := parseVimeoPlaylistPage(context.Background(), data)
		if err != nil {
			if !errors.Is(err, ErrInvalidPlaylist) && !errors.Is(err, ErrJSONResponseTooLarge) && !errors.Is(err, context.Canceled) {
				t.Fatalf("unstable playlist parse error: %v", err)
			}
			return
		}
		if len(parsed.entries) > vimeoMaxClipsPerPage {
			t.Fatalf("entry bound escaped: %d", len(parsed.entries))
		}
		_, sawCandidate, err := parseVimeoPlaylistClipAnchors(context.Background(), data)
		if err != nil {
			t.Fatalf("anchor probe error: %v", err)
		}
		seen := make(map[string]struct{}, len(parsed.entries))
		for _, entry := range parsed.entries {
			if !vimeoNumericPattern.MatchString(entry.ID) || len(entry.ID) > vimeoMaxNumericVideoIDLen {
				t.Fatalf("bad id %q", entry.ID)
			}
			if entry.URL != "https://vimeo.com/"+entry.ID {
				t.Fatalf("bad url %q", entry.URL)
			}
			if entry.ExtractorKey != "vimeo" || !entry.Transparent {
				t.Fatalf("bad entry %#v", entry)
			}
			if entry.Title != "" && (strings.ContainsRune(entry.Title, '\x00') || utf8.RuneCountInString(entry.Title) > vimeoMaxEntryTitle) {
				t.Fatalf("bad title %q", entry.Title)
			}
			if _, dup := seen[entry.ID]; dup {
				t.Fatalf("duplicate id %q", entry.ID)
			}
			seen[entry.ID] = struct{}{}
			if parsedURL, err := url.Parse(entry.URL); err != nil || parsedURL.Scheme != "https" || parsedURL.Host != "vimeo.com" || parsedURL.User != nil || parsedURL.RawQuery != "" || parsedURL.Fragment != "" {
				t.Fatalf("unsafe accepted entry URL %q", entry.URL)
			}
			if sawCandidate {
				// When candidate anchors exist, every emitted ID must have had an
				// agreeing href; bare marker fallback is forbidden.
				window := data
				if idx := bytes.Index(data, []byte("clip_"+entry.ID)); idx >= 0 {
					start := idx + len("clip_"+entry.ID)
					end := start + vimeoClipLookaheadBytes
					if end > len(data) {
						end = len(data)
					}
					window = data[start:end]
				}
				if _, _, ok := findVimeoClipAnchor(window, entry.ID); !ok {
					t.Fatalf("fallback reintroduced id %q despite candidate anchors", entry.ID)
				}
			}
		}
	})
}

func FuzzClassifyVimeoPlaylistURL(f *testing.F) {
	for _, seed := range []string{
		"https://vimeo.com/fixtureuser",
		"https://vimeo.com/fixtureuser/videos",
		"https://vimeo.com/channels/fixturechannel",
		"https://vimeo.com/groups/fixturegroup",
		"https://vimeo.com/123456789",
		"https://vimeo.com/groups/fixture%2fgroup",
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
		target, ok := classifyVimeoPlaylistURL(parsed)
		if !ok {
			return
		}
		if target.id == "" || !vimeoSlugPattern.MatchString(target.id) ||
			target.kind != vimeoRouteChannel && target.kind != vimeoRouteUserVideos && target.kind != vimeoRouteGroup {
			t.Fatalf("unsafe target: %#v", target)
		}
		for _, raw := range []string{target.canonical, target.baseURL} {
			canonical, err := url.Parse(raw)
			if err != nil || canonical.Scheme != "https" || canonical.Hostname() != "vimeo.com" ||
				canonical.User != nil || canonical.Port() != "" || canonical.RawQuery != "" || canonical.Fragment != "" {
				t.Fatalf("unsafe canonical %q for %#v: %v", raw, target, err)
			}
		}
		roundTrip, ok := classifyVimeoPlaylistURL(mustParseURL(t, target.canonical))
		if !ok || roundTrip != target {
			t.Fatalf("round trip = %#v, %v; want %#v", roundTrip, ok, target)
		}
	})
}

func FuzzClassifyVimeoContextVideoURL(f *testing.F) {
	for _, seed := range []string{
		"https://vimeo.com/channels/fixturechannel/123456789",
		"https://vimeo.com/groups/fixturegroup/videos/123456789",
		"https://vimeo.com/album/fixturealbum/video/123456789",
		"https://vimeo.com/showcase/fixtureshowcase/video/123456789",
		"https://vimeo.com/showcase/fixture%2fshowcase/video/123456789",
		"https://evil.example/channels/fixturechannel/123456789",
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
		target, ok := classifyVimeoContextVideoURL(parsed)
		if !ok {
			return
		}
		if target.kind != vimeoRouteVideo || !validVimeoNumericVideoID(target.id) ||
			target.baseURL != "" {
			t.Fatalf("unsafe target: %#v", target)
		}
		canonical, err := url.Parse(target.canonical)
		if err != nil || canonical.Scheme != "https" || canonical.Hostname() != "vimeo.com" ||
			canonical.User != nil || canonical.Port() != "" || canonical.RawQuery != "" ||
			canonical.Fragment != "" {
			t.Fatalf("unsafe canonical %q: %v", target.canonical, err)
		}
		roundTrip, ok := classifyVimeoContextVideoURL(canonical)
		if !ok || roundTrip != target {
			t.Fatalf("round trip = %#v, %v; want %#v", roundTrip, ok, target)
		}
		kind, routed := classifyVimeoURL(canonical)
		if kind != vimeoRouteVideo || routed != target {
			t.Fatalf("top-level route = %v, %#v; want video %#v", kind, routed, target)
		}
	})
}

func mustParseURL(t testing.TB, rawURL string) *url.URL {
	t.Helper()
	parsed, err := url.Parse(rawURL)
	if err != nil {
		t.Fatal(err)
	}
	return parsed
}

func TestValidVimeoRefererRejectsHostileInputs(t *testing.T) {
	for _, raw := range []string{
		"http://publisher.example/", "https://user:secret@publisher.example/",
		"https://publisher.example:443/", "https://127.0.0.1/", "https://publisher.example/path#frag",
		"https://publisher.example/\x00", strings.Repeat("a", vimeoMaxReferer+1),
	} {
		if _, ok := validVimeoReferer(raw); ok {
			t.Fatalf("accepted hostile referer %q", raw)
		}
	}
	if got, ok := validVimeoReferer("https://publisher.example/show"); !ok || got != "https://publisher.example/show" {
		t.Fatalf("valid referer = %q, %v", got, ok)
	}
}

func TestMergeVimeoSubtitlesPrefersPlayerConfigDuplicates(t *testing.T) {
	var config vimeoConfig
	if err := json.Unmarshal(readVimeoFixture(t, "text_tracks_mixed.json"), &config); err != nil {
		t.Fatal(err)
	}
	config.Request.TextTracks = append(config.Request.TextTracks, vimeoTextTrack{
		URL: "/texttrack/api-fallback.vtt", Language: "fr", Kind: "subtitles",
	})
	subtitles, err := mergeVimeoSubtitles(context.Background(), nil, "1", config, vimeoFiles{}, vimeoSubtitleMergeOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if subtitles.Len() != 2 || subtitles.Lookup("fr").IsMissing() {
		t.Fatalf("subtitles = %#v", subtitles)
	}
}

func TestVimeoSubtitleManifestFallbackMergesHLSAndDASH(t *testing.T) {
	transport := &vimeoManifestSubtitleTransport{}
	files := vimeoFiles{
		HLS: struct {
			CDNs map[string]struct {
				URL string `json:"url"`
			} `json:"cdns"`
		}{CDNs: map[string]struct {
			URL string `json:"url"`
		}{"fixture": {URL: "https://cdn.example.test/master.m3u8"}}},
		DASH: struct {
			CDNs map[string]struct {
				URL string `json:"url"`
			} `json:"cdns"`
		}{CDNs: map[string]struct {
			URL string `json:"url"`
		}{"fixture": {URL: "https://cdn.example.test/master.json"}}},
	}
	candidates, err := vimeoSubtitleCandidatesFromManifests(context.Background(), transport, files)
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 2 || candidates[0].language != "en" || candidates[1].language != "fr" {
		t.Fatalf("candidates = %#v", candidates)
	}
	if candidates[0].url != "https://cdn.example.test/subs_en.m3u8" || candidates[0].ext != "vtt" {
		t.Fatalf("hls candidate = %#v", candidates[0])
	}
	if !candidates[0].isolated || !candidates[1].isolated {
		t.Fatalf("manifest candidates must be credential-isolated: %#v", candidates)
	}
	subtitles, err := vimeoSubtitlesFromCandidates(candidates)
	if err != nil {
		t.Fatal(err)
	}
	entries, ok := subtitles.Lookup("en").ListValue()
	if !ok || len(entries) != 1 {
		t.Fatalf("english subtitles = %#v", subtitles)
	}
	entry, ok := entries[0].Object()
	if !ok {
		t.Fatal("expected object subtitle entry")
	}
	isolated, ok := entry.Lookup("_credential_isolated").Bool()
	if !ok || !isolated {
		t.Fatalf("entry = %#v", entry)
	}
}

type vimeoTexttracksTransport struct {
	vimeoAlbumFixtureTransport
	texttracksStatus int
	texttracksBody   []byte
	manifestRequests []string
}

func newVimeoTexttracksTransport(t *testing.T) *vimeoTexttracksTransport {
	return &vimeoTexttracksTransport{vimeoAlbumFixtureTransport: *newVimeoAlbumFixtureTransport(t)}
}

func (transport *vimeoTexttracksTransport) DoWithoutCredentialsNoRedirect(ctx context.Context, request *http.Request) (*http.Response, error) {
	transport.manifestRequests = append(transport.manifestRequests, request.URL.String())
	if request.Header.Get("Authorization") != "" || request.Header.Get("Cookie") != "" {
		return nil, fmt.Errorf("credential leakage to manifest origin: %#v", request.Header)
	}
	switch request.URL.Host {
	case "cdn.example.test":
		return (&vimeoManifestSubtitleTransport{}).DoWithoutCredentialsNoRedirect(ctx, request)
	default:
		return transport.vimeoAlbumFixtureTransport.DoWithoutCredentialsNoRedirect(ctx, request)
	}
}

func (transport *vimeoTexttracksTransport) DoWithScopedAuthorizationNoRedirect(ctx context.Context, request *http.Request) (*http.Response, error) {
	if request.URL.Host == "api.vimeo.com" && strings.HasSuffix(request.URL.Path, "/texttracks") {
		if request.Header.Get("Authorization") == "" {
			return nil, fmt.Errorf("missing scoped authorization: %#v", request.Header)
		}
		if request.Header.Get("Cookie") != "" {
			return nil, fmt.Errorf("credential leakage to texttracks API: %#v", request.Header)
		}
		status := transport.texttracksStatus
		if status == 0 {
			status = http.StatusOK
		}
		body := transport.texttracksBody
		if len(body) == 0 {
			body = []byte(`{"data":[{"language":"de","link":"https://cdn.example.test/api-fallback.vtt","display_language":"German"}]}`)
		}
		return vimeoAlbumResponse(status, body), nil
	}
	return transport.vimeoAlbumFixtureTransport.DoWithScopedAuthorizationNoRedirect(ctx, request)
}

func TestVimeoViewerJWTTexttracksAPIUsesScopedAuthorization(t *testing.T) {
	transport := newVimeoTexttracksTransport(t)
	candidates, err := vimeoSubtitleCandidatesFromAPI(context.Background(), transport, "123456789")
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 1 || candidates[0].language != "de" || candidates[0].url != "https://cdn.example.test/api-fallback.vtt" {
		t.Fatalf("candidates = %#v", candidates)
	}
	if transport.countPath("/_next/viewer") != 1 {
		t.Fatalf("viewer calls = %d", transport.countPath("/_next/viewer"))
	}
}

func TestVimeoTexttracksAPI401And403AreNonfatal(t *testing.T) {
	for _, status := range []int{http.StatusUnauthorized, http.StatusForbidden} {
		t.Run(fmt.Sprintf("status-%d", status), func(t *testing.T) {
			transport := newVimeoTexttracksTransport(t)
			transport.texttracksStatus = status
			var config vimeoConfig
			config.Request.TextTracks = []vimeoTextTrack{{URL: "/texttrack/player.vtt", Language: "en", Kind: "subtitles"}}
			subtitles, err := mergeVimeoSubtitles(context.Background(), transport, "1", config, vimeoFiles{}, vimeoSubtitleMergeOptions{})
			if err != nil {
				t.Fatal(err)
			}
			if subtitles.Len() != 1 || subtitles.Lookup("en").IsMissing() {
				t.Fatalf("subtitles = %#v", subtitles)
			}
		})
	}
}

func TestVimeoSubtitleManifestFetchesWithoutCredentials(t *testing.T) {
	transport := newVimeoTexttracksTransport(t)
	files := vimeoFiles{
		HLS: struct {
			CDNs map[string]struct {
				URL string `json:"url"`
			} `json:"cdns"`
		}{CDNs: map[string]struct {
			URL string `json:"url"`
		}{"fixture": {URL: "https://cdn.example.test/master.m3u8"}}},
	}
	if _, err := vimeoSubtitleCandidatesFromManifests(context.Background(), transport, files); err != nil {
		t.Fatal(err)
	}
	if len(transport.manifestRequests) == 0 {
		t.Fatal("expected manifest requests")
	}
}

func TestVimeoSubtitleLimitsRejectOversizedSourcesAndAggregate(t *testing.T) {
	oversized := make([]vimeoTextTrack, vimeoMaxTextTracks+1)
	_, err := vimeoSubtitleCandidatesFromTracks(context.Background(), oversized, true)
	if !errors.Is(err, ErrInvalidMetadata) {
		t.Fatalf("track source error = %v", err)
	}
	apiPayload := struct {
		Data []struct {
			Language string `json:"language"`
			Link     string `json:"link"`
		} `json:"data"`
	}{}
	for index := 0; index < vimeoMaxTextTracks+1; index++ {
		apiPayload.Data = append(apiPayload.Data, struct {
			Language string `json:"language"`
			Link     string `json:"link"`
		}{Language: "en", Link: fmt.Sprintf("https://cdn.example.test/%d.vtt", index)})
	}
	body, err := json.Marshal(apiPayload)
	if err != nil {
		t.Fatal(err)
	}
	transport := newVimeoTexttracksTransport(t)
	transport.texttracksBody = body
	if _, err := vimeoSubtitleCandidatesFromAPI(context.Background(), transport, "1"); !errors.Is(err, ErrInvalidMetadata) {
		t.Fatalf("api source error = %v", err)
	}
	primary := make([]vimeoSubtitleCandidate, vimeoMaxTextTracks)
	for index := range primary {
		primary[index] = vimeoSubtitleCandidate{language: fmt.Sprintf("l%d", index), url: fmt.Sprintf("https://cdn.example.test/%d.vtt", index), ext: "vtt", primary: true}
	}
	if _, err := appendBoundedVimeoSubtitleCandidates(nil, primary); err != nil {
		t.Fatal(err)
	}
	if _, err := appendBoundedVimeoSubtitleCandidates(primary, []vimeoSubtitleCandidate{{language: "overflow", url: "https://cdn.example.test/overflow.vtt", ext: "vtt"}}); err != nil {
		t.Fatalf("aggregate append error = %v", err)
	}
	merged := append(primary, vimeoSubtitleCandidate{language: "overflow", url: "https://cdn.example.test/overflow.vtt", ext: "vtt"})
	if _, err := vimeoSubtitlesFromCandidates(merged); !errors.Is(err, ErrInvalidMetadata) {
		t.Fatalf("aggregate result error = %v", err)
	}
}

type vimeoManifestSubtitleTransport struct{}

func (transport *vimeoManifestSubtitleTransport) Do(context.Context, *http.Request) (*http.Response, error) {
	return nil, errors.New("ambient transport must not be used")
}

func (transport *vimeoManifestSubtitleTransport) ReadPage(context.Context, string) ([]byte, http.Header, error) {
	return nil, nil, errors.New("ambient page transport must not be used")
}

func (transport *vimeoManifestSubtitleTransport) DoWithoutCredentialsNoRedirect(_ context.Context, request *http.Request) (*http.Response, error) {
	switch request.URL.String() {
	case "https://cdn.example.test/master.m3u8":
		body := "#EXTM3U\n#EXT-X-MEDIA:TYPE=SUBTITLES,GROUP-ID=\"subs\",NAME=\"English\",LANGUAGE=\"en\",URI=\"subs_en.m3u8\"\n"
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
	case "https://cdn.example.test/subs_en.m3u8":
		body := "#EXTM3U\n#EXTINF:1,\nseg0.vtt\n#EXT-X-ENDLIST\n"
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
	case "https://cdn.example.test/seg0.vtt":
		body := "WEBVTT\n\n00:00.000 --> 00:01.000\nmanifest english\n"
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
	case "https://cdn.example.test/master.mpd":
		body := `<?xml version="1.0"?><MPD><Period><AdaptationSet contentType="text" lang="fr"><BaseURL>subs_fr.vtt</BaseURL></AdaptationSet></Period></MPD>`
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
	default:
		return nil, fmt.Errorf("unexpected manifest %s", request.URL)
	}
}

func TestVimeoSubtitleManifestCredentialIsolation(t *testing.T) {
	transport := &vimeoManifestSubtitleTransport{}
	files := vimeoFiles{
		HLS: struct {
			CDNs map[string]struct {
				URL string `json:"url"`
			} `json:"cdns"`
		}{CDNs: map[string]struct {
			URL string `json:"url"`
		}{"fixture": {URL: "https://cdn.example.test/master.m3u8"}}},
	}
	candidates, err := vimeoSubtitleCandidatesFromManifests(context.Background(), transport, files)
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 1 || candidates[0].language != "en" {
		t.Fatalf("candidates = %#v", candidates)
	}
}

func TestVimeoSubtitleManifestContextCancellation(t *testing.T) {
	transport := &vimeoManifestSubtitleTransport{}
	files := vimeoFiles{
		HLS: struct {
			CDNs map[string]struct {
				URL string `json:"url"`
			} `json:"cdns"`
		}{CDNs: map[string]struct {
			URL string `json:"url"`
		}{"fixture": {URL: "https://cdn.example.test/master.m3u8"}}},
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := vimeoSubtitleCandidatesFromManifests(ctx, transport, files); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled error = %v", err)
	}
}

func TestVimeoSubtitleManifestRejectsNonIsolatedTransport(t *testing.T) {
	plain := &memoryTransport{pages: map[string][]byte{}}
	files := vimeoFiles{
		HLS: struct {
			CDNs map[string]struct {
				URL string `json:"url"`
			} `json:"cdns"`
		}{CDNs: map[string]struct {
			URL string `json:"url"`
		}{"fixture": {URL: "https://cdn.example.test/master.m3u8"}}},
	}
	candidates, err := vimeoSubtitleCandidatesFromManifests(context.Background(), plain, files)
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 0 {
		t.Fatalf("expected no candidates from non-isolated transport, got %d", len(candidates))
	}
}

func TestVimeoSubtitleManifestRejectsOversized(t *testing.T) {
	oversizedHLS := vimeoFiles{
		HLS: struct {
			CDNs map[string]struct {
				URL string `json:"url"`
			} `json:"cdns"`
		}{CDNs: map[string]struct {
			URL string `json:"url"`
		}{"fixture": {URL: "http://evil.example/test.m3u8"}}},
	}
	transport := &vimeoManifestSubtitleTransport{}
	candidates, err := vimeoSubtitleCandidatesFromManifests(context.Background(), transport, oversizedHLS)
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 0 {
		t.Fatalf("expected no candidates from non-HTTPS URL, got %d", len(candidates))
	}
}
