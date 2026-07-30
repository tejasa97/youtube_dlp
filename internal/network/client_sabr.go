package network

import (
	"context"
	"errors"
	"net/http"
)

// DoWithoutCredentialsNoRedirect executes a native request without operation-jar
// cookies, without credential-bearing default or explicit headers, without
// ambient or explicit Referer, without persisting response cookies, and without
// following redirects. It is the combined boundary required for SABR POSTs to
// googlevideo endpoints, hop-by-hop short-link Location validation (for example
// TikTok vm/vt/t), and credential-isolated subtitle/CDN fetches.
func (client *Client) DoWithoutCredentialsNoRedirect(ctx context.Context, request *http.Request) (*http.Response, error) {
	return client.doWithoutCredentialsNoRedirect(ctx, request, false)
}

// DoWithoutCredentialsNoRedirectWithReferer preserves only an explicit,
// extractor-validated Referer while retaining credential and redirect
// isolation. Callers must validate the Referer before using this boundary.
func (client *Client) DoWithoutCredentialsNoRedirectWithReferer(ctx context.Context, request *http.Request) (*http.Response, error) {
	return client.doWithoutCredentialsNoRedirect(ctx, request, true)
}

func (client *Client) doWithoutCredentialsNoRedirect(ctx context.Context, request *http.Request, preserveReferer bool) (*http.Response, error) {
	if request == nil {
		return nil, errors.New("HTTP request must not be nil")
	}
	cloned := client.prepareRequest(ctx, request, false, true)
	referer := request.Header.Get("Referer")
	for _, key := range []string{"Authorization", "Cookie", "Proxy-Authorization", "Referer"} {
		cloned.Header.Del(key)
	}
	if preserveReferer && referer != "" {
		cloned.Header.Set("Referer", referer)
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
