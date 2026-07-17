// Package extractor defines extraction contracts, registry, and Phase 0 extractors.
package extractor

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"

	"github.com/ytdlp-go/ytdlp/internal/javascript/ejs"
	"github.com/ytdlp-go/ytdlp/internal/value"
)

var (
	ErrUnsupported     = errors.New("unsupported URL")
	ErrInvalidMetadata = errors.New("invalid extractor metadata")
	ErrUnavailable     = errors.New("media unavailable")
	ErrAuthentication  = errors.New("authentication required")
	ErrChallengeSolver = errors.New("JavaScript challenge solver unavailable")
)

type Transport interface {
	Do(context.Context, *http.Request) (*http.Response, error)
	ReadPage(context.Context, string) ([]byte, http.Header, error)
}

type Request struct {
	URL             string
	Transport       Transport
	ChallengeSolver YouTubeChallengeSolver
}

type YouTubeChallengeSolver interface {
	SolvePlayer(context.Context, string, string, []ejs.ChallengeRequest, bool) (ejs.Result, error)
}

type Extractor interface {
	Name() string
	Suitable(*url.URL) bool
	Extract(context.Context, Request) (value.Info, error)
}

type Registry struct {
	extractors []Extractor
}

func NewRegistry(extractors ...Extractor) *Registry {
	return &Registry{extractors: append([]Extractor(nil), extractors...)}
}

// Select returns the first suitable extractor, making registration order the
// explicit and deterministic priority rule.
func (registry *Registry) Select(rawURL string) (Extractor, error) {
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return nil, fmt.Errorf("%w: %q", ErrUnsupported, rawURL)
	}
	for _, candidate := range registry.extractors {
		if candidate.Suitable(parsed) {
			return candidate, nil
		}
	}
	return nil, fmt.Errorf("%w: %s", ErrUnsupported, parsed.Redacted())
}

func (registry *Registry) Extract(ctx context.Context, request Request) (value.Info, string, error) {
	selected, err := registry.Select(request.URL)
	if err != nil {
		return value.Info{}, "", err
	}
	info, err := selected.Extract(ctx, request)
	if err != nil {
		return value.Info{}, selected.Name(), fmt.Errorf("%s extractor: %w", selected.Name(), err)
	}
	return info, selected.Name(), nil
}
