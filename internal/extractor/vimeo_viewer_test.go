package extractor

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

// vimeoAuthenticatedViewerFixtureTransport is a deterministic, mutex-protected
// fixture that satisfies both vimeoAuthenticatedTransport and
// ScopedAuthorizationNoRedirectTransport. The same instance backs the cookie
// lookup + GET https://vimeo.com/_next/viewer (no explicit credentials) path
// and the api.vimeo.com JWT-scoped path. Errors raised by this fixture never
// include raw cookie, JWT, or body bytes.
type vimeoAuthenticatedViewerFixtureTransport struct {
	mu sync.Mutex

	viewerPayload []byte
	viewerStatus  int
	viewers       [][]byte

	cookieURLs     []string
	requests       []*http.Request
	cookies        []*http.Cookie
	cookieErr      error
	scopedURL      string
	scopedBody     []byte
	scopedToken    string
	scopedCalls    int
	viewerCalls    int
	scopedRequests []*http.Request

	// allowVideoAPI permits the scoped executor to serve URLs that start
	// with the api.vimeo.com/videos/ prefix even when scopedURL is set to a
	// different fixed value. Used by the authenticated unlisted video tests.
	allowVideoAPI bool

	// scopedStatuses queues the responses returned for /videos/... calls in
	// order. When empty, the fixture defaults to 200 with scopedBody.
	scopedStatuses []int
	scopedBodies   [][]byte

	// configBody is the JSON returned for credential-isolated player-config
	// requests on player.vimeo.com and empty synthetic manifests on the reserved
	// media.example.test CDN. Defaults to an empty config object.
	configBody []byte

	// credentialIsolatedCalls counts credential-isolated no-redirect calls.
	credentialIsolatedCalls int

	// isolatedRequestURLs records the request URLs that arrived on the
	// credential-isolated executor for assertion.
	isolatedRequestURLs []string
	isolatedRequests    []*http.Request

	// explicitRequests preserves the explicit headers as they arrived on each
	// viewer request, before the fixture simulated the jar attaching cookies.
	// Existing tests validate sensitive header absence on the explicit form.
	explicitRequests []*http.Request

	// sentCookies records the cookie header values that the fixture simulated
	// the jar attaching to each viewer request. Bounded to the cookie names
	// actually present so errors never reveal raw cookie bytes.
	sentCookies [][]string
}

// Cookies implements vimeoAuthenticatedTransport.Cookies. It records the
// requested origin and returns the configured jar cookies for the vimeo.com
// origin only. Asking about any other origin yields a nil slice, never the jar.
func (transport *vimeoAuthenticatedViewerFixtureTransport) Cookies(rawURL string) ([]*http.Cookie, error) {
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed == nil || parsed.Scheme == "" || parsed.Host == "" {
		return nil, fmt.Errorf("invalid cookie lookup target")
	}
	transport.mu.Lock()
	defer transport.mu.Unlock()
	transport.cookieURLs = append(transport.cookieURLs, rawURL)
	if transport.cookieErr != nil {
		return nil, transport.cookieErr
	}
	if parsed.Scheme != "https" || parsed.Host != "vimeo.com" {
		return nil, nil
	}
	cloned := make([]*http.Cookie, 0, len(transport.cookies))
	for _, cookie := range transport.cookies {
		if cookie == nil {
			continue
		}
		cloned = append(cloned, &http.Cookie{
			Name:     cookie.Name,
			Value:    cookie.Value,
			Path:     cookie.Path,
			Domain:   cookie.Domain,
			Secure:   cookie.Secure,
			HttpOnly: cookie.HttpOnly,
			SameSite: cookie.SameSite,
		})
	}
	return cloned, nil
}

// DoNoRedirect implements vimeoAuthenticatedTransport.DoNoRedirect for the
// viewer GET. The fixture simulates the operation cookie jar by attaching
// the configured `vimeo` cookie to the request, then records a cloned sent
// request plus a bounded cookie-name snapshot. The original incoming
// request carries no explicit Cookie so the explicit-header assertion
// continues to hold. Errors raised here never include raw cookie or JWT
// bytes.
func (transport *vimeoAuthenticatedViewerFixtureTransport) DoNoRedirect(ctx context.Context, request *http.Request) (*http.Response, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	transport.mu.Lock()
	defer transport.mu.Unlock()

	// Capture the explicit headers as they arrived so the explicit-header
	// assertion can verify no Cookie/Authorization/Proxy-Authorization/
	// Referer was attached by the caller before the jar ran.
	explicit := request.Clone(request.Context())
	transport.explicitRequests = append(transport.explicitRequests, explicit)

	// Simulate the operation jar attaching cookies to the vimeo.com hop. The
	// transport is responsible for translating the jar entry into a Cookie
	// header before the request goes out; the production path does this via
	// its real jar implementation. We mirror only the cookie names in the
	// snapshot so the snapshot cannot leak raw cookie values.
	jarNames := make([]string, 0, len(transport.cookies))
	if request.URL.Scheme == "https" && request.URL.Host == "vimeo.com" {
		for _, cookie := range transport.cookies {
			if cookie == nil || cookie.Name == "" {
				continue
			}
			jarNames = append(jarNames, cookie.Name)
			existing := request.Header.Values("Cookie")
			if !hasCookieName(existing, cookie.Name) {
				request.Header.Add("Cookie", cookie.Name+"=redacted")
			}
		}
	}
	transport.sentCookies = append(transport.sentCookies, jarNames)

	// The sent request is the post-jar state, proving the jar can reach the
	// viewer origin with the configured `vimeo` cookie.
	cloned := request.Clone(request.Context())
	transport.requests = append(transport.requests, cloned)
	transport.viewerCalls++

	if explicit.Method != http.MethodGet ||
		explicit.URL.Scheme != "https" ||
		explicit.URL.Host != "vimeo.com" ||
		explicit.URL.Path != "/_next/viewer" ||
		explicit.URL.RawQuery != "" {
		return nil, fmt.Errorf("unexpected authenticated viewer request")
	}
	if explicit.Header.Get("Accept") != "application/json" {
		return nil, fmt.Errorf("missing or wrong Accept on viewer request")
	}
	for _, sensitive := range []string{"Cookie", "Authorization", "Proxy-Authorization", "Referer"} {
		if raw := explicit.Header.Values(sensitive); len(raw) != 0 {
			return nil, fmt.Errorf("unexpected %s on viewer request", sensitive)
		}
	}

	body, status := transport.consumeViewerLocked()
	if status == 0 {
		status = http.StatusOK
	}
	return &http.Response{
		StatusCode: status,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(string(body))),
		Request:    request,
	}, nil
}

// hasCookieName reports whether any cookie in the raw header values begins
// with the given name followed by '='. It performs no decoding so callers
// never see raw cookie values.
func hasCookieName(rawValues []string, name string) bool {
	prefix := name + "="
	for _, raw := range rawValues {
		for _, part := range strings.Split(raw, ";") {
			trimmed := strings.TrimSpace(part)
			if strings.HasPrefix(trimmed, prefix) {
				return true
			}
		}
	}
	return false
}

func (transport *vimeoAuthenticatedViewerFixtureTransport) consumeViewerLocked() ([]byte, int) {
	if len(transport.viewers) > 0 {
		body := append([]byte(nil), transport.viewers[0]...)
		transport.viewers = transport.viewers[1:]
		return body, http.StatusOK
	}
	status := transport.viewerStatus
	if status == 0 {
		status = http.StatusOK
	}
	return append([]byte(nil), transport.viewerPayload...), status
}

// Do implements Transport but the authenticated viewer provider must never
// invoke it.
func (*vimeoAuthenticatedViewerFixtureTransport) Do(context.Context, *http.Request) (*http.Response, error) {
	return nil, errors.New("authenticated viewer must not use ambient transport")
}

// ReadPage implements Transport but the authenticated viewer provider must
// never invoke it.
func (*vimeoAuthenticatedViewerFixtureTransport) ReadPage(context.Context, string) ([]byte, http.Header, error) {
	return nil, nil, errors.New("authenticated viewer must not use ambient page transport")
}

// DoWithoutCredentialsNoRedirect implements CredentialIsolatedNoRedirectTransport
// for the player-config URL handoff. The fixture serves the configured
// configBody on player.vimeo.com and empty manifests on media.example.test;
// every other host is rejected so a regression that attempts to fetch through
// the cookie-bearing path will fail closed.
func (transport *vimeoAuthenticatedViewerFixtureTransport) DoWithoutCredentialsNoRedirect(ctx context.Context, request *http.Request) (*http.Response, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	transport.mu.Lock()
	defer transport.mu.Unlock()
	transport.credentialIsolatedCalls++
	transport.isolatedRequestURLs = append(transport.isolatedRequestURLs, request.URL.String())
	transport.isolatedRequests = append(transport.isolatedRequests, request.Clone(request.Context()))
	if request.URL.Scheme != "https" ||
		(request.URL.Host != "player.vimeo.com" && request.URL.Host != "media.example.test") {
		return nil, fmt.Errorf("unexpected credential-isolated request host")
	}
	if raw := request.Header.Values("Cookie"); len(raw) != 0 {
		return nil, fmt.Errorf("Cookie header on config call forbidden")
	}
	if raw := request.Header.Values("Proxy-Authorization"); len(raw) != 0 {
		return nil, fmt.Errorf("Proxy-Authorization on config call forbidden")
	}
	if raw := request.Header.Values("Authorization"); len(raw) != 0 {
		return nil, fmt.Errorf("Authorization on config call forbidden")
	}
	var body []byte
	switch {
	case request.URL.Host == "media.example.test" && strings.HasSuffix(request.URL.Path, ".m3u8"):
		body = []byte("#EXTM3U\n")
	case request.URL.Host == "media.example.test" && strings.HasSuffix(request.URL.Path, ".mpd"):
		body = []byte(`<MPD></MPD>`)
	default:
		body = transport.configBody
		if body == nil {
			body = []byte(`{}`)
		}
	}
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     make(http.Header),
		Body:       io.NopCloser(bytes.NewReader(body)),
		Request:    request,
	}, nil
}

// DoWithScopedAuthorizationNoRedirect implements
// ScopedAuthorizationNoRedirectTransport for api.vimeo.com calls. The fixture
// only knows how to serve one URL at a time.
func (transport *vimeoAuthenticatedViewerFixtureTransport) DoWithScopedAuthorizationNoRedirect(ctx context.Context, request *http.Request) (*http.Response, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	transport.mu.Lock()
	defer transport.mu.Unlock()
	transport.scopedCalls++
	transport.scopedRequests = append(transport.scopedRequests, request.Clone(request.Context()))
	if request.Method != http.MethodGet || request.URL.Scheme != "https" || request.URL.Host != "api.vimeo.com" {
		return nil, fmt.Errorf("unexpected scoped request")
	}
	if transport.scopedURL != "" && request.URL.String() != transport.scopedURL {
		// Allow the unlisted authenticated path to also exercise a /videos/...
		// endpoint when an explicit override is configured.
		if !transport.allowVideoAPI {
			return nil, fmt.Errorf("unexpected scoped URL")
		}
	}
	authorization := request.Header.Get("Authorization")
	if !strings.HasPrefix(authorization, "jwt ") {
		return nil, fmt.Errorf("missing scoped Authorization header")
	}
	if transport.scopedToken != "" && strings.TrimPrefix(authorization, "jwt ") != transport.scopedToken {
		return nil, fmt.Errorf("unexpected scoped token")
	}
	if raw := request.Header.Values("Cookie"); len(raw) != 0 {
		return nil, fmt.Errorf("Cookie header on API call forbidden")
	}
	if raw := request.Header.Values("Proxy-Authorization"); len(raw) != 0 {
		return nil, fmt.Errorf("Proxy-Authorization on API call forbidden")
	}
	if request.Header.Get("Accept") != "application/json" {
		return nil, fmt.Errorf("Accept must be application/json on API call")
	}
	// The unlisted authenticated path drives /videos/{id}:{hash} calls with
	// scripted status/body sequences; honor those when configured so the
	// taxonomy tests can deterministically trigger 401/403/404/5460 etc.
	if transport.allowVideoAPI && strings.HasPrefix(request.URL.Path, "/videos/") {
		if len(transport.scopedBodies) > 0 {
			body := transport.scopedBodies[0]
			transport.scopedBodies = transport.scopedBodies[1:]
			status := http.StatusOK
			if len(transport.scopedStatuses) > 0 {
				status = transport.scopedStatuses[0]
				transport.scopedStatuses = transport.scopedStatuses[1:]
			}
			return &http.Response{
				StatusCode: status,
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader(string(body))),
				Request:    request,
			}, nil
		}
	}
	body := transport.scopedBody
	if body == nil {
		body = []byte(`{}`)
	}
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(string(body))),
		Request:    request,
	}, nil
}

func newVimeoAuthenticatedViewerFixtureTransport(cookies []*http.Cookie) *vimeoAuthenticatedViewerFixtureTransport {
	cloned := make([]*http.Cookie, 0, len(cookies))
	for _, cookie := range cookies {
		if cookie == nil {
			continue
		}
		cloned = append(cloned, &http.Cookie{
			Name:     cookie.Name,
			Value:    cookie.Value,
			Path:     cookie.Path,
			Domain:   cookie.Domain,
			Secure:   cookie.Secure,
			HttpOnly: cookie.HttpOnly,
			SameSite: cookie.SameSite,
		})
	}
	return &vimeoAuthenticatedViewerFixtureTransport{
		cookies:    cloned,
		scopedURL:  "https://api.vimeo.com/me",
		scopedBody: []byte(`{}`),
	}
}

// vimeoAuthenticatedViewerTestJWT constructs a deterministic JWT with the
// given expiry and a fixed high-entropy signature used only for fixture
// assertions. Real credentials are never embedded.
func vimeoAuthenticatedViewerTestJWT(exp int64, signature string) string {
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"HS256"}`))
	payload := base64.RawURLEncoding.EncodeToString([]byte(fmt.Sprintf(`{"exp":%d}`, exp)))
	return header + "." + payload + "." + base64.RawURLEncoding.EncodeToString([]byte(signature))
}

// vimeoViewerFixtureSecret is a high-entropy marker that must never appear in
// any error string emitted by the viewer provider.
const vimeoViewerFixtureSecret = "VimeoViewerSecret-SHARED-MARKER-12345-ABCDE"

// Compile-time assertions: the fixture honors both interfaces required by the
// provider, plus the operation transport it must replace.
var (
	_ vimeoAuthenticatedTransport            = (*vimeoAuthenticatedViewerFixtureTransport)(nil)
	_ ScopedAuthorizationNoRedirectTransport = (*vimeoAuthenticatedViewerFixtureTransport)(nil)
	_ Transport                              = (*vimeoAuthenticatedViewerFixtureTransport)(nil)
	_ CredentialIsolatedNoRedirectTransport  = (*vimeoAuthenticatedViewerFixtureTransport)(nil)
)

func TestVimeoAuthenticatedViewerConstructorRejectsMissingCapabilities(t *testing.T) {
	if _, err := vimeoAuthenticatedViewerTokenProviderFromTransport(nil, time.Now); err == nil {
		t.Fatal("nil transport accepted")
	} else if !errors.Is(err, ErrAuthentication) {
		t.Fatalf("nil transport error = %v, want ErrAuthentication", err)
	}
	plainTransport := &struct{ Transport }{}
	if _, err := vimeoAuthenticatedViewerTokenProviderFromTransport(plainTransport, time.Now); err == nil {
		t.Fatal("transport without cookies/no-redirect capability accepted")
	} else if !errors.Is(err, ErrAuthentication) {
		t.Fatalf("capability error = %v, want ErrAuthentication", err)
	}
}

func TestVimeoAuthenticatedViewerFetchSuccessCacheAndHeaders(t *testing.T) {
	base := time.Now().Truncate(time.Second)
	token := vimeoAuthenticatedViewerTestJWT(base.Add(time.Hour).Unix(), "first-viewer")
	cookies := []*http.Cookie{{Name: "vimeo", Value: "session-token-A"}}
	transport := newVimeoAuthenticatedViewerFixtureTransport(cookies)
	transport.viewerPayload = []byte(`{"jwt":"` + token + `"}`)
	provider, err := vimeoAuthenticatedViewerTokenProviderFromTransport(transport, func() time.Time { return base })
	if err != nil {
		t.Fatalf("constructor error = %v", err)
	}
	calls := 0
	err = withVimeoAuthenticatedViewerToken(context.Background(), provider, func(received string) error {
		calls++
		if received != token {
			t.Fatalf("token = %q, want %q", received, token)
		}
		return nil
	})
	if err != nil || calls != 1 {
		t.Fatalf("first callback error=%v calls=%d", err, calls)
	}
	if got := transport.viewerCalls; got != 1 {
		t.Fatalf("viewer calls = %d, want 1", got)
	}
	if got := len(transport.cookieURLs); got != 1 || transport.cookieURLs[0] != "https://vimeo.com" {
		t.Fatalf("cookie URLs = %v", transport.cookieURLs)
	}
	viewerReq := transport.requests[0]
	if viewerReq.Header.Get("Accept") != "application/json" {
		t.Fatalf("Accept = %q", viewerReq.Header.Get("Accept"))
	}
	// The explicit (incoming) request must not carry Cookie/Authorization/
	// Proxy-Authorization. The fixture simulates the jar attaching the
	// configured `vimeo` cookie to the post-jar send recorded above.
	explicitReq := transport.explicitRequests[0]
	for _, sensitive := range []string{"Cookie", "Authorization", "Proxy-Authorization", "Referer"} {
		if raw := explicitReq.Header.Values(sensitive); len(raw) != 0 {
			t.Fatalf("viewer %s leaked: %v", sensitive, raw)
		}
	}
	// The simulated send must carry the configured `vimeo` cookie.
	cookieHeader := viewerReq.Header.Values("Cookie")
	foundVimeoCookie := false
	for _, raw := range cookieHeader {
		for _, part := range strings.Split(raw, ";") {
			if strings.HasPrefix(strings.TrimSpace(part), "vimeo=") {
				foundVimeoCookie = true
			}
		}
	}
	if !foundVimeoCookie {
		t.Fatalf("vimeo cookie not present on simulated viewer send: %v", cookieHeader)
	}

	// Second call must hit the cache; no second viewer fetch.
	calls = 0
	err = withVimeoAuthenticatedViewerToken(context.Background(), provider, func(received string) error {
		calls++
		if received != token {
			t.Fatalf("cache token = %q", received)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("cache hit error = %v", err)
	}
	if calls != 1 {
		t.Fatalf("cache call count = %d, want 1", calls)
	}
	if got := transport.viewerCalls; got != 1 {
		t.Fatalf("cache hit made extra fetch: calls=%d", got)
	}
}

func TestVimeoAuthenticatedViewerFetchRejectsInvalidCookieTable(t *testing.T) {
	base := time.Now().Truncate(time.Second)
	token := vimeoAuthenticatedViewerTestJWT(base.Add(time.Hour).Unix(), "cookie-table")
	overlong := strings.Repeat("a", vimeoAuthenticatedViewerMaxCookieBytes+1)

	cases := map[string][]*http.Cookie{
		"missing":        nil,
		"unrelated only": {{Name: "session", Value: "irrelevant"}, {Name: "tracking", Value: "ignore"}},
		"empty value":    {{Name: "vimeo", Value: ""}},
		"overlong":       {{Name: "vimeo", Value: overlong}},
		"CR":             {{Name: "vimeo", Value: "ok\rbad"}},
		"LF":             {{Name: "vimeo", Value: "ok\nbad"}},
		"NUL":            {{Name: "vimeo", Value: "ok" + "\x00" + "bad"}},
	}
	for name, cookies := range cases {
		t.Run(name, func(t *testing.T) {
			transport := newVimeoAuthenticatedViewerFixtureTransport(cookies)
			transport.viewerPayload = []byte(`{"jwt":"` + token + `"}`)
			provider, err := vimeoAuthenticatedViewerTokenProviderFromTransport(transport, func() time.Time { return base })
			if err != nil {
				t.Fatalf("constructor error = %v", err)
			}
			calls := 0
			err = withVimeoAuthenticatedViewerToken(context.Background(), provider, func(string) error {
				calls++
				return nil
			})
			if !errors.Is(err, ErrAuthentication) {
				t.Fatalf("error = %v, want ErrAuthentication", err)
			}
			if calls != 0 {
				t.Fatalf("callback invoked despite invalid cookies: calls=%d", calls)
			}
			if transport.viewerCalls != 0 {
				t.Fatalf("viewer network call made despite invalid cookies: %d", transport.viewerCalls)
			}
		})
	}
}

func TestVimeoAuthenticatedViewerFetchDuplicateOrderCookieTable(t *testing.T) {
	base := time.Now().Truncate(time.Second)
	token := vimeoAuthenticatedViewerTestJWT(base.Add(time.Hour).Unix(), "cookie-order")
	overlong := strings.Repeat("a", vimeoAuthenticatedViewerMaxCookieBytes+1)

	t.Run("unrelated then valid vimeo succeeds", func(t *testing.T) {
		transport := newVimeoAuthenticatedViewerFixtureTransport([]*http.Cookie{
			{Name: "session", Value: "unrelated"},
			{Name: "tracking", Value: "ignore"},
			{Name: "vimeo", Value: "session-cookie-A"},
		})
		transport.viewerPayload = []byte(`{"jwt":"` + token + `"}`)
		provider, err := vimeoAuthenticatedViewerTokenProviderFromTransport(transport, func() time.Time { return base })
		if err != nil {
			t.Fatalf("constructor error = %v", err)
		}
		calls := 0
		err = withVimeoAuthenticatedViewerToken(context.Background(), provider, func(received string) error {
			calls++
			if received != token {
				t.Fatalf("token = %q, want %q", received, token)
			}
			return nil
		})
		if err != nil {
			t.Fatalf("orchestration error = %v", err)
		}
		if calls != 1 {
			t.Fatalf("callback calls = %d, want 1", calls)
		}
		if transport.viewerCalls != 1 {
			t.Fatalf("viewer network calls = %d, want 1", transport.viewerCalls)
		}
	})

	t.Run("first vimeo invalid then valid vimeo fails before network", func(t *testing.T) {
		cases := map[string][]*http.Cookie{
			"empty then valid": {
				{Name: "vimeo", Value: ""},
				{Name: "vimeo", Value: "session-cookie-A"},
			},
			"overlong then valid": {
				{Name: "vimeo", Value: overlong},
				{Name: "vimeo", Value: "session-cookie-A"},
			},
			"CR then valid": {
				{Name: "vimeo", Value: "ok\rbad"},
				{Name: "vimeo", Value: "session-cookie-A"},
			},
			"LF then valid": {
				{Name: "vimeo", Value: "ok\nbad"},
				{Name: "vimeo", Value: "session-cookie-A"},
			},
			"NUL then valid": {
				{Name: "vimeo", Value: "ok" + "\x00" + "bad"},
				{Name: "vimeo", Value: "session-cookie-A"},
			},
		}
		for name, cookies := range cases {
			t.Run(name, func(t *testing.T) {
				transport := newVimeoAuthenticatedViewerFixtureTransport(cookies)
				transport.viewerPayload = []byte(`{"jwt":"` + token + `"}`)
				provider, err := vimeoAuthenticatedViewerTokenProviderFromTransport(transport, func() time.Time { return base })
				if err != nil {
					t.Fatalf("constructor error = %v", err)
				}
				calls := 0
				err = withVimeoAuthenticatedViewerToken(context.Background(), provider, func(string) error {
					calls++
					return nil
				})
				if !errors.Is(err, ErrAuthentication) {
					t.Fatalf("error = %v, want ErrAuthentication", err)
				}
				if calls != 0 {
					t.Fatalf("callback invoked despite first invalid vimeo cookie: calls=%d", calls)
				}
				if transport.viewerCalls != 0 {
					t.Fatalf("viewer network call made despite first invalid vimeo cookie: %d", transport.viewerCalls)
				}
			})
		}
	})
}

func TestVimeoAuthenticatedViewerOriginIsolation(t *testing.T) {
	base := time.Now().Truncate(time.Second)
	viewerToken := vimeoAuthenticatedViewerTestJWT(base.Add(time.Hour).Unix(), "viewer-origin")

	transport := newVimeoAuthenticatedViewerFixtureTransport([]*http.Cookie{
		{Name: "vimeo", Value: "session-cookie-A"},
		{Name: "session", Value: "unrelated"},
	})
	transport.viewerPayload = []byte(`{"jwt":"` + viewerToken + `"}`)
	transport.scopedURL = "https://api.vimeo.com/albums/9/videos"
	transport.scopedBody = []byte(`{}`)
	// The API Authorization must be the JWT returned by the provider. Using a
	// separately generated apiToken would not prove the provider's output is
	// what reaches api.vimeo.com.
	transport.scopedToken = viewerToken

	provider, err := vimeoAuthenticatedViewerTokenProviderFromTransport(transport, func() time.Time { return base })
	if err != nil {
		t.Fatalf("constructor error = %v", err)
	}

	err = withVimeoAuthenticatedViewerToken(context.Background(), provider, func(token string) error {
		if token != viewerToken {
			t.Fatalf("viewer token = %q, want %q", token, viewerToken)
		}
		request, requestErr := http.NewRequestWithContext(context.Background(),
			http.MethodGet, "https://api.vimeo.com/albums/9/videos", nil)
		if requestErr != nil {
			return requestErr
		}
		request.Header = http.Header{
			"Accept":        {"application/json"},
			"Authorization": {"jwt " + token},
		}
		_, scopedErr := transport.DoWithScopedAuthorizationNoRedirect(context.Background(), request)
		return scopedErr
	})
	if err != nil {
		t.Fatalf("orchestration error = %v", err)
	}

	if transport.viewerCalls != 1 {
		t.Fatalf("viewer calls = %d, want 1", transport.viewerCalls)
	}
	if transport.scopedCalls != 1 {
		t.Fatalf("scoped calls = %d, want 1", transport.scopedCalls)
	}
	if len(transport.cookieURLs) != 1 || transport.cookieURLs[0] != "https://vimeo.com" {
		t.Fatalf("cookie lookup URLs = %v", transport.cookieURLs)
	}
	viewerRequest := transport.requests[0]
	if viewerRequest.URL.Scheme != "https" ||
		viewerRequest.URL.Host != "vimeo.com" ||
		viewerRequest.URL.Path != "/_next/viewer" {
		t.Fatalf("viewer request URL = %s", viewerRequest.URL)
	}
	if got := viewerRequest.URL.String(); got != "https://vimeo.com/_next/viewer" {
		t.Fatalf("viewer request URL = %s, want exact https://vimeo.com/_next/viewer", got)
	}
	// The explicit (incoming) request must not carry Authorization/Cookie/
	// Proxy-Authorization/Referer. The post-jar send is allowed to carry
	// the `vimeo` cookie because the fixture simulated the jar attachment.
	explicitReq := transport.explicitRequests[0]
	for _, sensitive := range []string{"Authorization", "Cookie", "Proxy-Authorization", "Referer"} {
		if raw := explicitReq.Header.Values(sensitive); len(raw) != 0 {
			t.Fatalf("%s leaked on viewer request: %v", sensitive, raw)
		}
	}
	// The fixture must prove the jar's `vimeo` cookie reached the viewer
	// origin via the simulated send. The snapshot is bounded to cookie names
	// so it cannot leak raw cookie values.
	if len(transport.sentCookies) != 1 {
		t.Fatalf("sent cookie snapshots = %d, want 1", len(transport.sentCookies))
	}
	seenVimeo := false
	for _, name := range transport.sentCookies[0] {
		if name == "vimeo" {
			seenVimeo = true
		}
	}
	if !seenVimeo {
		t.Fatalf("configured vimeo cookie not delivered to viewer origin: %v", transport.sentCookies[0])
	}
	// The cloned sent request must show the `vimeo` cookie actually
	// attached after the jar ran, while the explicit-header assertion
	// (verified above) confirms no Cookie was present on the inbound call.
	cookieHeader := viewerRequest.Header.Values("Cookie")
	foundVimeoCookie := false
	for _, raw := range cookieHeader {
		for _, part := range strings.Split(raw, ";") {
			if strings.HasPrefix(strings.TrimSpace(part), "vimeo=") {
				foundVimeoCookie = true
			}
		}
	}
	if !foundVimeoCookie {
		t.Fatalf("vimeo cookie not present on simulated viewer send: %v", cookieHeader)
	}
}

func TestVimeoAuthenticatedViewerCallbackRefreshOn401And403(t *testing.T) {
	base := time.Now().Truncate(time.Second)
	firstToken := vimeoAuthenticatedViewerTestJWT(base.Add(time.Hour).Unix(), "refresh-401")
	secondToken := vimeoAuthenticatedViewerTestJWT(base.Add(time.Hour).Unix(), "refresh-403")

	// The viewer itself returns 200 with a valid JWT; the callback then
	// surfaces an HTTPStatusError that the orchestrator must catch and refresh.
	t.Run("401 then second token", func(t *testing.T) {
		transport := newVimeoAuthenticatedViewerFixtureTransport([]*http.Cookie{{Name: "vimeo", Value: "session"}})
		transport.viewers = [][]byte{
			[]byte(`{"jwt":"` + firstToken + `"}`),
			[]byte(`{"jwt":"` + secondToken + `"}`),
		}
		provider, err := vimeoAuthenticatedViewerTokenProviderFromTransport(transport, func() time.Time { return base })
		if err != nil {
			t.Fatalf("constructor error = %v", err)
		}
		attempts := 0
		err = withVimeoAuthenticatedViewerToken(context.Background(), provider, func(token string) error {
			attempts++
			switch attempts {
			case 1:
				if token != firstToken {
					t.Fatalf("first token = %q", token)
				}
				return &HTTPStatusError{Code: http.StatusUnauthorized}
			case 2:
				if token != secondToken {
					t.Fatalf("second token = %q", token)
				}
				return nil
			default:
				t.Fatalf("unexpected attempt %d", attempts)
				return nil
			}
		})
		if err != nil || attempts != 2 {
			t.Fatalf("error=%v attempts=%d", err, attempts)
		}
		if transport.viewerCalls != 2 {
			t.Fatalf("viewer calls = %d, want 2", transport.viewerCalls)
		}
	})

	t.Run("403 then second 401 maps to authentication without third attempt", func(t *testing.T) {
		transport := newVimeoAuthenticatedViewerFixtureTransport([]*http.Cookie{{Name: "vimeo", Value: "session"}})
		transport.viewers = [][]byte{
			[]byte(`{"jwt":"` + firstToken + `"}`),
			[]byte(`{"jwt":"` + secondToken + `"}`),
		}
		provider, err := vimeoAuthenticatedViewerTokenProviderFromTransport(transport, func() time.Time { return base })
		if err != nil {
			t.Fatalf("constructor error = %v", err)
		}
		attempts := 0
		err = withVimeoAuthenticatedViewerToken(context.Background(), provider, func(token string) error {
			attempts++
			switch attempts {
			case 1:
				if token != firstToken {
					t.Fatalf("first token = %q", token)
				}
				return &HTTPStatusError{Code: http.StatusForbidden}
			case 2:
				if token != secondToken {
					t.Fatalf("second token = %q", token)
				}
				return &HTTPStatusError{Code: http.StatusUnauthorized}
			default:
				t.Fatalf("unexpected attempt %d", attempts)
				return nil
			}
		})
		if !errors.Is(err, ErrAuthentication) {
			t.Fatalf("error = %v, want ErrAuthentication", err)
		}
		if attempts != 2 || transport.viewerCalls != 2 {
			t.Fatalf("attempts=%d viewer calls=%d, want 2 both", attempts, transport.viewerCalls)
		}
	})
}

func TestVimeoAuthenticatedViewerStatusAndMetadataTaxonomy(t *testing.T) {
	base := time.Now().Truncate(time.Second)
	goodToken := vimeoAuthenticatedViewerTestJWT(base.Add(time.Hour).Unix(), "taxonomy-good")
	overlongToken := strings.Repeat("a", vimeoAuthenticatedViewerMaxJWTBytes+1)

	cases := []struct {
		name        string
		status      int
		payload     []byte
		wantErr     error
		wantPhrase  string
		cookieError bool
	}{
		{name: "302", status: http.StatusFound, wantErr: ErrAuthentication},
		{name: "401", status: http.StatusUnauthorized, wantErr: ErrAuthentication},
		{name: "403", status: http.StatusForbidden, wantErr: ErrAuthentication},
		{name: "404", status: http.StatusNotFound, wantErr: ErrUnavailable},
		{name: "410", status: http.StatusGone, wantErr: ErrUnavailable},
		{
			name:    "500",
			status:  http.StatusInternalServerError,
			wantErr: &HTTPStatusError{Code: http.StatusInternalServerError},
		},
		{
			name:       "malformed JSON",
			payload:    []byte(`{"jwt":`),
			wantErr:    ErrInvalidMetadata,
			wantPhrase: "malformed Vimeo authenticated viewer response",
		},
		{
			name:       "trailing JSON",
			payload:    []byte(`{"jwt":"` + goodToken + `"} extra`),
			wantErr:    ErrInvalidMetadata,
			wantPhrase: "malformed Vimeo authenticated viewer response",
		},
		{
			name:       "overlong JWT",
			payload:    []byte(`{"jwt":"` + overlongToken + `"}`),
			wantErr:    ErrInvalidMetadata,
			wantPhrase: "malformed Vimeo authenticated viewer token",
		},
		{
			name:       "missing JWT field",
			payload:    []byte(`{"other":"v"}`),
			wantErr:    ErrInvalidMetadata,
			wantPhrase: "malformed Vimeo authenticated viewer token",
		},
		{
			name:       "expiring JWT",
			payload:    []byte(`{"jwt":"` + vimeoAuthenticatedViewerTestJWT(base.Add(30*time.Second).Unix(), "near-exp") + `"}`),
			wantErr:    ErrAuthentication,
			wantPhrase: "",
		},
		{
			name:       "oversized JSON body",
			payload:    bytes.Repeat([]byte("x"), int(maxExtractorJSONBytes)+1),
			wantErr:    ErrInvalidMetadata,
			wantPhrase: "malformed Vimeo authenticated viewer response",
		},
		{
			name:        "cookie lookup error",
			cookieError: true,
			wantErr:     ErrAuthentication,
		},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			cookies := []*http.Cookie{{Name: "vimeo", Value: vimeoViewerFixtureSecret + "-cookie"}}
			transport := newVimeoAuthenticatedViewerFixtureTransport(cookies)
			transport.viewerStatus = test.status
			if test.payload != nil {
				transport.viewerPayload = test.payload
			} else {
				transport.viewerPayload = []byte(`{"jwt":"` + goodToken + `"}`)
			}
			if test.cookieError {
				transport.cookieErr = fmt.Errorf("jar blew up: %s", vimeoViewerFixtureSecret)
			}
			provider, err := vimeoAuthenticatedViewerTokenProviderFromTransport(transport, func() time.Time { return base })
			if err != nil {
				t.Fatalf("constructor error = %v", err)
			}
			calls := 0
			err = withVimeoAuthenticatedViewerToken(context.Background(), provider, func(token string) error {
				calls++
				if token == "" {
					t.Fatalf("callback got empty token")
				}
				return nil
			})
			if test.wantErr == nil {
				t.Fatalf("test %q: wantErr must be set", test.name)
			}
			if target, ok := test.wantErr.(*HTTPStatusError); ok {
				var got *HTTPStatusError
				if !errors.As(err, &got) || got.Code != target.Code {
					t.Fatalf("error = %v, want HTTP status %d", err, target.Code)
				}
			} else if !errors.Is(err, test.wantErr) {
				t.Fatalf("error = %v, want %v", err, test.wantErr)
			}
			for _, marker := range []string{goodToken, vimeoViewerFixtureSecret} {
				if strings.Contains(err.Error(), marker) {
					t.Fatalf("error leaked %q: %q", marker, err)
				}
			}
		})
	}
}

func TestVimeoAuthenticatedViewerFixtureDoesNotLeakCredentialsInErrors(t *testing.T) {
	base := time.Now().Truncate(time.Second)
	secureCookie := "secretCookie-AAAAA-ZZZZZ-987654"
	tokenSig := "secretSignature-BBBB-CCCC-555555"
	token := vimeoAuthenticatedViewerTestJWT(base.Add(time.Hour).Unix(), tokenSig)

	t.Run("success path", func(t *testing.T) {
		transport := newVimeoAuthenticatedViewerFixtureTransport([]*http.Cookie{{Name: "vimeo", Value: secureCookie}})
		transport.viewerPayload = []byte(`{"jwt":"` + token + `"}`)
		provider, err := vimeoAuthenticatedViewerTokenProviderFromTransport(transport, func() time.Time { return base })
		if err != nil {
			t.Fatalf("constructor error = %v", err)
		}
		err = withVimeoAuthenticatedViewerToken(context.Background(), provider, func(string) error { return nil })
		if err != nil {
			t.Fatalf("success error = %v", err)
		}
	})

	t.Run("cookie error hides cookie value", func(t *testing.T) {
		transport := newVimeoAuthenticatedViewerFixtureTransport([]*http.Cookie{{Name: "vimeo", Value: secureCookie}})
		transport.cookieErr = fmt.Errorf("jar blew up: %s", secureCookie)
		transport.viewerPayload = []byte(`{"jwt":"` + token + `"}`)
		provider, err := vimeoAuthenticatedViewerTokenProviderFromTransport(transport, func() time.Time { return base })
		if err != nil {
			t.Fatalf("constructor error = %v", err)
		}
		err = withVimeoAuthenticatedViewerToken(context.Background(), provider, func(string) error { return nil })
		if !errors.Is(err, ErrAuthentication) {
			t.Fatalf("cookie error = %v, want ErrAuthentication", err)
		}
		if strings.Contains(err.Error(), secureCookie) {
			t.Fatalf("error leaked cookie value: %q", err)
		}
	})

	t.Run("malformed response hides JWT", func(t *testing.T) {
		transport := newVimeoAuthenticatedViewerFixtureTransport([]*http.Cookie{{Name: "vimeo", Value: "ok"}})
		transport.viewerPayload = []byte(`{"jwt":"` + token + `"} extra`)
		provider, err := vimeoAuthenticatedViewerTokenProviderFromTransport(transport, func() time.Time { return base })
		if err != nil {
			t.Fatalf("constructor error = %v", err)
		}
		err = withVimeoAuthenticatedViewerToken(context.Background(), provider, func(string) error { return nil })
		if !errors.Is(err, ErrInvalidMetadata) {
			t.Fatalf("malformed error = %v, want ErrInvalidMetadata", err)
		}
		for _, marker := range []string{token, tokenSig, vimeoViewerFixtureSecret} {
			if strings.Contains(err.Error(), marker) {
				t.Fatalf("error leaked %q: %q", marker, err)
			}
		}
	})
}

type blockingVimeoAuthenticatedViewerTransport struct {
	*vimeoAuthenticatedViewerFixtureTransport
	started chan struct{}
	once    sync.Once
}

func (transport *blockingVimeoAuthenticatedViewerTransport) DoNoRedirect(ctx context.Context, _ *http.Request) (*http.Response, error) {
	transport.mu.Lock()
	transport.viewerCalls++
	transport.mu.Unlock()
	transport.once.Do(func() { close(transport.started) })
	<-ctx.Done()
	return nil, ctx.Err()
}

func TestVimeoAuthenticatedViewerExpiryBoundariesAndNilClock(t *testing.T) {
	base := time.Unix(1_800_000_000, 0)
	for _, test := range []struct {
		name      string
		offset    time.Duration
		wantError bool
	}{
		{name: "expired", offset: -time.Second, wantError: true},
		{name: "inside refresh lead", offset: 119 * time.Second, wantError: true},
		{name: "exact refresh lead", offset: 120 * time.Second},
	} {
		t.Run(test.name, func(t *testing.T) {
			token := vimeoAuthenticatedViewerTestJWT(base.Add(test.offset).Unix(), test.name)
			transport := newVimeoAuthenticatedViewerFixtureTransport([]*http.Cookie{{Name: "vimeo", Value: "session"}})
			transport.viewerPayload = []byte(`{"jwt":"` + token + `"}`)
			provider, err := vimeoAuthenticatedViewerTokenProviderFromTransport(transport, func() time.Time { return base })
			if err != nil {
				t.Fatal(err)
			}
			got, err := provider.get(context.Background())
			if test.wantError {
				if !errors.Is(err, ErrAuthentication) || got != "" {
					t.Fatalf("get = %q, %v; want ErrAuthentication", got, err)
				}
				if provider.token != "" || provider.expires != 0 {
					t.Fatalf("rejected token was cached: token=%q expires=%d", provider.token, provider.expires)
				}
				return
			}
			if err != nil || got != token {
				t.Fatalf("get = %q, %v; want %q", got, err, token)
			}
			if _, err := provider.get(context.Background()); err != nil || transport.viewerCalls != 1 {
				t.Fatalf("boundary token was not cached: calls=%d err=%v", transport.viewerCalls, err)
			}
		})
	}

	t.Run("nil clock falls back to wall clock", func(t *testing.T) {
		token := vimeoAuthenticatedViewerTestJWT(time.Now().Add(24*time.Hour).Unix(), "nil-clock")
		transport := newVimeoAuthenticatedViewerFixtureTransport([]*http.Cookie{{Name: "vimeo", Value: "session"}})
		transport.viewerPayload = []byte(`{"jwt":"` + token + `"}`)
		provider, err := vimeoAuthenticatedViewerTokenProviderFromTransport(transport, nil)
		if err != nil {
			t.Fatal(err)
		}
		if got, err := provider.get(context.Background()); err != nil || got != token {
			t.Fatalf("get = %q, %v; want far-future token", got, err)
		}
	})
}

func TestVimeoAuthenticatedViewerCancellation(t *testing.T) {
	base := time.Unix(1_800_000_000, 0)
	token := vimeoAuthenticatedViewerTestJWT(base.Add(time.Hour).Unix(), "cancellation")

	t.Run("before cookie access", func(t *testing.T) {
		transport := newVimeoAuthenticatedViewerFixtureTransport([]*http.Cookie{{Name: "vimeo", Value: "session"}})
		transport.viewerPayload = []byte(`{"jwt":"` + token + `"}`)
		provider, err := vimeoAuthenticatedViewerTokenProviderFromTransport(transport, func() time.Time { return base })
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if _, err := provider.get(ctx); !errors.Is(err, context.Canceled) {
			t.Fatalf("error = %v, want context.Canceled", err)
		}
		if len(transport.cookieURLs) != 0 || transport.viewerCalls != 0 {
			t.Fatalf("canceled call accessed transport: cookies=%d viewer=%d", len(transport.cookieURLs), transport.viewerCalls)
		}
	})

	t.Run("cookie lookup returns cancellation", func(t *testing.T) {
		transport := newVimeoAuthenticatedViewerFixtureTransport([]*http.Cookie{{Name: "vimeo", Value: "session"}})
		transport.cookieErr = context.Canceled
		provider, err := vimeoAuthenticatedViewerTokenProviderFromTransport(transport, func() time.Time { return base })
		if err != nil {
			t.Fatal(err)
		}
		if _, err := provider.get(context.Background()); !errors.Is(err, context.Canceled) {
			t.Fatalf("error = %v, want context.Canceled", err)
		}
		if transport.viewerCalls != 0 {
			t.Fatalf("cookie cancellation reached viewer transport: calls=%d", transport.viewerCalls)
		}
	})

	t.Run("during viewer fetch", func(t *testing.T) {
		fixture := newVimeoAuthenticatedViewerFixtureTransport([]*http.Cookie{{Name: "vimeo", Value: "session"}})
		transport := &blockingVimeoAuthenticatedViewerTransport{
			vimeoAuthenticatedViewerFixtureTransport: fixture,
			started:                                  make(chan struct{}),
		}
		provider, err := vimeoAuthenticatedViewerTokenProviderFromTransport(transport, func() time.Time { return base })
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() {
			_, getErr := provider.get(ctx)
			done <- getErr
		}()
		<-transport.started
		cancel()
		if err := <-done; !errors.Is(err, context.Canceled) {
			t.Fatalf("error = %v, want context.Canceled", err)
		}
		if provider.token != "" || provider.expires != 0 {
			t.Fatalf("canceled fetch installed token: token=%q expires=%d", provider.token, provider.expires)
		}
	})

	t.Run("while waiting for provider mutex", func(t *testing.T) {
		transport := newVimeoAuthenticatedViewerFixtureTransport([]*http.Cookie{{Name: "vimeo", Value: "session"}})
		transport.viewerPayload = []byte(`{"jwt":"` + token + `"}`)
		provider, err := vimeoAuthenticatedViewerTokenProviderFromTransport(transport, func() time.Time { return base })
		if err != nil {
			t.Fatal(err)
		}
		provider.token, provider.expires = token, base.Add(time.Hour).Unix()
		provider.mu.Lock()
		ctx, cancel := context.WithCancel(context.Background())
		started := make(chan struct{})
		done := make(chan error, 1)
		go func() {
			close(started)
			_, getErr := provider.get(ctx)
			done <- getErr
		}()
		<-started
		cancel()
		provider.mu.Unlock()
		if err := <-done; !errors.Is(err, context.Canceled) {
			t.Fatalf("error = %v, want context.Canceled", err)
		}
		if len(transport.cookieURLs) != 0 || transport.viewerCalls != 0 {
			t.Fatalf("mutex-wait cancellation accessed transport: cookies=%d viewer=%d", len(transport.cookieURLs), transport.viewerCalls)
		}
	})
}

func TestVimeoAuthenticatedViewerConcurrentCache(t *testing.T) {
	base := time.Unix(1_800_000_000, 0)
	token := vimeoAuthenticatedViewerTestJWT(base.Add(time.Hour).Unix(), "concurrent-cache")
	transport := newVimeoAuthenticatedViewerFixtureTransport([]*http.Cookie{{Name: "vimeo", Value: "session"}})
	transport.viewerPayload = []byte(`{"jwt":"` + token + `"}`)
	provider, err := vimeoAuthenticatedViewerTokenProviderFromTransport(transport, func() time.Time { return base })
	if err != nil {
		t.Fatal(err)
	}

	const callers = 32
	start := make(chan struct{})
	results := make(chan string, callers)
	errorsCh := make(chan error, callers)
	var group sync.WaitGroup
	group.Add(callers)
	for range callers {
		go func() {
			defer group.Done()
			<-start
			got, getErr := provider.get(context.Background())
			results <- got
			errorsCh <- getErr
		}()
	}
	close(start)
	group.Wait()
	close(results)
	close(errorsCh)
	for err := range errorsCh {
		if err != nil {
			t.Fatalf("concurrent get error = %v", err)
		}
	}
	for got := range results {
		if got != token {
			t.Fatalf("concurrent token = %q, want %q", got, token)
		}
	}
	transport.mu.Lock()
	cookieCalls, viewerCalls := len(transport.cookieURLs), transport.viewerCalls
	transport.mu.Unlock()
	if cookieCalls != 1 || viewerCalls != 1 {
		t.Fatalf("transport calls = cookies:%d viewer:%d, want 1 each", cookieCalls, viewerCalls)
	}
}

func TestVimeoAuthenticatedViewerNilAndInvalidate(t *testing.T) {
	if err := withVimeoAuthenticatedViewerToken(context.Background(), nil, func(string) error { return nil }); !errors.Is(err, ErrInvalidMetadata) {
		t.Fatalf("nil provider error = %v, want ErrInvalidMetadata", err)
	}
	provider := &vimeoAuthenticatedViewerTokenProvider{}
	if err := withVimeoAuthenticatedViewerToken(context.Background(), provider, nil); !errors.Is(err, ErrInvalidMetadata) {
		t.Fatalf("nil callback error = %v, want ErrInvalidMetadata", err)
	}
	provider.invalidate("anything")

	base := time.Unix(1_800_000_000, 0)
	first := vimeoAuthenticatedViewerTestJWT(base.Add(time.Hour).Unix(), "invalidate-first")
	second := vimeoAuthenticatedViewerTestJWT(base.Add(2*time.Hour).Unix(), "invalidate-second")
	transport := newVimeoAuthenticatedViewerFixtureTransport([]*http.Cookie{{Name: "vimeo", Value: "session"}})
	transport.viewers = [][]byte{
		[]byte(`{"jwt":"` + first + `"}`),
		[]byte(`{"jwt":"` + second + `"}`),
	}
	provider, err := vimeoAuthenticatedViewerTokenProviderFromTransport(transport, func() time.Time { return base })
	if err != nil {
		t.Fatal(err)
	}
	if got, err := provider.get(context.Background()); err != nil || got != first {
		t.Fatalf("first get = %q, %v", got, err)
	}
	provider.invalidate("different-token")
	if got, err := provider.get(context.Background()); err != nil || got != first || transport.viewerCalls != 1 {
		t.Fatalf("mismatch invalidation changed cache: got=%q err=%v calls=%d", got, err, transport.viewerCalls)
	}
	provider.invalidate(first)
	if got, err := provider.get(context.Background()); err != nil || got != second || transport.viewerCalls != 2 {
		t.Fatalf("matching invalidation did not refresh: got=%q err=%v calls=%d", got, err, transport.viewerCalls)
	}
}

func vimeoAuthenticatedViewerJWTWithPayload(payload []byte) string {
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"HS256"}`))
	return header + "." + base64.RawURLEncoding.EncodeToString(payload) + ".signature"
}

func TestParseVimeoAuthenticatedViewerJWT(t *testing.T) {
	const secret = "VimeoViewerParserSecret-AAAAA-12345"
	valid := vimeoAuthenticatedViewerJWTWithPayload([]byte(`{"exp":4102444800}`))
	if token, expires, err := parseVimeoAuthenticatedViewerJWT(valid); err != nil || token != valid || expires != 4_102_444_800 {
		t.Fatalf("valid parse = %q, %d, %v", token, expires, err)
	}

	oversizedPayload := bytes.Repeat([]byte("x"), vimeoAuthenticatedViewerMaxJWTPayload+1)
	for _, test := range []struct {
		name  string
		token string
	}{
		{name: "empty"},
		{name: "overlong", token: strings.Repeat("a", vimeoAuthenticatedViewerMaxJWTBytes+1)},
		{name: "two segments", token: "a.b"},
		{name: "invalid charset", token: "a.b.c!"},
		{name: "invalid base64 payload", token: "a.a.a"},
		{name: "empty payload", token: "a..a"},
		{name: "oversized payload", token: vimeoAuthenticatedViewerJWTWithPayload(oversizedPayload)},
		{name: "malformed JSON", token: vimeoAuthenticatedViewerJWTWithPayload([]byte(`{"exp":`))},
		{name: "trailing JSON", token: vimeoAuthenticatedViewerJWTWithPayload([]byte(`{"exp":1}{"secret":"` + secret + `"}`))},
		{name: "missing exp", token: vimeoAuthenticatedViewerJWTWithPayload([]byte(`{"secret":"` + secret + `"}`))},
		{name: "string exp", token: vimeoAuthenticatedViewerJWTWithPayload([]byte(`{"exp":"1"}`))},
		{name: "fractional exp", token: vimeoAuthenticatedViewerJWTWithPayload([]byte(`{"exp":1.5}`))},
		{name: "zero exp", token: vimeoAuthenticatedViewerJWTWithPayload([]byte(`{"exp":0}`))},
		{name: "negative exp", token: vimeoAuthenticatedViewerJWTWithPayload([]byte(`{"exp":-1}`))},
	} {
		t.Run(test.name, func(t *testing.T) {
			token, expires, err := parseVimeoAuthenticatedViewerJWT(test.token)
			if !errors.Is(err, ErrInvalidMetadata) || token != "" || expires != 0 {
				t.Fatalf("parse = %q, %d, %v; want ErrInvalidMetadata", token, expires, err)
			}
			if strings.Contains(err.Error(), secret) || err.Error() != "invalid extractor metadata: malformed Vimeo authenticated viewer token" {
				t.Fatalf("error is not fixed and secret-safe: %q", err)
			}
		})
	}
}

func FuzzParseVimeoAuthenticatedViewerJWT(f *testing.F) {
	const secret = "VimeoViewerFuzzSecret-AAAAA-12345"
	f.Add("")
	f.Add(vimeoAuthenticatedViewerJWTWithPayload([]byte(`{"exp":4102444800}`)))
	f.Add(vimeoAuthenticatedViewerJWTWithPayload([]byte(`{"secret":"` + secret + `"}`)))
	f.Add(secret)
	f.Fuzz(func(t *testing.T, input string) {
		token, expires, err := parseVimeoAuthenticatedViewerJWT(input)
		if err == nil {
			if token != input || token == "" || len(token) > vimeoAuthenticatedViewerMaxJWTBytes || expires <= 0 {
				t.Fatalf("invalid success: token_bytes=%d expires=%d", len(token), expires)
			}
			return
		}
		if !errors.Is(err, ErrInvalidMetadata) {
			t.Fatalf("error = %v, want ErrInvalidMetadata", err)
		}
		if strings.Contains(err.Error(), secret) {
			t.Fatalf("error leaked fuzz marker: %q", err)
		}
	})
}
