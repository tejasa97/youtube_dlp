package ytdlp

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/ytdlp-go/ytdlp/internal/extractor"
	"github.com/ytdlp-go/ytdlp/internal/network"
)

// credentialIsolatedTransport delegates to DoWithoutCredentialsNoRedirect after
// stripping ambient Referer. The network client removes cookies,
// Authorization, Proxy-Authorization, redirect following, and the cookie jar.
type credentialIsolatedTransport struct {
	ambient *network.Client
	referer string
}

func newCredentialIsolatedTransport(ambient *network.Client) *credentialIsolatedTransport {
	if ambient == nil {
		return nil
	}
	return &credentialIsolatedTransport{ambient: ambient}
}

func validCredentialIsolatedMediaReferer(raw string) bool {
	if raw == "" || len(raw) > 2048 || strings.ContainsAny(raw, "\x00\r\n") {
		return false
	}
	parsed, err := url.Parse(raw)
	return err == nil && parsed.Scheme == "https" && parsed.Opaque == "" && parsed.User == nil && parsed.Port() == "" && parsed.Fragment == "" && parsed.Hostname() != ""
}

func newCredentialIsolatedTransportWithReferer(ambient *network.Client, referer string) (*credentialIsolatedTransport, error) {
	if referer != "" && !validCredentialIsolatedMediaReferer(referer) {
		return nil, fmt.Errorf("%w: invalid scoped media referer", extractor.ErrTransportIsolation)
	}
	if ambient == nil {
		return nil, nil
	}
	return &credentialIsolatedTransport{ambient: ambient, referer: referer}, nil
}

func (transport *credentialIsolatedTransport) DoWithoutCredentialsNoRedirect(ctx context.Context, request *http.Request) (*http.Response, error) {
	cloned := request.Clone(ctx)
	for _, key := range []string{"Authorization", "Cookie", "Proxy-Authorization", "Referer"} {
		cloned.Header.Del(key)
	}
	if transport.referer != "" {
		cloned.Header.Set("Referer", transport.referer)
	}
	if transport.referer != "" {
		return transport.ambient.DoWithoutCredentialsNoRedirectWithReferer(ctx, cloned)
	}
	return transport.ambient.DoWithoutCredentialsNoRedirect(ctx, cloned)
}

func (transport *credentialIsolatedTransport) DoWithoutCredentialsNoRedirectWithReferer(ctx context.Context, request *http.Request) (*http.Response, error) {
	return transport.DoWithoutCredentialsNoRedirect(ctx, request)
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

func (operation *operation) mediaTransport(credentialIsolated bool, referer string) (any, error) {
	if !credentialIsolated {
		if referer != "" {
			return nil, fmt.Errorf("%w: scoped referer requires credential isolation", extractor.ErrTransportIsolation)
		}
		return operation.transport, nil
	}
	return newCredentialIsolatedTransportWithReferer(operation.transport, referer)
}
