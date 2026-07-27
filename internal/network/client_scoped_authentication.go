package network

import (
	"context"
	"errors"
	"net/http"
)

// DoWithScopedAuthenticationNoRedirect preserves only the request's explicit
// Authentication and Referer headers while excluding operation/default
// credentials, cookies, response-cookie persistence, and redirects.
func (client *Client) DoWithScopedAuthenticationNoRedirect(ctx context.Context, request *http.Request) (*http.Response, error) {
	if request == nil {
		return nil, errors.New("HTTP request must not be nil")
	}
	explicitAuthentication := request.Header.Values("Authentication")
	explicitReferer := request.Header.Values("Referer")
	cloned := client.prepareRequest(ctx, request, false, true)
	for _, key := range []string{"Authentication", "Authorization", "Cookie", "Proxy-Authorization", "Referer"} {
		cloned.Header.Del(key)
	}
	if len(explicitAuthentication) == 1 && explicitAuthentication[0] != "" {
		cloned.Header.Set("Authentication", explicitAuthentication[0])
	}
	if len(explicitReferer) == 1 && explicitReferer[0] != "" {
		cloned.Header.Set("Referer", explicitReferer[0])
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
