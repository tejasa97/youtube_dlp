package extractor

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"sync"
)

type bbcFixtureResponse struct {
	status int
	body   []byte
}

// bbcFixtureTransport serves BBC extractor tests with credential-isolated
// no-redirect semantics. It records request headers so tests can verify that
// isolated calls do not forward ambient credentials.
type bbcFixtureTransport struct {
	mu        sync.Mutex
	responses map[string]bbcFixtureResponse
	pages     map[string][]byte
	handler   func(context.Context, *http.Request) (*http.Response, error)
	requests  []string
	headers   []http.Header
	wait      bool
}

func (transport *bbcFixtureTransport) Do(ctx context.Context, request *http.Request) (*http.Response, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	transport.mu.Lock()
	transport.requests = append(transport.requests, request.Method+" "+request.URL.String())
	transport.headers = append(transport.headers, request.Header.Clone())
	handler := transport.handler
	response, ok := transport.responses[request.Method+" "+request.URL.String()]
	transport.mu.Unlock()
	if transport.wait {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	if handler != nil {
		return handler(ctx, request)
	}
	if !ok {
		return bbcHTTPResponse(http.StatusNotFound, nil), nil
	}
	status := response.status
	if status == 0 {
		status = http.StatusOK
	}
	return bbcHTTPResponse(status, response.body), nil
}

func (transport *bbcFixtureTransport) ReadPage(ctx context.Context, rawURL string) ([]byte, http.Header, error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	transport.mu.Lock()
	transport.requests = append(transport.requests, "GET "+rawURL)
	body, ok := transport.pages[rawURL]
	transport.mu.Unlock()
	if transport.wait {
		<-ctx.Done()
		return nil, nil, ctx.Err()
	}
	if !ok {
		return nil, nil, &HTTPStatusError{Code: http.StatusNotFound}
	}
	return append([]byte(nil), body...), make(http.Header), nil
}

func (transport *bbcFixtureTransport) DoWithoutCredentialsNoRedirect(ctx context.Context, request *http.Request) (*http.Response, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	transport.mu.Lock()
	transport.requests = append(transport.requests, request.Method+" "+request.URL.String())
	transport.headers = append(transport.headers, request.Header.Clone())
	body, ok := transport.pages[request.URL.String()]
	handler := transport.handler
	response, hasResponse := transport.responses[request.Method+" "+request.URL.String()]
	transport.mu.Unlock()
	if transport.wait {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	if ok {
		return bbcHTTPResponse(http.StatusOK, body), nil
	}
	if hasResponse {
		status := response.status
		if status == 0 {
			status = http.StatusOK
		}
		return bbcHTTPResponse(status, response.body), nil
	}
	if handler != nil {
		return handler(ctx, request)
	}
	return bbcHTTPResponse(http.StatusNotFound, nil), nil
}

func bbcHTTPResponse(status int, body []byte) *http.Response {
	return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(bytes.NewReader(body))}
}

func bbcFixtureHasSensitiveHeader(headers http.Header) bool {
	for _, key := range []string{"Authorization", "Cookie", "Proxy-Authorization"} {
		if headers.Get(key) != "" {
			return true
		}
	}
	return false
}
