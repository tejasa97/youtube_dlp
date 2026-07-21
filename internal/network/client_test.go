package network

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/ytdlp-go/ytdlp/internal/network/impersonate"
	"github.com/ytdlp-go/ytdlp/internal/testserver"
)

func TestClientHeadersCookiesRedirectsAndCompression(t *testing.T) {
	server := testserver.New()
	defer server.Close()
	client, err := New(Config{DefaultHeaders: http.Header{"X-Fixture": []string{"present"}}})
	if err != nil {
		t.Fatal(err)
	}

	body, _, err := client.ReadPage(context.Background(), server.URL+"/headers")
	if err != nil {
		t.Fatalf("headers: %v", err)
	}
	if !strings.Contains(string(body), `"x_fixture":"present"`) || !strings.Contains(string(body), `"user_agent":"ytdlp-go/`) {
		t.Fatalf("header response = %s", body)
	}
	if _, _, err := client.ReadPage(context.Background(), server.URL+"/cookies/set"); err != nil {
		t.Fatalf("cookie set: %v", err)
	}
	if _, _, err := client.ReadPage(context.Background(), server.URL+"/cookies/check"); err != nil {
		t.Fatalf("cookie check: %v", err)
	}
	body, _, err = client.ReadPage(context.Background(), server.URL+"/redirect")
	if err != nil || !strings.Contains(string(body), `"fixture-direct"`) {
		t.Fatalf("redirect body = %s, error = %v", body, err)
	}
	body, _, err = client.ReadPage(context.Background(), server.URL+"/gzip")
	if err != nil || string(body) != "deterministic gzip response\n" {
		t.Fatalf("gzip body = %q, error = %v", body, err)
	}
}

func TestReadPageWithHeadersIsBounded(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Header.Get("X-Fixture") != "present" {
			http.Error(writer, "missing header", http.StatusForbidden)
			return
		}
		_, _ = writer.Write([]byte("bounded"))
	}))
	defer server.Close()
	client, err := New(Config{})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := ReadPageWithHeaders(context.Background(), client, server.URL, http.Header{"X-Fixture": []string{"present"}}, 6); !errors.Is(err, ErrPageTooLarge) {
		t.Fatalf("oversized response error = %v", err)
	}
	body, _, err := ReadPageWithHeaders(context.Background(), client, server.URL, http.Header{"X-Fixture": []string{"present"}}, 7)
	if err != nil || string(body) != "bounded" {
		t.Fatalf("body = %q, error = %v", body, err)
	}
}

// The historical replay includes yt-dlp 272657252, which fixed cookie leakage
// by an external downloader across redirects. The native Go path delegates
// redirect scoping to its cookie jar; keep that boundary under regression.
func TestRedirectDoesNotForwardHostCookieCrossHost(t *testing.T) {
	var leaked string
	target := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		leaked = request.Header.Get("Cookie")
		_, _ = io.WriteString(writer, "target")
	}))
	defer target.Close()
	source := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		http.SetCookie(writer, &http.Cookie{Name: "source_secret", Value: "do-not-forward", Path: "/"})
		http.Redirect(writer, request, target.URL, http.StatusFound)
	}))
	defer source.Close()
	sourceURL := strings.Replace(source.URL, "127.0.0.1", "localhost", 1)
	client, _ := New(Config{})
	if _, _, err := client.ReadPage(context.Background(), sourceURL); err != nil {
		t.Fatal(err)
	}
	if leaked != "" {
		t.Fatalf("cross-host redirect leaked cookies: %q", leaked)
	}
}

func TestImpersonatedRedirectDoesNotForwardCredentialsCrossHost(t *testing.T) {
	var authorization, cookie string
	target := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		authorization = request.Header.Get("Authorization")
		cookie = request.Header.Get("Cookie")
		_, _ = io.WriteString(writer, "target")
	}))
	defer target.Close()
	source := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		http.Redirect(writer, request, target.URL, http.StatusFound)
	}))
	defer source.Close()

	// Force different hostnames so net/http's redirect policy treats the
	// destination as a different origin even though both fixtures are local.
	sourceURL := strings.Replace(source.URL, "127.0.0.1", "localhost", 1)
	client, err := New(Config{})
	if err != nil {
		t.Fatal(err)
	}
	request, _ := http.NewRequest(http.MethodGet, sourceURL, nil)
	request.Header.Set("Authorization", "Bearer do-not-forward")
	request.Header.Set("Cookie", "explicit_secret=do-not-forward")
	response, err := client.DoProfile(context.Background(), request, impersonate.Chrome133Name)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if authorization != "" || cookie != "" {
		t.Fatalf("cross-host impersonated redirect leaked authorization %q or cookie %q", authorization, cookie)
	}
}

func TestReadPageLimitAndCancellation(t *testing.T) {
	server := testserver.New()
	defer server.Close()
	client, _ := New(Config{MaxPageSize: 32})
	if _, _, err := client.ReadPage(context.Background(), server.URL+"/large?size=64"); !errors.Is(err, ErrPageTooLarge) {
		t.Fatalf("large page error = %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if _, _, err := client.ReadPage(ctx, server.URL+"/slow?delay=1s"); err == nil || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("cancel error = %v", err)
	}
}

func TestConfiguredTimeoutDoesNotCapActiveResponseBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		flusher, _ := writer.(http.Flusher)
		for range 6 {
			_, _ = io.WriteString(writer, "chunk")
			flusher.Flush()
			time.Sleep(15 * time.Millisecond)
		}
	}))
	defer server.Close()
	client, err := New(Config{Timeout: 40 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	body, _, err := client.ReadPage(context.Background(), server.URL)
	if err != nil || string(body) != strings.Repeat("chunk", 6) {
		t.Fatalf("body=%q error=%v", body, err)
	}
	if time.Since(started) <= 40*time.Millisecond {
		t.Fatal("fixture did not exceed the configured connection/header timeout")
	}
}

func TestClientUsesConfiguredProxy(t *testing.T) {
	var received string
	proxy := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		received = request.URL.String()
		_, _ = writer.Write([]byte("proxied"))
	}))
	defer proxy.Close()
	client, err := New(Config{Proxy: proxy.URL})
	if err != nil {
		t.Fatal(err)
	}
	body, _, err := client.ReadPage(context.Background(), "http://media.invalid/page")
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "proxied" || received != "http://media.invalid/page" {
		t.Fatalf("body = %q, URL = %q", body, received)
	}
}

func TestImpersonationUsesProxyAndRedactsFailures(t *testing.T) {
	var received, method string
	proxy := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		received = request.URL.String()
		method = request.Method
		http.Error(writer, "fixture proxy rejects tunnel", http.StatusBadGateway)
	}))
	defer proxy.Close()
	client, err := New(Config{Proxy: proxy.URL})
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = client.ReadPageProfile(context.Background(), "http://media.invalid/page", impersonate.Chrome133Name)
	if err == nil || method != http.MethodConnect || received != "//media.invalid:80" {
		t.Fatalf("method = %q, URL = %q, error = %v", method, received, err)
	}
	_, _, err = client.ReadPageProfile(context.Background(), "http://media.invalid/page?token=secret", impersonate.Chrome133Name)
	if err == nil || strings.Contains(err.Error(), "secret") || !strings.Contains(err.Error(), "REDACTED") {
		t.Fatalf("redacted profile error = %v", err)
	}
	if _, err := New(Config{Proxy: "://user:secret@invalid"}); !errors.Is(err, ErrInvalidProxy) || strings.Contains(err.Error(), "secret") {
		t.Fatalf("invalid proxy error = %v", err)
	}
}

func TestDoLeavesBodyOwnershipToCaller(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(writer, "body")
	}))
	defer server.Close()
	client, _ := New(Config{})
	request, _ := http.NewRequest(http.MethodGet, server.URL, nil)
	response, err := client.Do(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if response.Body == nil {
		t.Fatal("response body is nil")
	}
	response.Body.Close()
}

func TestImpersonatedProtectedFlowAndSharedCookies(t *testing.T) {
	fixture := loadProtectedFlowFixture(t)
	hybridCurve := tls.CurveID(fixture.HybridCurveID)
	protected := httptest.NewUnstartedServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		for key, fragment := range fixture.RequiredHeaders {
			if !strings.Contains(request.Header.Get(key), fragment) {
				http.Error(writer, "browser headers required", http.StatusForbidden)
				return
			}
		}
		cookie, err := request.Cookie("native_cookie")
		if err != nil || cookie.Value != "shared" {
			http.Error(writer, "shared cookie required", http.StatusForbidden)
			return
		}
		switch request.URL.Path {
		case "/slow":
			select {
			case <-request.Context().Done():
			case <-time.After(time.Second):
			}
			return
		case "/large":
			_, _ = io.WriteString(writer, strings.Repeat("x", 64))
			return
		}
		http.SetCookie(writer, &http.Cookie{Name: "profile_cookie", Value: "returned", Path: "/"})
		_, _ = io.WriteString(writer, fixture.ExpectedBody)
	}))
	protected.Config.ErrorLog = log.New(io.Discard, "", 0)
	protected.TLS = &tls.Config{GetConfigForClient: func(hello *tls.ClientHelloInfo) (*tls.Config, error) {
		for _, curve := range hello.SupportedCurves {
			if curve == hybridCurve {
				return nil, nil
			}
		}
		return nil, errors.New("Chrome 133 hybrid curve required")
	}}
	protected.StartTLS()
	defer protected.Close()

	pool := x509.NewCertPool()
	pool.AddCert(protected.Certificate())
	client, err := New(Config{RootCAs: pool})
	if err != nil {
		t.Fatal(err)
	}
	target, _ := url.Parse(protected.URL)
	client.jar.SetCookies(target, []*http.Cookie{{Name: "native_cookie", Value: "shared", Path: "/"}})

	if _, _, err := client.ReadPage(context.Background(), protected.URL); err == nil {
		t.Fatal("native transport unexpectedly passed protected TLS flow")
	}
	body, _, err := client.ReadPageProfile(context.Background(), protected.URL, fixture.Profile)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != fixture.ExpectedBody {
		t.Fatalf("body = %q", body)
	}
	cookies, err := client.Cookies(protected.URL)
	if err != nil {
		t.Fatal(err)
	}
	seen := make(map[string]string)
	for _, cookie := range cookies {
		seen[cookie.Name] = cookie.Value
	}
	if seen["native_cookie"] != "shared" || seen["profile_cookie"] != "returned" {
		t.Fatalf("shared cookies = %#v", seen)
	}

	bounded, err := New(Config{RootCAs: pool, MaxPageSize: 32})
	if err != nil {
		t.Fatal(err)
	}
	bounded.jar.SetCookies(target, []*http.Cookie{{Name: "native_cookie", Value: "shared", Path: "/"}})
	if _, _, err := bounded.ReadPageProfile(context.Background(), protected.URL+"/large", impersonate.Chrome133Name); !errors.Is(err, ErrPageTooLarge) {
		t.Fatalf("bounded profile error = %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if _, _, err := client.ReadPageProfile(ctx, protected.URL+"/slow", impersonate.Chrome133Name); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("profile cancellation error = %v", err)
	}
}

type protectedFlowFixture struct {
	Version         int               `json:"version"`
	Profile         string            `json:"profile"`
	Engine          string            `json:"engine"`
	EngineVersion   string            `json:"engine_version"`
	HybridCurveID   uint16            `json:"hybrid_curve_id"`
	RequiredHeaders map[string]string `json:"required_headers"`
	ExpectedBody    string            `json:"expected_body"`
}

func loadProtectedFlowFixture(t *testing.T) protectedFlowFixture {
	t.Helper()
	data, err := os.ReadFile("../../conformance/network/impersonation/protected-flow.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture protectedFlowFixture
	if err := json.Unmarshal(data, &fixture); err != nil {
		t.Fatal(err)
	}
	if fixture.Version != 1 || fixture.Profile != impersonate.Chrome133Name ||
		fixture.Engine != "github.com/imroc/req/v3" || fixture.EngineVersion != impersonate.ReqVersion || fixture.HybridCurveID == 0 ||
		len(fixture.RequiredHeaders) == 0 || fixture.ExpectedBody == "" {
		t.Fatalf("invalid protected-flow fixture: %#v", fixture)
	}
	return fixture
}

func TestImpersonationRejectsUnknownProfileWithoutFallback(t *testing.T) {
	client, err := New(Config{})
	if err != nil {
		t.Fatal(err)
	}
	request, _ := http.NewRequest(http.MethodGet, "https://example.invalid", nil)
	_, err = client.DoProfile(context.Background(), request, "unknown-profile")
	if !errors.Is(err, ErrImpersonationUnavailable) {
		t.Fatalf("error = %v", err)
	}
}

func TestImpersonationProfileDiagnostics(t *testing.T) {
	profiles := SupportedImpersonationProfiles()
	if len(profiles) != 2 || profiles[0].Name != impersonate.Chrome133Name || profiles[1].Name != impersonate.Firefox120Name || profiles[0].EngineVersion != impersonate.ReqVersion || profiles[1].EngineVersion != impersonate.ReqVersion {
		t.Fatalf("profiles = %#v", profiles)
	}
}

func TestDefaultImpersonationProfileAppliesAndExplicitProfileOverrides(t *testing.T) {
	observed := make(chan string, 2)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		observed <- request.UserAgent()
		_, _ = writer.Write([]byte("ok"))
	}))
	defer server.Close()
	client, err := New(Config{DefaultProfile: impersonate.Firefox120Name})
	if err != nil {
		t.Fatal(err)
	}
	request, _ := http.NewRequest(http.MethodGet, server.URL, nil)
	response, err := client.Do(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if userAgent := <-observed; !strings.Contains(userAgent, "Firefox/120.0") {
		t.Fatalf("default user agent=%q", userAgent)
	}
	request, _ = http.NewRequest(http.MethodGet, server.URL, nil)
	response, err = client.DoProfile(context.Background(), request, impersonate.Chrome133Name)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if userAgent := <-observed; !strings.Contains(userAgent, "Chrome/133.0.0.0") {
		t.Fatalf("override user agent=%q", userAgent)
	}
	if _, err := New(Config{DefaultProfile: "firefox-latest"}); !errors.Is(err, ErrImpersonationUnavailable) {
		t.Fatalf("unknown default error=%v", err)
	}
}

func TestAddCookiesPreservesHostScopeAndSecurity(t *testing.T) {
	client, err := New(Config{})
	if err != nil {
		t.Fatal(err)
	}
	err = client.AddCookies([]*http.Cookie{
		{Name: "host_only", Value: "one", Domain: "example.com", Path: "/"},
		{Name: "domain", Value: "two", Domain: ".example.com", Path: "/"},
		{Name: "secure", Value: "three", Domain: ".example.com", Path: "/", Secure: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	root, _ := client.Cookies("https://example.com/")
	if got := cookieValues(root); got["host_only"] != "one" || got["domain"] != "two" || got["secure"] != "three" {
		t.Fatalf("root cookies = %#v", got)
	}
	subdomain, _ := client.Cookies("https://sub.example.com/")
	if got := cookieValues(subdomain); got["host_only"] != "" || got["domain"] != "two" || got["secure"] != "three" {
		t.Fatalf("subdomain cookies = %#v", got)
	}
	insecure, _ := client.Cookies("http://example.com/")
	if got := cookieValues(insecure); got["secure"] != "" {
		t.Fatalf("insecure cookies = %#v", got)
	}
	for _, invalid := range [][]*http.Cookie{nil, {nil}, {{Name: "", Domain: "example.com"}}, {{Name: "x", Domain: "bad/path"}}} {
		if invalid == nil {
			continue
		}
		if err := client.AddCookies(invalid); !errors.Is(err, ErrInvalidCookie) {
			t.Fatalf("AddCookies(%#v) error = %v", invalid, err)
		}
	}
}

func cookieValues(cookies []*http.Cookie) map[string]string {
	values := make(map[string]string, len(cookies))
	for _, cookie := range cookies {
		values[cookie.Name] = cookie.Value
	}
	return values
}

func TestRedaction(t *testing.T) {
	parsed, _ := url.Parse("https://user:secret@example.invalid/v?token=secret&visible=yes")
	redacted := RedactURL(parsed)
	if strings.Contains(redacted, "secret") || !strings.Contains(redacted, "visible=yes") {
		t.Fatalf("RedactURL() = %q", redacted)
	}
	if raw := RedactRawURL(parsed.String()); raw != redacted {
		t.Fatalf("RedactRawURL() = %q, want %q", raw, redacted)
	}
	if raw := RedactRawURL("https://example.invalid/%zz?token=secret"); raw != "<invalid URL>" {
		t.Fatalf("invalid RedactRawURL() = %q", raw)
	}
	headers := RedactHeaders(http.Header{"Authorization": []string{"secret"}, "X-Safe": []string{"yes"}})
	if headers.Get("Authorization") != "REDACTED" || headers.Get("X-Safe") != "yes" {
		t.Fatalf("RedactHeaders() = %v", headers)
	}
}
