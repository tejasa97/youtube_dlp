package impersonate

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net"
	"net/http"
	"net/url"
	"time"

	"github.com/imroc/req/v3"
)

type Config struct {
	Profile         Profile
	Proxy           string
	Timeout         time.Duration
	Jar             http.CookieJar
	RootCAs         *x509.CertPool
	DisableRedirect bool
	SourceAddress   string
}

// Client exposes a browser-fingerprinted transport through the repository's
// standard net/http boundary. The concrete engine remains private so callers
// cannot accidentally depend on engine-specific request types.
type Client struct {
	profile Profile
	client  *req.Client
}

func New(config Config) (*Client, error) {
	if config.Profile.Name == "" {
		return nil, errors.New("impersonation profile is required")
	}
	timeout := compatibleTimeout(config.Timeout)
	dialer, dialNetwork, err := newSourceDialer(timeout, config.SourceAddress)
	if err != nil {
		return nil, err
	}
	client := req.C().
		SetLogger(nil).
		SetTimeout(0).
		SetDial(func(ctx context.Context, _ string, address string) (net.Conn, error) {
			return dialer.DialContext(ctx, dialNetwork, address)
		}).
		SetTLSHandshakeTimeout(timeout)
	client.SetResponseHeaderTimeout(timeout)
	client.SetTLSClientConfig(&tls.Config{
		RootCAs:    config.RootCAs,
		NextProtos: []string{"h2", "http/1.1"},
	})
	config.Profile.apply(client)
	// The previous impersonation stack never consulted HTTP_PROXY or related
	// process environment variables. req defaults to ProxyFromEnvironment, so
	// disable that implicit behavior before applying an explicit product proxy.
	client.SetProxy(nil)

	if config.Proxy != "" {
		proxyURL, err := url.Parse(config.Proxy)
		if err != nil || proxyURL.Scheme == "" || proxyURL.Host == "" {
			return nil, errors.New("invalid impersonation proxy URL")
		}
		dialContext, err := newProxyDialContext(proxyURL, timeout, config.SourceAddress)
		if err != nil {
			return nil, err
		}
		client.SetDial(dialContext)
	}
	client.SetCookieJar(config.Jar)
	if config.DisableRedirect {
		client.SetRedirectPolicy(req.NoRedirectPolicy())
	}
	return &Client{profile: config.Profile, client: client}, nil
}

func newSourceDialer(timeout time.Duration, sourceAddress string) (*net.Dialer, string, error) {
	dialNetwork := "tcp"
	var localAddress net.Addr
	if sourceAddress != "" {
		ip := net.ParseIP(sourceAddress)
		if ip == nil {
			return nil, "", errors.New("invalid source address")
		}
		if v4 := ip.To4(); v4 != nil {
			dialNetwork = "tcp4"
			ip = v4
		} else {
			dialNetwork = "tcp6"
		}
		localAddress = &net.TCPAddr{IP: ip}
	}
	return &net.Dialer{Timeout: timeout, KeepAlive: 30 * time.Second, LocalAddr: localAddress}, dialNetwork, nil
}

func (client *Client) Do(request *http.Request) (*http.Response, error) {
	if request == nil {
		return nil, errors.New("HTTP request must not be nil")
	}
	prepared := request.Clone(request.Context())
	for key, values := range client.profile.Headers {
		if prepared.Header.Values(key) == nil {
			prepared.Header[key] = append([]string(nil), values...)
		}
	}
	if prepared.Header.Get("User-Agent") == "" {
		prepared.Header.Set("User-Agent", client.profile.UserAgent)
	}
	response, err := client.client.Do(prepared)
	if err != nil {
		return nil, err
	}
	return toStandardResponse(request, response), nil
}

func (client *Client) CloseIdleConnections() { client.client.CloseIdleConnections() }

func toStandardResponse(original *http.Request, response *http.Response) *http.Response {
	request := original.Clone(original.Context())
	if response.Request != nil {
		request.Method = response.Request.Method
		request.URL = response.Request.URL
		request.Host = response.Request.Host
		request.Header = response.Request.Header.Clone()
		request.Header.Del(req.HeaderOderKey)
		request.Header.Del(req.PseudoHeaderOderKey)
	}
	// Keep this field-for-field boundary compatible with the former fhttp
	// adapter. In particular, TLS state was not exposed by that adapter.
	return &http.Response{
		Status: response.Status, StatusCode: response.StatusCode,
		Proto: response.Proto, ProtoMajor: response.ProtoMajor, ProtoMinor: response.ProtoMinor,
		Header: response.Header.Clone(), Body: response.Body,
		ContentLength: response.ContentLength, TransferEncoding: append([]string(nil), response.TransferEncoding...),
		Close: response.Close, Uncompressed: response.Uncompressed, Trailer: response.Trailer.Clone(),
		Request: request,
	}
}

func compatibleTimeout(timeout time.Duration) time.Duration {
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	// tls-client's public timeout option accepted integer milliseconds. Retain
	// its truncation, minimum, and platform-int saturation behavior.
	milliseconds := timeout.Milliseconds()
	maximum := int64(^uint(0) >> 1)
	if milliseconds > maximum {
		milliseconds = maximum
	}
	if milliseconds < 1 {
		milliseconds = 1
	}
	return time.Duration(milliseconds) * time.Millisecond
}

var _ interface {
	Do(*http.Request) (*http.Response, error)
} = (*Client)(nil)
