// Package network implements the shared HTTP transport used by extraction and downloading.
package network

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"time"
)

const (
	defaultTimeout     = 30 * time.Second
	defaultMaxPageSize = 16 << 20
)

var (
	ErrPageTooLarge = errors.New("HTTP response exceeds page size limit")
	ErrInvalidProxy = errors.New("invalid proxy URL")
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
	RoundTripper   http.RoundTripper
}

// Client owns cookies and shared HTTP behavior for one operation.
type Client struct {
	httpClient     *http.Client
	defaultHeaders http.Header
	maxPageSize    int64
}

func New(config Config) (*Client, error) {
	transport := config.RoundTripper
	if transport == nil {
		base := http.DefaultTransport.(*http.Transport).Clone()
		if config.Proxy != "" {
			proxyURL, err := url.Parse(config.Proxy)
			if err != nil || proxyURL.Scheme == "" || proxyURL.Host == "" {
				return nil, fmt.Errorf("%w %q", ErrInvalidProxy, config.Proxy)
			}
			base.Proxy = http.ProxyURL(proxyURL)
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
	if headers.Get("User-Agent") == "" {
		headers.Set("User-Agent", "ytdlp-go/0.0.0-dev")
	}

	return &Client{
		httpClient: &http.Client{
			Transport: transport,
			Jar:       jar,
			Timeout:   timeout,
		},
		defaultHeaders: headers,
		maxPageSize:    maxPageSize,
	}, nil
}

// Do clones request, applies operation defaults, and binds it to ctx. The
// caller owns and must close a successful response body.
func (client *Client) Do(ctx context.Context, request *http.Request) (*http.Response, error) {
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
	response, err := client.httpClient.Do(cloned)
	if err != nil {
		return nil, fmt.Errorf("HTTP %s %s: %w", cloned.Method, RedactURL(cloned.URL), err)
	}
	return response, nil
}

// ReadPage fetches a bounded successful response and always closes its body.
func (client *Client) ReadPage(ctx context.Context, rawURL string) ([]byte, http.Header, error) {
	request, err := http.NewRequest(http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, nil, fmt.Errorf("create page request: %w", err)
	}
	response, err := client.Do(ctx, request)
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
