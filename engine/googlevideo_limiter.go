package engine

import (
	"context"
	"net/url"
	"strings"
)

func isGoogleVideoMediaURL(rawURL string) bool {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return false
	}
	host := strings.ToLower(parsed.Hostname())
	return host == "googlevideo.com" || strings.HasSuffix(host, ".googlevideo.com")
}

// googleVideoTransfers is process-wide because applications may create one
// engine client per job. GVS sees those jobs as concurrent request streams
// regardless of which Client authored them.
var googleVideoTransfers = make(chan struct{}, 1)

func (client *Client) acquireGoogleVideoTransfer(ctx context.Context, rawURL string) (func(), error) {
	if !isGoogleVideoMediaURL(rawURL) {
		return func() {}, nil
	}
	select {
	case googleVideoTransfers <- struct{}{}:
		return func() { <-googleVideoTransfers }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (operation *operation) acquireGoogleVideoTransfer(ctx context.Context, rawURL string) (func(), error) {
	if operation == nil {
		return func() {}, nil
	}
	return operation.client.acquireGoogleVideoTransfer(ctx, rawURL)
}
