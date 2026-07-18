// Package network implements the shared HTTP transport used by extraction and downloading.
package network

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/ytdlp-go/ytdlp/internal/network/impersonate"
)

const (
	defaultTimeout     = 30 * time.Second
	defaultMaxPageSize = 16 << 20
)

var (
	ErrPageTooLarge             = errors.New("HTTP response exceeds page size limit")
	ErrInvalidProxy             = errors.New("invalid proxy URL")
	ErrInvalidCookie            = errors.New("invalid cookie")
	ErrImpersonationUnavailable = errors.New("impersonation profile unavailable")
)

// Doer is the minimal transport boundary consumed by extractors and downloaders.
type Doer interface {
	Do(context.Context, *http.Request) (*http.Response, error)
}

type Config struct {
	Proxy          string
	Timeout        time.Duration
	MaxPageSize    int64
	DefaultHeaders http.Header
	DefaultProfile string
	RoundTripper   http.RoundTripper
	RootCAs        *x509.CertPool
}

// Client owns cookies and shared HTTP behavior for one operation.
type Client struct {
	httpClient     *http.Client
	jar            http.CookieJar
	defaultHeaders http.Header
	maxPageSize    int64
	defaultProfile string
	profileConfig  impersonate.Config
	profileMu      sync.Mutex
	profiles       map[string]*impersonate.Client
}

func New(config Config) (*Client, error) {
	if config.DefaultProfile != "" {
		if _, err := impersonate.Lookup(config.DefaultProfile); err != nil {
			return nil, fmt.Errorf("%w: %s", ErrImpersonationUnavailable, config.DefaultProfile)
		}
	}
	transport := config.RoundTripper
	if transport == nil {
		base := http.DefaultTransport.(*http.Transport).Clone()
		if config.Proxy != "" {
			proxyURL, err := url.Parse(config.Proxy)
			if err != nil || proxyURL.Scheme == "" || proxyURL.Host == "" {
				return nil, ErrInvalidProxy
			}
			base.Proxy = http.ProxyURL(proxyURL)
		}
		if config.RootCAs != nil {
			if base.TLSClientConfig == nil {
				base.TLSClientConfig = &tls.Config{}
			} else {
				base.TLSClientConfig = base.TLSClientConfig.Clone()
			}
			base.TLSClientConfig.RootCAs = config.RootCAs
		}
		transport = base
	}
	jar, err := cookiejar.New(nil)
	if err != nil {
		return nil, fmt.Errorf("create cookie jar: %w", err)
	}
	timeout := config.Timeout
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	maxPageSize := config.MaxPageSize
	if maxPageSize <= 0 {
		maxPageSize = defaultMaxPageSize
	}
	headers := config.DefaultHeaders.Clone()
	if headers == nil {
		headers = make(http.Header)
	}
	client := &Client{
		httpClient: &http.Client{
			Transport: transport,
			Jar:       jar,
			Timeout:   timeout,
		},
		jar:            jar,
		defaultHeaders: headers,
		maxPageSize:    maxPageSize,
		defaultProfile: config.DefaultProfile,
		profileConfig: impersonate.Config{
			Proxy: config.Proxy, Timeout: timeout, Jar: jar, RootCAs: config.RootCAs,
		},
		profiles: make(map[string]*impersonate.Client),
	}
	return client, nil
}

// Do clones request, applies operation defaults, and binds it to ctx. The
// caller owns and must close a successful response body.
func (client *Client) Do(ctx context.Context, request *http.Request) (*http.Response, error) {
	return client.do(ctx, request, client.defaultProfile)
}

// DoProfile executes a request with an explicitly named browser profile. An
// unknown or unavailable profile is an error; it never falls back to native
// net/http behavior.
func (client *Client) DoProfile(ctx context.Context, request *http.Request, profileName string) (*http.Response, error) {
	if profileName == "" {
		return client.Do(ctx, request)
	}
	return client.do(ctx, request, profileName)
}

func (client *Client) do(ctx context.Context, request *http.Request, profileName string) (*http.Response, error) {
	if request == nil {
		return nil, errors.New("HTTP request must not be nil")
	}
	cloned := request.Clone(ctx)
	for key, values := range client.defaultHeaders {
		if cloned.Header.Values(key) != nil {
			continue
		}
		for _, value := range values {
			cloned.Header.Add(key, value)
		}
	}
	if profileName == "" && cloned.Header.Get("User-Agent") == "" {
		cloned.Header.Set("User-Agent", "ytdlp-go/0.0.0-dev")
	}
	var response *http.Response
	var err error
	if profileName == "" {
		response, err = client.httpClient.Do(cloned)
	} else {
		profileClient, profileErr := client.profileClient(profileName)
		if profileErr != nil {
			return nil, profileErr
		}
		response, err = profileClient.Do(cloned)
	}
	if err != nil {
		return nil, &RequestError{Method: cloned.Method, URL: RedactURL(cloned.URL), Err: err}
	}
	return response, nil
}

func (client *Client) profileClient(name string) (*impersonate.Client, error) {
	profile, err := impersonate.Lookup(name)
	if err != nil {
		return nil, fmt.Errorf("%w: %s", ErrImpersonationUnavailable, name)
	}
	client.profileMu.Lock()
	defer client.profileMu.Unlock()
	if existing := client.profiles[name]; existing != nil {
		return existing, nil
	}
	config := client.profileConfig
	config.Profile = profile
	created, err := impersonate.New(config)
	if err != nil {
		return nil, fmt.Errorf("%w: %s", ErrImpersonationUnavailable, name)
	}
	client.profiles[name] = created
	return created, nil
}

// CloseIdleConnections releases pooled native and impersonated connections.
func (client *Client) CloseIdleConnections() {
	client.httpClient.CloseIdleConnections()
	client.profileMu.Lock()
	defer client.profileMu.Unlock()
	for _, profile := range client.profiles {
		profile.CloseIdleConnections()
	}
}

// ReadPage fetches a bounded successful response and always closes its body.
func (client *Client) ReadPage(ctx context.Context, rawURL string) ([]byte, http.Header, error) {
	return client.readPage(ctx, rawURL, client.defaultProfile)
}

// ReadPageProfile is the bounded page helper for named browser profiles.
func (client *Client) ReadPageProfile(ctx context.Context, rawURL, profileName string) ([]byte, http.Header, error) {
	if profileName == "" {
		return client.ReadPage(ctx, rawURL)
	}
	return client.readPage(ctx, rawURL, profileName)
}

func (client *Client) readPage(ctx context.Context, rawURL, profileName string) ([]byte, http.Header, error) {
	request, err := http.NewRequest(http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, nil, fmt.Errorf("create page request: %w", err)
	}
	response, err := client.DoProfile(ctx, request, profileName)
	if err != nil {
		return nil, nil, err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, response.Header.Clone(), &StatusError{Code: response.StatusCode, URL: RedactURL(response.Request.URL)}
	}
	reader := io.LimitReader(response.Body, client.maxPageSize+1)
	body, err := io.ReadAll(reader)
	if err != nil {
		return nil, response.Header.Clone(), fmt.Errorf("read page response: %w", err)
	}
	if int64(len(body)) > client.maxPageSize {
		return nil, response.Header.Clone(), fmt.Errorf("%w: limit is %d bytes", ErrPageTooLarge, client.maxPageSize)
	}
	return body, response.Header.Clone(), nil
}

// Cookies returns a defensive snapshot of cookies applicable to rawURL. This
// is primarily used to prove native/impersonated jar continuity.
func (client *Client) Cookies(rawURL string) ([]*http.Cookie, error) {
	target, err := url.Parse(rawURL)
	if err != nil || target.Scheme == "" || target.Host == "" {
		return nil, errors.New("invalid cookie URL")
	}
	cookies := client.jar.Cookies(target)
	cloned := make([]*http.Cookie, len(cookies))
	for index, cookie := range cookies {
		copy := *cookie
		copy.Unparsed = append([]string(nil), cookie.Unparsed...)
		cloned[index] = &copy
	}
	return cloned, nil
}

// AddCookies seeds the operation jar with browser cookies. Chromium host-only
// cookies omit the leading dot; clear Domain for those entries so cookiejar
// does not widen their scope to subdomains.
func (client *Client) AddCookies(cookies []*http.Cookie) error {
	for _, cookie := range cookies {
		if cookie == nil || cookie.Name == "" {
			return ErrInvalidCookie
		}
		host := strings.TrimPrefix(cookie.Domain, ".")
		if host == "" || strings.ContainsAny(host, "/?#@") {
			return ErrInvalidCookie
		}
		scheme := "http"
		if cookie.Secure {
			scheme = "https"
		}
		target := &url.URL{Scheme: scheme, Host: host, Path: "/"}
		if target.Hostname() == "" {
			return ErrInvalidCookie
		}
		cloned := *cookie
		cloned.Unparsed = append([]string(nil), cookie.Unparsed...)
		if !strings.HasPrefix(cookie.Domain, ".") {
			cloned.Domain = ""
		}
		client.jar.SetCookies(target, []*http.Cookie{&cloned})
	}
	return nil
}

func SupportedImpersonationProfiles() []impersonate.Profile { return impersonate.Supported() }

// RequestError retains the underlying cause for errors.Is/errors.As while its
// rendered message omits dependency-provided URLs and proxy credentials.
type RequestError struct {
	Method string
	URL    string
	Err    error
}

func (err *RequestError) Error() string {
	if err == nil {
		return "<nil>"
	}
	if errors.Is(err.Err, context.Canceled) {
		return fmt.Sprintf("HTTP %s %s: context canceled", err.Method, err.URL)
	}
	if errors.Is(err.Err, context.DeadlineExceeded) {
		return fmt.Sprintf("HTTP %s %s: context deadline exceeded", err.Method, err.URL)
	}
	return fmt.Sprintf("HTTP %s %s: request failed", err.Method, err.URL)
}

func (err *RequestError) Unwrap() error { return err.Err }

type StatusError struct {
	Code int
	URL  string
}

func (err *StatusError) Error() string {
	return fmt.Sprintf("HTTP status %d for %s", err.Code, err.URL)
}

func RetryableStatus(code int) bool {
	return code == http.StatusRequestTimeout || code == http.StatusTooManyRequests || code >= 500
}

var sensitiveQueryKeys = map[string]struct{}{
	"auth": {}, "authorization": {}, "key": {}, "sig": {}, "signature": {}, "token": {},
}

// RedactURL removes commonly sensitive query values from diagnostic output.
func RedactURL(input *url.URL) string {
	if input == nil {
		return "<nil>"
	}
	cloned := *input
	query := cloned.Query()
	for key := range query {
		if _, sensitive := sensitiveQueryKeys[strings.ToLower(key)]; sensitive {
			query.Set(key, "REDACTED")
		}
	}
	cloned.RawQuery = query.Encode()
	if cloned.User != nil {
		cloned.User = url.User("REDACTED")
	}
	return cloned.String()
}

// RedactRawURL is the string-input counterpart to RedactURL. It is intended
// for diagnostics and events only; callers must retain the original URL for
// requests and resumable state. Invalid URLs are deliberately not echoed.
func RedactRawURL(rawURL string) string {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return "<invalid URL>"
	}
	return RedactURL(parsed)
}

// RedactHeaders returns a clone safe for diagnostics.
func RedactHeaders(headers http.Header) http.Header {
	redacted := headers.Clone()
	for _, key := range []string{"Authorization", "Cookie", "Proxy-Authorization", "Set-Cookie"} {
		if redacted.Values(key) != nil {
			redacted.Set(key, "REDACTED")
		}
	}
	return redacted
}
