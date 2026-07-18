package network

import (
	"context"
	"fmt"
	"io"
	"net/http"
)

// ReadPageWithHeaders performs a bounded page request through a configured
// transport. It exists for manifest protocols whose selected format carries
// required request headers.
func ReadPageWithHeaders(ctx context.Context, transport Doer, rawURL string, headers http.Header, limit int64) ([]byte, http.Header, error) {
	if limit <= 0 {
		return nil, nil, fmt.Errorf("invalid page size limit")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, nil, fmt.Errorf("create page request: %w", err)
	}
	request.Header = headers.Clone()
	response, err := transport.Do(ctx, request)
	if err != nil {
		return nil, nil, err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, response.Header.Clone(), &StatusError{Code: response.StatusCode, URL: RedactURL(response.Request.URL)}
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, limit+1))
	if err != nil {
		return nil, response.Header.Clone(), fmt.Errorf("read page response: %w", err)
	}
	if int64(len(body)) > limit {
		return nil, response.Header.Clone(), fmt.Errorf("%w: limit is %d bytes", ErrPageTooLarge, limit)
	}
	return body, response.Header.Clone(), nil
}
