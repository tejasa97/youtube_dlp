package network

import (
	"context"
	"errors"
	"net/http"
)

// DoWithoutCredentialsNoRedirect executes a native request without operation-jar
// cookies, without credential-bearing default or explicit headers, without
// persisting response cookies, and without following redirects. It is the
// combined boundary required for SABR POSTs to googlevideo endpoints.
func (client *Client) DoWithoutCredentialsNoRedirect(ctx context.Context, request *http.Request) (*http.Response, error) {
	if request == nil {
		return nil, errors.New("HTTP request must not be nil")
	}
	cloned := client.prepareRequest(ctx, request, false, true)
	for _, key := range []string{"Authorization", "Cookie", "Proxy-Authorization"} {
		cloned.Header.Del(key)
	}
	isolated := *client.httpClient
	isolated.Jar = nil
	isolated.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}
	response, err := isolated.Do(cloned)
	if err != nil {
		return nil, &RequestError{Method: cloned.Method, URL: RedactURL(cloned.URL), Err: err}
	}
	return response, nil
}
