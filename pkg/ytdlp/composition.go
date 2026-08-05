package ytdlp

import (
	"errors"
	"fmt"

	"github.com/ytdlp-go/ytdlp/internal/extractor"
)

var errInvalidProviderComposition = errors.New("invalid provider composition")

// providerCatalog constructs the providers for one operation. Implementations
// must return a new slice and must not share mutable provider state between
// calls. Registry construction remains here so selection policy is always
// operation-local.
type providerCatalog func() []extractor.Extractor

type engineComposition struct {
	catalog providerCatalog
}

func composeProviders(catalog providerCatalog) engineComposition {
	return engineComposition{catalog: catalog}
}

func (composition engineComposition) newRegistry() (*extractor.Registry, error) {
	if composition.catalog == nil {
		return nil, fmt.Errorf("%w: missing provider catalog", errInvalidProviderComposition)
	}
	providers := composition.catalog()
	return extractor.NewRegistry(providers...), nil
}

// broadCompatibilityComposition explicitly appends Client-installed plugins
// to the broad facade's catalog. Focused compositions receive exactly the
// providers returned by their own catalog.
func broadCompatibilityComposition(plugins []*InstalledPlugin, approver PluginPermissionApprover) engineComposition {
	installed := append([]*InstalledPlugin(nil), plugins...)
	return composeProviders(func() []extractor.Extractor {
		return broadCompatibilityProviders(installed, approver)
	})
}

func newClientWithComposition(composition engineComposition, options ...Option) *Client {
	client := &Client{composition: composition}
	for _, option := range options {
		option(client)
	}
	return client
}

// productRegistry retains the package-level native-catalog test and discovery
// seam while making its broad compatibility ownership explicit.
func productRegistry() *extractor.Registry {
	registry, err := broadCompatibilityComposition(nil, nil).newRegistry()
	if err != nil {
		panic(err)
	}
	return registry
}
