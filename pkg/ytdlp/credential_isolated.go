package ytdlp

import (
	"context"
	"io"
	"net/http"

	"github.com/ytdlp-go/ytdlp/internal/network"
)

// credentialIsolatedTransport delegates to DoWithoutCredentialsNoRedirect after
// stripping ambient Referer. The network client removes cookies,
// Authorization, Proxy-Authorization, redirect following, and the cookie jar.
type credentialIsolatedTransport struct {
	ambient *network.Client
}

func newCredentialIsolatedTransport(ambient *network.Client) *credentialIsolatedTransport {
	if ambient == nil {
		return nil
	}
	return &credentialIsolatedTransport{ambient: ambient}
}

func (transport *credentialIsolatedTransport) DoWithoutCredentialsNoRedirect(ctx context.Context, request *http.Request) (*http.Response, error) {
	cloned := request.Clone(ctx)
	for _, key := range []string{"Authorization", "Cookie", "Proxy-Authorization", "Referer"} {
		cloned.Header.Del(key)
	}
	return transport.ambient.DoWithoutCredentialsNoRedirect(ctx, cloned)
}

func (transport *credentialIsolatedTransport) Do(ctx context.Context, request *http.Request) (*http.Response, error) {
	return transport.DoWithoutCredentialsNoRedirect(ctx, request)
}

func (transport *credentialIsolatedTransport) ReadPage(ctx context.Context, rawURL string) ([]byte, http.Header, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, nil, err
	}
	response, err := transport.DoWithoutCredentialsNoRedirect(ctx, request)
	if err != nil {
		return nil, nil, err
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, 4<<20))
	if err != nil {
		return nil, nil, err
	}
	return data, response.Header.Clone(), nil
}

func (operation *operation) mediaTransport(credentialIsolated bool) any {
	if !credentialIsolated {
		return operation.transport
	}
	return newCredentialIsolatedTransport(operation.transport)
}
