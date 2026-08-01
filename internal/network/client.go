// Package network implements the shared HTTP transport used by extraction and downloading.
package network

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"sync"
	"syscall"
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
	ErrInvalidSourceAddress     = errors.New("invalid source address")
	ErrConflictingAddressPolicy = errors.New("conflicting source address policy")
	ErrNetworkPolicyUnavailable = errors.New("network policy unavailable")
	ErrImpersonationUnavailable = errors.New("impersonation profile unavailable")
)

// AddressFamily is the address family selected for native TCP dials.
type AddressFamily uint8

const (
	AddressFamilyAny AddressFamily = iota
	AddressFamilyIPv4
	AddressFamilyIPv6
)

// AddressPolicy is the normalized, credential-neutral network address policy
// used by native and browser-profile transports. SourceAddress is always a
// canonical IP string when non-empty; wildcard values select a family without
// pinning a specific local interface.
type AddressPolicy struct {
	SourceAddress string
	Family        AddressFamily
}

// PolicyCapableRoundTripper is the explicit contract for injected transports.
// A custom transport that cannot bind the requested address policy must not be
// accepted as though it did. ConfigureAddressPolicy is called exactly once
// during client construction, before any request can be sent.
type PolicyCapableRoundTripper interface {
	http.RoundTripper
	ConfigureAddressPolicy(AddressPolicy) error
}

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
	SourceAddress  string
	ForceIPv4      bool
	ForceIPv6      bool
}

// Client owns cookies and shared HTTP behavior for one operation.
type Client struct {
	httpClient       *http.Client
	jar              http.CookieJar
	defaultHeaders   http.Header
	maxPageSize      int64
	defaultProfile   string
	profileConfig    impersonate.Config
	profileMu        sync.Mutex
	profiles         map[string]*impersonate.Client
	isolatedProfiles map[string]*impersonate.Client
}

func New(config Config) (*Client, error) {
	addressPolicy, err := normalizeAddressPolicy(config.SourceAddress, config.ForceIPv4, config.ForceIPv6)
	if err != nil {
		return nil, err
	}
	if config.DefaultProfile != "" {
		if _, err := impersonate.Lookup(config.DefaultProfile); err != nil {
			return nil, fmt.Errorf("%w: %s", ErrImpersonationUnavailable, config.DefaultProfile)
		}
	}
	timeout := config.Timeout
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	transport := config.RoundTripper
	if transport != nil && addressPolicy.Family != AddressFamilyAny {
		capable, ok := transport.(PolicyCapableRoundTripper)
		if !ok {
			return nil, fmt.Errorf("%w: injected transport does not support source address or IP-family policy", ErrNetworkPolicyUnavailable)
		}
		if err := capable.ConfigureAddressPolicy(addressPolicy); err != nil {
			return nil, fmt.Errorf("%w: %v", ErrNetworkPolicyUnavailable, redactPolicyError(err))
		}
	}
	if transport == nil {
		base := http.DefaultTransport.(*http.Transport).Clone()
		base.DialContext = addressPolicy.dialContext(timeout)
		base.TLSHandshakeTimeout = timeout
		base.ResponseHeaderTimeout = timeout
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
		},
		jar:            jar,
		defaultHeaders: headers,
		maxPageSize:    maxPageSize,
		defaultProfile: config.DefaultProfile,
		profileConfig: impersonate.Config{
			Proxy: config.Proxy, Timeout: timeout, Jar: jar, RootCAs: config.RootCAs, SourceAddress: addressPolicy.SourceAddress,
		},
		profiles:         make(map[string]*impersonate.Client),
		isolatedProfiles: make(map[string]*impersonate.Client),
	}
	return client, nil
}

func normalizeAddressPolicy(sourceAddress string, forceIPv4, forceIPv6 bool) (AddressPolicy, error) {
	if forceIPv4 && forceIPv6 || sourceAddress != "" && (forceIPv4 || forceIPv6) {
		return AddressPolicy{}, ErrConflictingAddressPolicy
	}
	if forceIPv4 {
		return AddressPolicy{SourceAddress: "0.0.0.0", Family: AddressFamilyIPv4}, nil
	}
	if forceIPv6 {
		return AddressPolicy{SourceAddress: "::", Family: AddressFamilyIPv6}, nil
	}
	if sourceAddress == "" {
		return AddressPolicy{}, nil
	}
	ip := net.ParseIP(sourceAddress)
	if ip == nil {
		return AddressPolicy{}, ErrInvalidSourceAddress
	}
	if v4 := ip.To4(); v4 != nil {
		return AddressPolicy{SourceAddress: v4.String(), Family: AddressFamilyIPv4}, nil
	}
	return AddressPolicy{SourceAddress: ip.String(), Family: AddressFamilyIPv6}, nil
}

func (policy AddressPolicy) dialContext(timeout time.Duration) func(context.Context, string, string) (net.Conn, error) {
	dialNetwork := "tcp"
	var localAddress net.Addr
	switch policy.Family {
	case AddressFamilyIPv4:
		dialNetwork = "tcp4"
	case AddressFamilyIPv6:
		dialNetwork = "tcp6"
	}
	if policy.SourceAddress != "" {
		localAddress = &net.TCPAddr{IP: net.ParseIP(policy.SourceAddress)}
	}
	dialer := &net.Dialer{Timeout: timeout, KeepAlive: 30 * time.Second, LocalAddr: localAddress}
	return dialContextForNetwork(dialer, dialNetwork)
}

// dialContextForNetwork keeps the policy construction readable while
// retaining the standard net.Dialer callback shape.
func dialContextForNetwork(dialer *net.Dialer, network string) func(context.Context, string, string) (net.Conn, error) {
	return func(ctx context.Context, _ string, address string) (net.Conn, error) {
		return dialer.DialContext(ctx, network, address)
	}
}

func redactPolicyError(error) error {
	return errors.New("injected transport rejected address policy")
}

// Do clones request, applies operation defaults, and binds it to ctx. The
// caller owns and must close a successful response body.
func (client *Client) Do(ctx context.Context, request *http.Request) (*http.Response, error) {
	return client.do(ctx, request, client.defaultProfile, true)
}

// DoWithoutCookies executes a native request without consulting the operation
// jar and removes any explicit Cookie header after defaults are applied. It is
// used by protocols whose client identity is incompatible with browser cookies.
func (client *Client) DoWithoutCookies(ctx context.Context, request *http.Request) (*http.Response, error) {
	return client.do(ctx, request, "", false)
}

// DoWithoutCredentials executes a native request without the operation cookie
// jar or credential-bearing request/default headers. It is intended for
// requests to unrelated third-party APIs where forwarding media-site
// credentials would cross a trust boundary.
func (client *Client) DoWithoutCredentials(ctx context.Context, request *http.Request) (*http.Response, error) {
	if request == nil {
		return nil, errors.New("HTTP request must not be nil")
	}
	cloned := client.prepareRequest(ctx, request, false, true)
	for _, key := range []string{"Authorization", "Cookie", "Proxy-Authorization"} {
		cloned.Header.Del(key)
	}
	isolated := *client.httpClient
	isolated.Jar = nil
	response, err := isolated.Do(cloned)
	if err != nil {
		return nil, &RequestError{Method: cloned.Method, URL: RedactURL(cloned.URL), Err: err}
	}
	return response, nil
}

// DoNoRedirect executes a native request with operation defaults and scoped
// operation-jar cookies, but returns the first redirect response instead of
// following it. Explicit and default Cookie headers are discarded so callers
// cannot override cookie-jar scoping. It is for security-sensitive flows whose
// credentials must never be forwarded to a redirect destination. The caller
// owns and must close a successful response body, including a 3xx response
// body.
func (client *Client) DoNoRedirect(ctx context.Context, request *http.Request) (*http.Response, error) {
	if request == nil {
		return nil, errors.New("HTTP request must not be nil")
	}
	cloned := client.prepareRequest(ctx, request, true, true)
	// The jar is the sole cookie authority for this sensitive request. net/http
	// attaches only cookies applicable to the initial request URL after this.
	cloned.Header.Del("Cookie")

	// Copy the configured native client rather than constructing one from
	// scratch: this preserves its transport, jar, timeout, and other native
	// behavior while changing only redirect handling for this request.
	noRedirect := *client.httpClient
	noRedirect.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}
	response, err := noRedirect.Do(cloned)
	if err != nil {
		return nil, &RequestError{Method: cloned.Method, URL: RedactURL(cloned.URL), Err: err}
	}
	return response, nil
}

// DoNoRedirectWithRequestCookies is the no-redirect variant for callers that
// must honor extractor-provided per-request Cookie headers. The caller remains
// responsible for stripping those headers before any cross-origin follow-up.
func (client *Client) DoNoRedirectWithRequestCookies(ctx context.Context, request *http.Request) (*http.Response, error) {
	if request == nil {
		return nil, errors.New("HTTP request must not be nil")
	}
	cloned := client.prepareRequest(ctx, request, true, true)
	noRedirect := *client.httpClient
	noRedirect.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	response, err := noRedirect.Do(cloned)
	if err != nil {
		return nil, &RequestError{Method: cloned.Method, URL: RedactURL(cloned.URL), Err: err}
	}
	return response, nil
}

// DoProfile executes a request with an explicitly named browser profile. An
// unknown or unavailable profile is an error; it never falls back to native
// net/http behavior.
func (client *Client) DoProfile(ctx context.Context, request *http.Request, profileName string) (*http.Response, error) {
	if profileName == "" {
		return client.Do(ctx, request)
	}
	return client.do(ctx, request, profileName, true)
}

// DoProfiledNoRedirect executes a profiled request using the operation jar and
// returns the first redirect response. Explicit Cookie headers are discarded so
// the jar remains the only cookie authority for the initial origin.
func (client *Client) DoProfiledNoRedirect(ctx context.Context, request *http.Request, profileName string) (*http.Response, error) {
	if request == nil {
		return nil, errors.New("HTTP request must not be nil")
	}
	if profileName == "" {
		return nil, fmt.Errorf("%w: missing profile", ErrImpersonationUnavailable)
	}
	cloned := client.prepareRequest(ctx, request, true, false)
	cloned.Header.Del("Cookie")
	cloned.Header.Del("Authorization")
	cloned.Header.Del("Proxy-Authorization")
	profile, err := client.noRedirectProfileClient(profileName)
	if err != nil {
		return nil, err
	}
	response, err := profile.Do(cloned)
	if err != nil {
		return nil, &RequestError{Method: cloned.Method, URL: RedactURL(cloned.URL), Err: err}
	}
	return response, nil
}

// DoProfiledPageNoRedirect is the bounded-extractor page variant. It shares
// the no-redirect profile boundary with sensitive form requests.
func (client *Client) DoProfiledPageNoRedirect(ctx context.Context, request *http.Request, profileName string) (*http.Response, error) {
	return client.DoProfiledNoRedirect(ctx, request, profileName)
}

func (client *Client) do(ctx context.Context, request *http.Request, profileName string, includeCookies bool) (*http.Response, error) {
	if request == nil {
		return nil, errors.New("HTTP request must not be nil")
	}
	cloned := client.prepareRequest(ctx, request, includeCookies, profileName == "")
	var response *http.Response
	var err error
	if profileName == "" {
		httpClient := client.httpClient
		if !includeCookies {
			isolated := *client.httpClient
			isolated.Jar = nil
			httpClient = &isolated
		}
		response, err = httpClient.Do(cloned)
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

func (client *Client) prepareRequest(ctx context.Context, request *http.Request, includeCookies, native bool) *http.Request {
	cloned := request.Clone(ctx)
	for key, values := range client.defaultHeaders {
		if cloned.Header.Values(key) != nil {
			continue
		}
		for _, value := range values {
			cloned.Header.Add(key, value)
		}
	}
	if native && cloned.Header.Get("User-Agent") == "" {
		cloned.Header.Set("User-Agent", "ytdlp-go/0.0.0-dev")
	}
	if !includeCookies {
		cloned.Header.Del("Cookie")
	}
	return cloned
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

// isolatedProfileClient returns a cached browser-profile client that never
// consults the operation cookie jar and never follows redirects. It is distinct
// from profileClient so credentialed/redirecting profile traffic cannot leak
// into anonymous page reads.
func (client *Client) isolatedProfileClient(name string) (*impersonate.Client, error) {
	profile, err := impersonate.Lookup(name)
	if err != nil {
		return nil, fmt.Errorf("%w: %s", ErrImpersonationUnavailable, name)
	}
	client.profileMu.Lock()
	defer client.profileMu.Unlock()
	if existing := client.isolatedProfiles[name]; existing != nil {
		return existing, nil
	}
	config := client.profileConfig
	config.Profile = profile
	config.Jar = nil
	config.DisableRedirect = true
	created, err := impersonate.New(config)
	if err != nil {
		return nil, fmt.Errorf("%w: %s", ErrImpersonationUnavailable, name)
	}
	client.isolatedProfiles[name] = created
	return created, nil
}

func (client *Client) noRedirectProfileClient(name string) (*impersonate.Client, error) {
	profile, err := impersonate.Lookup(name)
	if err != nil {
		return nil, fmt.Errorf("%w: %s", ErrImpersonationUnavailable, name)
	}
	client.profileMu.Lock()
	defer client.profileMu.Unlock()
	key := "no-redirect:" + name
	if existing := client.profiles[key]; existing != nil {
		return existing, nil
	}
	config := client.profileConfig
	config.Profile = profile
	config.DisableRedirect = true
	created, err := impersonate.New(config)
	if err != nil {
		return nil, fmt.Errorf("%w: %s", ErrImpersonationUnavailable, name)
	}
	client.profiles[key] = created
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
	for _, profile := range client.isolatedProfiles {
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

// ReadPageProfileWithoutCredentialsNoRedirect performs a bounded profile page
// read without operation-jar cookies, without Authorization/Proxy-Authorization/
// Cookie defaults or explicit headers, without persisting Set-Cookie, and
// without following redirects. An empty or unavailable profile fails closed and
// never falls back to the native transport.
func (client *Client) ReadPageProfileWithoutCredentialsNoRedirect(ctx context.Context, rawURL, profileName string) ([]byte, http.Header, error) {
	if profileName == "" {
		return nil, nil, fmt.Errorf("%w: missing profile", ErrImpersonationUnavailable)
	}
	request, err := http.NewRequest(http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, nil, errors.New("create page request failed")
	}
	cloned := client.prepareRequest(ctx, request, false, false)
	for _, key := range []string{"Authorization", "Cookie", "Proxy-Authorization"} {
		cloned.Header.Del(key)
	}
	profileClient, err := client.isolatedProfileClient(profileName)
	if err != nil {
		return nil, nil, err
	}
	response, err := profileClient.Do(cloned)
	if err != nil {
		return nil, nil, &RequestError{Method: cloned.Method, URL: RedactURL(cloned.URL), Err: err}
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		requestURL := cloned.URL
		if response.Request != nil && response.Request.URL != nil {
			requestURL = response.Request.URL
		}
		return nil, response.Header.Clone(), &StatusError{Code: response.StatusCode, URL: RedactURL(requestURL)}
	}
	reader := io.LimitReader(response.Body, client.maxPageSize+1)
	body, err := io.ReadAll(reader)
	if err != nil {
		return nil, response.Header.Clone(), errors.New("read page response failed")
	}
	if int64(len(body)) > client.maxPageSize {
		return nil, response.Header.Clone(), fmt.Errorf("%w: limit is %d bytes", ErrPageTooLarge, client.maxPageSize)
	}
	return body, response.Header.Clone(), nil
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
	return code == http.StatusRequestTimeout || code == http.StatusTooManyRequests || code >= 500 && code < 600
}

// IsRetryableError classifies only transient HTTP statuses and transport-level
// failures. It deliberately excludes context termination and malformed or
// policy/security errors so callers can safely use it for bounded outer retry
// loops without duplicating downloader classification.
func IsRetryableError(err error) bool {
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	var status *StatusError
	if errors.As(err, &status) {
		return RetryableStatus(status.Code)
	}
	var requestErr *RequestError
	if errors.As(err, &requestErr) {
		return isRetryableTransportError(requestErr.Err)
	}
	return isRetryableTransportError(err)
}

func isRetryableTransportError(err error) bool {
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		// NXDOMAIN and equivalent authoritative not-found answers are stable for
		// the attempted name. Some resolvers also mark them temporary; the
		// deterministic not-found signal takes precedence.
		return !dnsErr.IsNotFound && (dnsErr.IsTimeout || dnsErr.IsTemporary)
	}
	for _, transient := range []error{
		syscall.ECONNABORTED,
		syscall.ECONNREFUSED,
		syscall.ECONNRESET,
		syscall.EHOSTUNREACH,
		syscall.ENETUNREACH,
		syscall.ETIMEDOUT,
		syscall.EPIPE,
	} {
		if errors.Is(err, transient) {
			return true
		}
	}
	var netErr net.Error
	return errors.As(err, &netErr) && (netErr.Timeout() || netErr.Temporary())
}

var sensitiveQueryKeys = map[string]struct{}{
	"auth": {}, "authorization": {}, "key": {}, "sig": {}, "signature": {}, "token": {}, "x-amz-signature": {},
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
