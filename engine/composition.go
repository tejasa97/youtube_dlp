package engine

import (
	"context"
	"io"

	providerapi "github.com/tejasa97/youtube_dlp/engine/provider"
)

type ClientProviderConfig struct {
	InstalledPlugins         []*InstalledPlugin
	PluginPermissionApprover PluginPermissionApprover
}

type ChallengeSolverFactory func(string) (providerapi.ChallengeSolver, io.Closer, error)

type ProviderHooks struct {
	ChallengeSolverFactory ChallengeSolverFactory
	ClassifyError          func(error) (providerapi.ErrorClass, bool)
	ValidatePolicy         func(string) error
	ValidateURL            func(providerapi.URLPolicyRequest) error
	NetworkError           func(string) error
	StatusError            func(providerapi.StatusErrorRequest) error
	ValidateResponse       func(providerapi.PolicyResponseRequest) error
	ValidateAsset          func(providerapi.URLPolicyRequest) error
	ServiceIdentity        func(providerapi.ServiceRequest) (string, bool)
	Reload                 func(context.Context, providerapi.Operation, Request, providerapi.ReloadRequest) (providerapi.Extraction, error)
}

type compositionState struct {
	Request Request
	Client  ClientProviderConfig
}

type Composition struct {
	bundle providerapi.Bundle[compositionState]
	hooks  ProviderHooks
}

func NewComposition[R providerapi.URLRequest](
	catalog func(ClientProviderConfig) []providerapi.Provider[R],
	adapt func(providerapi.Operation, Request) R,
	hooks ProviderHooks,
) Composition {
	providerHooks := providerapi.Hooks[compositionState]{
		ClassifyError:    hooks.ClassifyError,
		ValidatePolicy:   hooks.ValidatePolicy,
		ValidateURL:      hooks.ValidateURL,
		NetworkError:     hooks.NetworkError,
		StatusError:      hooks.StatusError,
		ValidateResponse: hooks.ValidateResponse,
		ValidateAsset:    hooks.ValidateAsset,
		ServiceIdentity:  hooks.ServiceIdentity,
	}
	if hooks.Reload != nil {
		providerHooks.Reload = func(ctx context.Context, operation providerapi.Operation, state compositionState, request providerapi.ReloadRequest) (providerapi.Extraction, error) {
			return hooks.Reload(ctx, operation, state.Request, request)
		}
	}
	return Composition{
		bundle: providerapi.Compose(
			func(state compositionState) []providerapi.Provider[R] { return catalog(state.Client) },
			func(operation providerapi.Operation, state compositionState) R {
				return adapt(operation, state.Request)
			},
			providerHooks,
		),
		hooks: hooks,
	}
}

func (composition Composition) newRuntime(request Request, config ClientProviderConfig) (providerapi.Runtime[compositionState], error) {
	return composition.bundle.NewRuntime(compositionState{Request: request, Client: config})
}

func (client *Client) compositionState(request Request) compositionState {
	return compositionState{
		Request: request,
		Client: ClientProviderConfig{
			InstalledPlugins: client.plugins, PluginPermissionApprover: client.pluginApprover,
		},
	}
}
