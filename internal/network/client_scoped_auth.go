package network

import (
	"context"
	"errors"
	"net/http"
)

// DoWithScopedAuthorizationNoRedirect preserves only the request's explicit
// Authorization header while excluding operation/default credentials, cookies,
// response-cookie persistence, and redirects.
func (client *Client) DoWithScopedAuthorizationNoRedirect(ctx context.Context, request *http.Request) (*http.Response, error) {
	if request == nil {
		return nil, errors.New("HTTP request must not be nil")
	}
	explicitAuthorization := request.Header.Values("Authorization")
	cloned := client.prepareRequest(ctx, request, false, true)
	for _, key := range []string{"Authorization", "Cookie", "Proxy-Authorization"} {
		cloned.Header.Del(key)
	}
	if len(explicitAuthorization) == 1 && explicitAuthorization[0] != "" {
		cloned.Header.Set("Authorization", explicitAuthorization[0])
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
