package sponsorblock

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
)

// fakeTransport is the in-process recorder used by the client tests.
// It implements DoWithoutCredentials so credential isolation is exercised.
type fakeTransport struct {
	method      string
	url         string
	headers     http.Header
	noCookies   bool
	cookieValue string

	status      int
	contentType string

	calls atomic.Int32
}

func (transport *fakeTransport) Do(ctx context.Context, request *http.Request) (*http.Response, error) {
	transport.calls.Add(1)
	transport.method = request.Method
	transport.url = request.URL.String()
	transport.headers = request.Header.Clone()
	transport.cookieValue = request.Header.Get("Cookie")
	return transport.respond(), nil
}

func (transport *fakeTransport) DoWithoutCredentials(ctx context.Context, request *http.Request) (*http.Response, error) {
	transport.calls.Add(1)
	transport.noCookies = true
	transport.method = request.Method
	transport.url = request.URL.String()
	transport.headers = request.Header.Clone()
	transport.cookieValue = request.Header.Get("Cookie")
	return transport.respond(), nil
}

func (transport *fakeTransport) respond() *http.Response {
	header := http.Header{}
	if transport.contentType != "" {
		header.Set("Content-Type", transport.contentType)
	}
	return &http.Response{
		StatusCode: transport.status,
		Header:     header,
		Body:       http.NoBody,
		Status:     fmt.Sprintf("%d", transport.status),
		Request:    nil,
	}
}

// fakeResponseTransport extends fakeTransport with a real body.
type fakeResponseTransport struct {
	fakeTransport
	body *strings.Reader
}

func (transport *fakeResponseTransport) Do(ctx context.Context, request *http.Request) (*http.Response, error) {
	transport.calls.Add(1)
	transport.method = request.Method
	transport.url = request.URL.String()
	transport.headers = request.Header.Clone()
	transport.cookieValue = request.Header.Get("Cookie")
	return transport.respond(), nil
}

func (transport *fakeResponseTransport) DoWithoutCredentials(ctx context.Context, request *http.Request) (*http.Response, error) {
	transport.calls.Add(1)
	transport.noCookies = true
	transport.method = request.Method
	transport.url = request.URL.String()
	transport.headers = request.Header.Clone()
	transport.cookieValue = request.Header.Get("Cookie")
	return transport.respond(), nil
}

func (transport *fakeResponseTransport) respond() *http.Response {
	header := http.Header{}
	if transport.contentType != "" {
		header.Set("Content-Type", transport.contentType)
	}
	return &http.Response{
		StatusCode: transport.status,
		Header:     header,
		Body:       &readCloser{reader: transport.body},
		Status:     fmt.Sprintf("%d", transport.status),
		Request:    nil,
	}
}

type readCloser struct {
	reader *strings.Reader
}

func (rc *readCloser) Read(p []byte) (int, error) { return rc.reader.Read(p) }
func (rc *readCloser) Close() error               { return nil }
