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

func (client *Client) acquireGoogleVideoTransfer(ctx context.Context, rawURL string) (func(), error) {
	if client == nil || client.gvsTransfers == nil || !isGoogleVideoMediaURL(rawURL) {
		return func() {}, nil
	}
	select {
	case client.gvsTransfers <- struct{}{}:
		return func() { <-client.gvsTransfers }, nil
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
