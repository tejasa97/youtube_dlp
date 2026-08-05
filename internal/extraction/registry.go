package extraction

import (
	"context"
	"fmt"
	"net/url"
	"strings"
)

// URLRequest is the neutral minimum needed by Registry.Extract. Composition
// request types retain control of every other field.
type URLRequest interface {
	ExtractionURL() string
}

type Registry[R URLRequest] struct {
	providers []Provider[R]
	selection selectionPolicy
}

func NewRegistry[R URLRequest](providers ...Provider[R]) *Registry[R] {
	return &Registry[R]{providers: append([]Provider[R](nil), providers...)}
}

// Names returns provider identifiers in deterministic routing-priority order.
func (registry *Registry[R]) Names() []string {
	if registry == nil {
		return nil
	}
	names := make([]string, 0, len(registry.providers))
	for _, candidate := range registry.providers {
		if candidate != nil {
			names = append(names, candidate.Name())
		}
	}
	return names
}

func (registry *Registry[R]) ConfigureSelection(rules []string) error {
	if registry == nil {
		return fmt.Errorf("%w: nil registry", ErrInvalidSelection)
	}
	compiled, err := compileSelectionPolicy(rules, registry.providers)
	if err != nil {
		return err
	}
	registry.selection = compiled
	return nil
}

func (registry *Registry[R]) selectionOrder() []string {
	if registry == nil {
		return nil
	}
	if !registry.selection.configured {
		order := make([]string, 0, len(registry.providers))
		for _, candidate := range registry.providers {
			if candidate != nil {
				order = append(order, candidate.Name())
			}
		}
		return order
	}
	return registry.selection.ordered
}

// Select returns the first suitable provider, making registration order the
// explicit and deterministic priority rule.
func (registry *Registry[R]) Select(rawURL string) (Provider[R], error) {
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" && parsed.Opaque == "" {
		return nil, fmt.Errorf("%w: invalid URL", ErrUnsupported)
	}
	for _, name := range registry.selectionOrder() {
		if name == selectionEndSentinel {
			return nil, fmt.Errorf("%w: selection ended", ErrUnsupported)
		}
		for _, candidate := range registry.providers {
			if candidate != nil && strings.EqualFold(candidate.Name(), name) && candidate.Suitable(parsed) {
				return candidate, nil
			}
		}
	}
	return nil, fmt.Errorf("%w: no registered extractor", ErrUnsupported)
}

// SelectFor honors an explicit URL-result provider key. It never silently
// falls back when the producer requested an unknown provider.
func (registry *Registry[R]) SelectFor(rawURL, providerKey string) (Provider[R], error) {
	if providerKey == "" {
		return registry.Select(rawURL)
	}
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" && parsed.Opaque == "" {
		return nil, fmt.Errorf("%w: invalid URL result", ErrUnsupported)
	}
	for _, candidate := range registry.providers {
		if candidate == nil || !strings.EqualFold(candidate.Name(), providerKey) {
			continue
		}
		if registry.selection.configured {
			if _, allowed := registry.selection.allowed[strings.ToLower(candidate.Name())]; !allowed {
				if _, explicitOnly := candidate.(ExplicitOnlyProvider); !explicitOnly {
					return nil, fmt.Errorf("%w: %w: %s", ErrUnsupported, ErrSelectionDisabled, providerKey)
				}
			}
		}
		if strings.EqualFold(candidate.Name(), "generic") && !candidate.Suitable(parsed) {
			return nil, fmt.Errorf("%w: generic URL", ErrUnsupported)
		}
		if parsed.Host == "" && !candidate.Suitable(parsed) {
			return nil, fmt.Errorf("%w: invalid opaque URL result", ErrUnsupported)
		}
		return candidate, nil
	}
	return nil, fmt.Errorf("%w: unknown extractor key", ErrUnsupported)
}

func (registry *Registry[R]) SearchPrefix(prefix string) (SearchPrefixProvider[R], bool) {
	if registry == nil {
		return nil, false
	}
	for _, candidate := range registry.providers {
		search, ok := candidate.(SearchPrefixProvider[R])
		if ok && search.SupportsSearchPrefix(prefix) {
			return search, true
		}
	}
	return nil, false
}

func (registry *Registry[R]) Extract(ctx context.Context, request R) (Extraction, string, error) {
	selected, err := registry.Select(request.ExtractionURL())
	if err != nil {
		return Extraction{}, "", err
	}
	result, err := selected.Extract(ctx, request)
	if err != nil {
		return Extraction{}, selected.Name(), fmt.Errorf("%s extractor: %w", selected.Name(), err)
	}
	return result, selected.Name(), nil
}
