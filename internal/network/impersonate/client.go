package impersonate

import (
	"crypto/x509"
	"errors"
	"io"
	"net/http"
	"net/url"
	"time"

	fhttp "github.com/bogdanfinn/fhttp"
	tlsclient "github.com/bogdanfinn/tls-client"
)

type Config struct {
	Profile Profile
	Proxy   string
	Timeout time.Duration
	Jar     http.CookieJar
	RootCAs *x509.CertPool
}

// Client adapts the fhttp-based impersonation stack to the repository's
// standard net/http boundary.
type Client struct {
	profile Profile
	client  tlsclient.HttpClient
}

func New(config Config) (*Client, error) {
	if config.Profile.Name == "" {
		return nil, errors.New("impersonation profile is required")
	}
	options := []tlsclient.HttpClientOption{
		tlsclient.WithClientProfile(config.Profile.clientProfile),
		tlsclient.WithRandomTLSExtensionOrder(),
		tlsclient.WithTimeoutMilliseconds(timeoutMilliseconds(config.Timeout)),
		tlsclient.WithTransportOptions(&tlsclient.TransportOptions{RootCAs: config.RootCAs}),
	}
	if config.Proxy != "" {
		options = append(options, tlsclient.WithProxyUrl(config.Proxy))
	}
	if config.Jar != nil {
		options = append(options, tlsclient.WithCookieJar(cookieJarAdapter{jar: config.Jar}))
	}
	client, err := tlsclient.NewHttpClient(tlsclient.NewNoopLogger(), options...)
	if err != nil {
		return nil, err
	}
	return &Client{profile: config.Profile, client: client}, nil
}

func (client *Client) Do(request *http.Request) (*http.Response, error) {
	converted, err := toFingerprintRequest(request, client.profile)
	if err != nil {
		return nil, err
	}
	response, err := client.client.Do(converted)
	if err != nil {
		return nil, err
	}
	return toStandardResponse(request, response), nil
}

func (client *Client) CloseIdleConnections() { client.client.CloseIdleConnections() }

func toFingerprintRequest(request *http.Request, profile Profile) (*fhttp.Request, error) {
	var body io.Reader
	if request.Body != nil {
		body = request.Body
	}
	converted, err := fhttp.NewRequestWithContext(request.Context(), request.Method, request.URL.String(), body)
	if err != nil {
		return nil, err
	}
	converted.Header = make(fhttp.Header, len(request.Header)+2)
	for key, values := range request.Header {
		converted.Header[key] = append([]string(nil), values...)
	}
	for key, values := range profile.Headers {
		if converted.Header.Values(key) == nil {
			converted.Header[key] = append([]string(nil), values...)
		}
	}
	if converted.Header.Get("User-Agent") == "" {
		converted.Header.Set("User-Agent", profile.UserAgent)
	}
	converted.Header[fhttp.HeaderOrderKey] = append([]string(nil), profile.HeaderOrder...)
	converted.Host = request.Host
	converted.ContentLength = request.ContentLength
	converted.TransferEncoding = append([]string(nil), request.TransferEncoding...)
	converted.Close = request.Close
	converted.GetBody = request.GetBody
	converted.Trailer = toFingerprintHeader(request.Trailer)
	return converted, nil
}

func toStandardResponse(original *http.Request, response *fhttp.Response) *http.Response {
	request := original.Clone(original.Context())
	if response.Request != nil {
		request.Method = response.Request.Method
		request.URL = response.Request.URL
		request.Host = response.Request.Host
		request.Header = toStandardHeader(response.Request.Header)
	}
	return &http.Response{
		Status: response.Status, StatusCode: response.StatusCode,
		Proto: response.Proto, ProtoMajor: response.ProtoMajor, ProtoMinor: response.ProtoMinor,
		Header: toStandardHeader(response.Header), Body: response.Body,
		ContentLength: response.ContentLength, TransferEncoding: append([]string(nil), response.TransferEncoding...),
		Close: response.Close, Uncompressed: response.Uncompressed, Trailer: toStandardHeader(response.Trailer),
		Request: request,
	}
}

func toFingerprintHeader(header http.Header) fhttp.Header {
	converted := make(fhttp.Header, len(header))
	for key, values := range header {
		converted[key] = append([]string(nil), values...)
	}
	return converted
}

func toStandardHeader(header fhttp.Header) http.Header {
	converted := make(http.Header, len(header))
	for key, values := range header {
		if key == fhttp.HeaderOrderKey || key == fhttp.PHeaderOrderKey {
			continue
		}
		converted[key] = append([]string(nil), values...)
	}
	return converted
}

type cookieJarAdapter struct{ jar http.CookieJar }

func (adapter cookieJarAdapter) SetCookies(target *url.URL, cookies []*fhttp.Cookie) {
	converted := make([]*http.Cookie, len(cookies))
	for index, cookie := range cookies {
		converted[index] = toStandardCookie(cookie)
	}
	adapter.jar.SetCookies(target, converted)
}

func (adapter cookieJarAdapter) Cookies(target *url.URL) []*fhttp.Cookie {
	cookies := adapter.jar.Cookies(target)
	converted := make([]*fhttp.Cookie, len(cookies))
	for index, cookie := range cookies {
		converted[index] = toFingerprintCookie(cookie)
	}
	return converted
}

func toStandardCookie(cookie *fhttp.Cookie) *http.Cookie {
	return &http.Cookie{
		Name: cookie.Name, Value: cookie.Value, Path: cookie.Path, Domain: cookie.Domain,
		Expires: cookie.Expires, RawExpires: cookie.RawExpires, MaxAge: cookie.MaxAge,
		Secure: cookie.Secure, HttpOnly: cookie.HttpOnly, SameSite: http.SameSite(cookie.SameSite),
		Raw: cookie.Raw, Unparsed: append([]string(nil), cookie.Unparsed...),
	}
}

func toFingerprintCookie(cookie *http.Cookie) *fhttp.Cookie {
	return &fhttp.Cookie{
		Name: cookie.Name, Value: cookie.Value, Path: cookie.Path, Domain: cookie.Domain,
		Expires: cookie.Expires, RawExpires: cookie.RawExpires, MaxAge: cookie.MaxAge,
		Secure: cookie.Secure, HttpOnly: cookie.HttpOnly, SameSite: fhttp.SameSite(cookie.SameSite),
		Raw: cookie.Raw, Unparsed: append([]string(nil), cookie.Unparsed...),
	}
}

func timeoutMilliseconds(timeout time.Duration) int {
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	milliseconds := timeout.Milliseconds()
	maximum := int64(^uint(0) >> 1)
	if milliseconds > maximum {
		milliseconds = maximum
	}
	if milliseconds < 1 {
		milliseconds = 1
	}
	return int(milliseconds)
}

var _ interface {
	Do(*http.Request) (*http.Response, error)
} = (*Client)(nil)
