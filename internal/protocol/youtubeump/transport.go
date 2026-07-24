package youtubeump

import (
	"context"
	"net/http"
)

// IsolatedTransport is the only HTTP entry point SABR may use. Implementations
// must strip credentials, ignore redirect following, and avoid cookie persistence.
type IsolatedTransport interface {
	DoWithoutCredentialsNoRedirect(context.Context, *http.Request) (*http.Response, error)
}
