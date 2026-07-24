package sponsorblock

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
)

// errNotFound is a private sentinel used to express pinned 404
// semantics. The error is converted to an empty FetchResult at the
// call site, never returned to external callers.
var errNotFound = errors.New("sponsorblock not found")

// fetchBody executes the canonical request and returns a bounded
// response body. It enforces cookie isolation, context cancellation,
// response size limits, and secret-safe error mapping.
func fetchBody(ctx context.Context, transport Transport, endpoint *url.URL) ([]byte, error) {
	isolated, ok := transport.(cookieIsolated)
	if !ok {
		return nil, errorf(ErrIsolation, "credential isolation unavailable")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return nil, errorf(ErrNetwork, "build request")
	}
	// Defense-in-depth deletion keeps the request object itself free of
	// credentials before the transport applies its stronger isolation.
	request.Header.Del("Cookie")
	request.Header.Del("Authorization")
	request.Header.Set("Accept", "application/json")
	response, err := isolated.DoWithoutCredentials(ctx, request)
	if err != nil {
		if contextErr := ctx.Err(); contextErr != nil {
			return nil, contextErr
		}
		return nil, errorf(ErrNetwork, "transport failure")
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusNotFound {
		// 404 follows pinned no-segment semantics: the
		// operation succeeds with no chapters.
		return nil, errNotFound
	}
	if response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden {
		return nil, errorf(ErrAuthentication, "forbidden response")
	}
	if response.StatusCode == http.StatusTooManyRequests {
		return nil, errorf(ErrNetwork, "rate limited")
	}
	if response.StatusCode >= 500 {
		return nil, errorf(ErrNetwork, "server error")
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, errorf(ErrInvalidMetadata, "unexpected status")
	}
	// ContentLength is advisory only; the limit reader is the
	// authoritative gate.
	reader := io.LimitReader(response.Body, MaxResponseBytes+1)
	body, err := io.ReadAll(reader)
	if err != nil {
		if contextErr := ctx.Err(); contextErr != nil {
			return nil, contextErr
		}
		return nil, errorf(ErrNetwork, "read body")
	}
	if len(body) > MaxResponseBytes {
		return nil, errorf(ErrInvalidMetadata, "response too large")
	}
	return body, nil
}
