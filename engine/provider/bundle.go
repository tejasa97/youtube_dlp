package provider

import (
	"context"
	"errors"
	"fmt"
	"net/url"
)

var ErrInvalidBundle = errors.New("invalid provider bundle")

// Operation is the provider-neutral state assembled for one extraction call.
// A composition-owned adapter combines it with typed configuration C.
type Operation struct {
	Request         Request
	ChallengeSolver ChallengeSolver
	POTResolver     POTResolver
}

// ErrorClass is a provider-neutral product error classification supplied by a
// bundle when concrete provider sentinels need compatibility mapping.
type ErrorClass string

const (
	ErrorAuthentication ErrorClass = "authentication"
	ErrorInvalidInput   ErrorClass = "invalid_input"
	ErrorNetwork        ErrorClass = "network"
	ErrorSecurity       ErrorClass = "security"
	ErrorUnsupported    ErrorClass = "unsupported"
	ErrorInternal       ErrorClass = "internal"
)

type URLPolicyRequest struct {
	Policy string
	Role   string
	URL    string
}

type StatusErrorRequest struct {
	Policy string
	Status int
}

// ReloadRequest carries bounded playback state for an attributable provider
// reload. Bundles that do not support reload leave Hooks.Reload nil.
type ReloadRequest struct {
	MediaID         string
	VisitorData     string
	WebpageURL      string
	Token           string
	ClientName      string
	ClientID        string
	ClientVersion   string
	UserAgent       string
	DurationSeconds int64
}

// Hooks contains the typed provider-specific support deliberately supplied by
// a composition root. Engine orchestration never switches on provider names.
type Hooks[C any] struct {
	ClassifyError func(error) (ErrorClass, bool)
	ValidateURL   func(URLPolicyRequest) error
	NetworkError  func(string) error
	StatusError   func(StatusErrorRequest) error
	ValidateAsset func(URLPolicyRequest) error
	Reload        func(context.Context, Operation, C, ReloadRequest) (Extraction, error)
}

type Selected[C any] interface {
	Name() string
	Suitable(string) bool
	RetrySafe() bool
	Extract(context.Context, Operation, C) (Extraction, error)
}

type SearchSelected[C any] interface {
	Selected[C]
	SearchQueryAllowed(string) bool
}

type Runtime[C any] interface {
	ConfigureSelection([]string) error
	Names() []string
	Select(string) (Selected[C], error)
	SelectFor(string, string) (Selected[C], error)
	SearchPrefix(string) (SearchSelected[C], bool)
	Hooks() Hooks[C]
}

// Bundle is an immutable typed provider composition. Registry and selection
// state are constructed per operation by NewRuntime.
type Bundle[C any] struct {
	newRuntime func(C) (Runtime[C], error)
}

func Compose[R URLRequest, C any](catalog func(C) []Provider[R], adapt func(Operation, C) R, hooks Hooks[C]) Bundle[C] {
	return Bundle[C]{newRuntime: func(configuration C) (Runtime[C], error) {
		if catalog == nil || adapt == nil {
			return nil, fmt.Errorf("%w: missing catalog or request adapter", ErrInvalidBundle)
		}
		return &typedRuntime[R, C]{
			registry: NewRegistry(catalog(configuration)...), adapt: adapt, hooks: hooks,
		}, nil
	}}
}

func (bundle Bundle[C]) NewRuntime(configuration C) (Runtime[C], error) {
	if bundle.newRuntime == nil {
		return nil, fmt.Errorf("%w: missing composition", ErrInvalidBundle)
	}
	return bundle.newRuntime(configuration)
}

type typedRuntime[R URLRequest, C any] struct {
	registry *Registry[R]
	adapt    func(Operation, C) R
	hooks    Hooks[C]
}

func (runtime *typedRuntime[R, C]) ConfigureSelection(rules []string) error {
	return runtime.registry.ConfigureSelection(rules)
}
func (runtime *typedRuntime[R, C]) Names() []string { return runtime.registry.Names() }
func (runtime *typedRuntime[R, C]) Hooks() Hooks[C] { return runtime.hooks }

func (runtime *typedRuntime[R, C]) Select(rawURL string) (Selected[C], error) {
	selected, err := runtime.registry.Select(rawURL)
	if err != nil {
		return nil, err
	}
	return typedSelected[R, C]{provider: selected, adapt: runtime.adapt}, nil
}

func (runtime *typedRuntime[R, C]) SelectFor(rawURL, key string) (Selected[C], error) {
	selected, err := runtime.registry.SelectFor(rawURL, key)
	if err != nil {
		return nil, err
	}
	return typedSelected[R, C]{provider: selected, adapt: runtime.adapt}, nil
}

func (runtime *typedRuntime[R, C]) SearchPrefix(prefix string) (SearchSelected[C], bool) {
	selected, ok := runtime.registry.SearchPrefix(prefix)
	if !ok {
		return nil, false
	}
	return typedSearchSelected[R, C]{provider: selected, adapt: runtime.adapt}, true
}

type typedSelected[R URLRequest, C any] struct {
	provider Provider[R]
	adapt    func(Operation, C) R
}

func (selected typedSelected[R, C]) Name() string { return selected.provider.Name() }
func (selected typedSelected[R, C]) Suitable(rawURL string) bool {
	parsed, err := url.Parse(rawURL)
	return err == nil && selected.provider.Suitable(parsed)
}
func (selected typedSelected[R, C]) RetrySafe() bool {
	_, ok := selected.provider.(RetrySafeProvider[R])
	return ok
}
func (selected typedSelected[R, C]) Extract(ctx context.Context, operation Operation, configuration C) (Extraction, error) {
	return selected.provider.Extract(ctx, selected.adapt(operation, configuration))
}

type typedSearchSelected[R URLRequest, C any] struct {
	provider SearchPrefixProvider[R]
	adapt    func(Operation, C) R
}

func (selected typedSearchSelected[R, C]) Name() string { return selected.provider.Name() }
func (selected typedSearchSelected[R, C]) Suitable(rawURL string) bool {
	parsed, err := url.Parse(rawURL)
	return err == nil && selected.provider.Suitable(parsed)
}
func (selected typedSearchSelected[R, C]) RetrySafe() bool {
	_, ok := selected.provider.(RetrySafeProvider[R])
	return ok
}
func (selected typedSearchSelected[R, C]) Extract(ctx context.Context, operation Operation, configuration C) (Extraction, error) {
	return selected.provider.Extract(ctx, selected.adapt(operation, configuration))
}
func (selected typedSearchSelected[R, C]) SearchQueryAllowed(query string) bool {
	return selected.provider.SearchQueryAllowed(query)
}
