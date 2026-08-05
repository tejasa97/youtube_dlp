package engine

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strings"

	providerapi "github.com/tejasa97/youtube_dlp/engine/provider"
	mediaformat "github.com/tejasa97/youtube_dlp/internal/format"
	"github.com/tejasa97/youtube_dlp/internal/network"
	"github.com/tejasa97/youtube_dlp/internal/value"
)

type providerPolicyTransport struct {
	ambient *network.Client
	hooks   providerapi.Hooks[compositionState]
	policy  string
	role    string
}

func newProviderPolicyTransport(operation *operation, policy, role string) *providerPolicyTransport {
	if operation == nil || operation.transport == nil || operation.registry == nil {
		return nil
	}
	return &providerPolicyTransport{
		ambient: operation.transport, hooks: operation.registry.Hooks(), policy: policy, role: role,
	}
}

func (transport *providerPolicyTransport) validate(rawURL string) error {
	if transport == nil || transport.hooks.ValidateURL == nil {
		return ErrTransportIsolation
	}
	role := transport.role
	if role == "playback" {
		parsed, err := url.Parse(rawURL)
		if err != nil {
			return err
		}
		if strings.EqualFold(path.Ext(parsed.Path), ".m3u8") {
			role = "manifest"
		} else {
			role = "segment"
		}
	}
	return transport.hooks.ValidateURL(providerapi.URLPolicyRequest{Policy: transport.policy, Role: role, URL: rawURL})
}

func (transport *providerPolicyTransport) DoWithoutCredentialsNoRedirect(ctx context.Context, request *http.Request) (*http.Response, error) {
	if request == nil || request.URL == nil {
		return nil, ErrTransportIsolation
	}
	if err := transport.validate(request.URL.String()); err != nil {
		return nil, err
	}
	cloned := request.Clone(ctx)
	for _, key := range []string{"Authorization", "Cookie", "Proxy-Authorization", "Referer"} {
		cloned.Header.Del(key)
	}
	response, err := transport.ambient.DoWithoutCredentialsNoRedirect(ctx, cloned)
	if err != nil {
		return nil, err
	}
	if transport.hooks.ValidateResponse != nil {
		status := 0
		hasBody := response != nil && response.Body != nil
		if response != nil {
			status = response.StatusCode
		}
		if err := transport.hooks.ValidateResponse(providerapi.PolicyResponseRequest{
			Policy: transport.policy, Status: status, HasBody: hasBody,
		}); err != nil {
			if hasBody {
				_ = response.Body.Close()
			}
			return nil, err
		}
	}
	return response, nil
}

func (transport *providerPolicyTransport) Do(ctx context.Context, request *http.Request) (*http.Response, error) {
	return transport.DoWithoutCredentialsNoRedirect(ctx, request)
}

func (transport *providerPolicyTransport) ReadPage(ctx context.Context, rawURL string) ([]byte, http.Header, error) {
	if err := transport.validate(rawURL); err != nil {
		return nil, nil, err
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
		if transport.hooks.NetworkError != nil {
			return nil, nil, transport.hooks.NetworkError(transport.policy)
		}
		return nil, nil, ErrUnavailable
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		if transport.hooks.StatusError != nil {
			return nil, nil, transport.hooks.StatusError(providerapi.StatusErrorRequest{Policy: transport.policy, Status: response.StatusCode})
		}
		return nil, nil, &HTTPStatusError{Code: response.StatusCode}
	}
	const maxReadPageBytes = 8 << 20
	body, err := io.ReadAll(io.LimitReader(response.Body, maxReadPageBytes+1))
	if err != nil {
		if transport.hooks.NetworkError != nil {
			return nil, nil, transport.hooks.NetworkError(transport.policy)
		}
		return nil, nil, err
	}
	if len(body) > maxReadPageBytes {
		return nil, nil, ErrJSONResponseTooLarge
	}
	return body, response.Header.Clone(), nil
}

func providerHostPolicy(object *value.Object) string {
	if object == nil {
		return ""
	}
	policy, _ := object.Lookup("_host_policy").StringValue()
	return policy
}

func validateHostPolicyDispatch(operation *operation, selections []mediaformat.Selection) error {
	for _, selected := range selections {
		if selected.HostPolicy == "" {
			continue
		}
		if operation == nil || operation.registry == nil || operation.registry.Hooks().ValidatePolicy == nil {
			return ErrUnavailable
		}
		if err := operation.registry.Hooks().ValidatePolicy(selected.HostPolicy); err != nil {
			return err
		}
		if !selected.CredentialIsolated {
			return fmt.Errorf("%w: provider host policy requires credential-isolated dispatch", ErrTransportIsolation)
		}
	}
	return nil
}
