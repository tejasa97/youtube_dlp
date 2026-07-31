package ytdlp

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strings"

	"github.com/ytdlp-go/ytdlp/internal/extractor"
	mediaformat "github.com/ytdlp-go/ytdlp/internal/format"
	"github.com/ytdlp-go/ytdlp/internal/network"
	"github.com/ytdlp-go/ytdlp/internal/value"
)

type tedCredentialIsolatedTransport struct {
	ambient *network.Client
	role    string
}

type dailymotionCredentialIsolatedTransport struct {
	ambient *network.Client
	role    string
}

func newDailymotionCredentialIsolatedTransport(ambient *network.Client, role string) *dailymotionCredentialIsolatedTransport {
	if ambient == nil {
		return nil
	}
	return &dailymotionCredentialIsolatedTransport{ambient: ambient, role: role}
}

func (transport *dailymotionCredentialIsolatedTransport) allowed(rawURL string) bool {
	if transport == nil {
		return false
	}
	role := transport.role
	if role == "playback" {
		parsed, err := url.Parse(rawURL)
		if err != nil {
			return false
		}
		if strings.EqualFold(path.Ext(parsed.Path), ".m3u8") {
			role = "manifest"
		} else {
			role = "segment"
		}
	}
	return extractor.DailymotionAttributableURL(rawURL, role)
}

func (transport *dailymotionCredentialIsolatedTransport) DoWithoutCredentialsNoRedirect(ctx context.Context, request *http.Request) (*http.Response, error) {
	if request == nil || request.URL == nil || !transport.allowed(request.URL.String()) {
		return nil, fmt.Errorf("%w: Dailymotion attributable host policy", extractor.ErrUnavailable)
	}
	cloned := request.Clone(ctx)
	for _, key := range []string{"Authorization", "Cookie", "Proxy-Authorization", "Referer"} {
		cloned.Header.Del(key)
	}
	return transport.ambient.DoWithoutCredentialsNoRedirect(ctx, cloned)
}

func (transport *dailymotionCredentialIsolatedTransport) Do(ctx context.Context, request *http.Request) (*http.Response, error) {
	return transport.DoWithoutCredentialsNoRedirect(ctx, request)
}

func (transport *dailymotionCredentialIsolatedTransport) ReadPage(ctx context.Context, rawURL string) ([]byte, http.Header, error) {
	if !transport.allowed(rawURL) {
		return nil, nil, fmt.Errorf("%w: Dailymotion attributable host policy", extractor.ErrUnavailable)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, nil, err
	}
	response, err := transport.DoWithoutCredentialsNoRedirect(ctx, request)
	if err != nil {
		return nil, nil, err
	}
	if response == nil || response.Body == nil {
		return nil, nil, extractor.ErrDailymotionNetwork
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return nil, nil, extractor.DailymotionStatusError(response.StatusCode)
	}
	const maxDailymotionReadPageBytes = 8 << 20
	body, err := io.ReadAll(io.LimitReader(response.Body, maxDailymotionReadPageBytes+1))
	if err != nil {
		return nil, nil, extractor.ErrDailymotionNetwork
	}
	if len(body) > maxDailymotionReadPageBytes {
		return nil, nil, extractor.ErrJSONResponseTooLarge
	}
	return body, response.Header.Clone(), nil
}

func extractorHostPolicy(object *value.Object) string {
	if object == nil {
		return ""
	}
	policy, _ := object.Lookup("_host_policy").StringValue()
	if policy == "" {
		policy, _ = object.Lookup("_ted_host_policy").StringValue()
	}
	return policy
}

func newTedCredentialIsolatedTransport(ambient *network.Client, role string) *tedCredentialIsolatedTransport {
	if ambient == nil {
		return nil
	}
	return &tedCredentialIsolatedTransport{ambient: ambient, role: role}
}

func (transport *tedCredentialIsolatedTransport) allowed(rawURL string) bool {
	if transport == nil {
		return false
	}
	role := transport.role
	if role == "playback" {
		parsed, err := url.Parse(rawURL)
		if err != nil {
			return false
		}
		// HLS manifests and variant playlists retain the manifest allowlist;
		// every other playback request is an HLS segment. Direct media uses the
		// separate media role, so the HLS boundary cannot broaden it.
		if strings.EqualFold(path.Ext(parsed.Path), ".m3u8") {
			role = "manifest"
		} else {
			role = "segment"
		}
	}
	return extractor.TedAttributableURL(rawURL, role)
}

func validateHostPolicyDispatch(selections []mediaformat.Selection) error {
	for _, selected := range selections {
		switch selected.HostPolicy {
		case "":
			continue
		case "ted":
			if selected.CredentialIsolated {
				continue
			}
			return fmt.Errorf("%w: TED host policy requires credential-isolated dispatch", extractor.ErrTransportIsolation)
		case "dailymotion":
			if selected.CredentialIsolated {
				continue
			}
			return fmt.Errorf("%w: Dailymotion host policy requires credential-isolated dispatch", extractor.ErrTransportIsolation)
		default:
			return fmt.Errorf("%w: unknown host policy", extractor.ErrUnavailable)
		}
	}
	return nil
}

func (transport *tedCredentialIsolatedTransport) DoWithoutCredentialsNoRedirect(ctx context.Context, request *http.Request) (*http.Response, error) {
	if request == nil || request.URL == nil || !transport.allowed(request.URL.String()) {
		return nil, fmt.Errorf("%w: TED attributable host policy", extractor.ErrUnavailable)
	}
	cloned := request.Clone(ctx)
	for _, key := range []string{"Authorization", "Cookie", "Proxy-Authorization", "Referer"} {
		cloned.Header.Del(key)
	}
	return transport.ambient.DoWithoutCredentialsNoRedirect(ctx, cloned)
}

func (transport *tedCredentialIsolatedTransport) Do(ctx context.Context, request *http.Request) (*http.Response, error) {
	return transport.DoWithoutCredentialsNoRedirect(ctx, request)
}

func (transport *tedCredentialIsolatedTransport) ReadPage(ctx context.Context, rawURL string) ([]byte, http.Header, error) {
	if !transport.allowed(rawURL) {
		return nil, nil, fmt.Errorf("%w: TED attributable host policy", extractor.ErrUnavailable)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, nil, err
	}
	response, err := transport.DoWithoutCredentialsNoRedirect(ctx, request)
	if err != nil {
		return nil, nil, err
	}
	if response == nil || response.Body == nil {
		return nil, nil, fmt.Errorf("%w: empty TED response", extractor.ErrTedNetwork)
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return nil, nil, extractor.TedStatusError(response.StatusCode)
	}
	const maxTEDReadPageBytes = 8 << 20
	body, err := io.ReadAll(io.LimitReader(response.Body, maxTEDReadPageBytes+1))
	if err != nil {
		return nil, nil, err
	}
	if len(body) > maxTEDReadPageBytes {
		return nil, nil, extractor.ErrJSONResponseTooLarge
	}
	return body, response.Header.Clone(), nil
}

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

func (operation *operation) mediaTransport(credentialIsolated bool, referer, hostPolicy, protocol string) (any, error) {
	if hostPolicy != "" {
		if referer != "" {
			return nil, fmt.Errorf("%w: scoped referer cannot be combined with host policy", extractor.ErrTransportIsolation)
		}
		if hostPolicy == "ted" && credentialIsolated {
			role := "media"
			if protocol == "m3u8_native" {
				role = "playback"
			}
			return newTedCredentialIsolatedTransport(operation.transport, role), nil
		}
		if hostPolicy == "dailymotion" && credentialIsolated {
			role := "media"
			if protocol == "m3u8_native" {
				role = "playback"
			}
			return newDailymotionCredentialIsolatedTransport(operation.transport, role), nil
		}
		return nil, fmt.Errorf("%w: unknown or inconsistent host policy", extractor.ErrTransportIsolation)
	}
	if !credentialIsolated {
		if referer != "" {
			return nil, fmt.Errorf("%w: scoped referer requires credential isolation", extractor.ErrTransportIsolation)
		}
		return operation.transport, nil
	}
	return newCredentialIsolatedTransportWithReferer(operation.transport, referer)
}
