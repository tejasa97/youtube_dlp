// Package extractor defines extraction contracts, registry, and Phase 0 extractors.
package extractor

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/ytdlp-go/ytdlp/internal/javascript/ejs"
)

var (
	ErrUnsupported      = errors.New("unsupported URL")
	ErrInvalidMetadata  = errors.New("invalid extractor metadata")
	ErrUnavailable      = errors.New("media unavailable")
	ErrRegionRestricted = errors.New("media region restricted")
	ErrAuthentication   = errors.New("authentication required")
	ErrChallengeSolver  = errors.New("JavaScript challenge solver unavailable")
	ErrTransportProfile = errors.New("transport profile unavailable")
)

type Transport interface {
	Do(context.Context, *http.Request) (*http.Response, error)
	ReadPage(context.Context, string) ([]byte, http.Header, error)
}

// ProfileTransport is an optional capability implemented by request directors
// that can execute an explicitly named browser transport profile.
type ProfileTransport interface {
	DoProfile(context.Context, *http.Request, string) (*http.Response, error)
	ReadPageProfile(context.Context, string, string) ([]byte, http.Header, error)
}

func DoWithProfile(ctx context.Context, transport Transport, request *http.Request, profile string) (*http.Response, error) {
	if profile == "" {
		return transport.Do(ctx, request)
	}
	profiled, ok := transport.(ProfileTransport)
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrTransportProfile, profile)
	}
	return profiled.DoProfile(ctx, request, profile)
}

func ReadPageWithProfile(ctx context.Context, transport Transport, rawURL, profile string) ([]byte, http.Header, error) {
	if profile == "" {
		return transport.ReadPage(ctx, rawURL)
	}
	profiled, ok := transport.(ProfileTransport)
	if !ok {
		return nil, nil, fmt.Errorf("%w: %s", ErrTransportProfile, profile)
	}
	return profiled.ReadPageProfile(ctx, rawURL, profile)
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
	Extract(context.Context, Request) (Extraction, error)
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

// SelectFor honors an explicit URL-result extractor key. It never silently
// falls back when the producer requested an unknown extractor.
func (registry *Registry) SelectFor(rawURL, extractorKey string) (Extractor, error) {
	if extractorKey == "" {
		return registry.Select(rawURL)
	}
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return nil, fmt.Errorf("%w: invalid URL result", ErrUnsupported)
	}
	for _, candidate := range registry.extractors {
		if strings.EqualFold(candidate.Name(), extractorKey) {
			return candidate, nil
		}
	}
	return nil, fmt.Errorf("%w: extractor key %q", ErrUnsupported, extractorKey)
}

func (registry *Registry) Extract(ctx context.Context, request Request) (Extraction, string, error) {
	selected, err := registry.Select(request.URL)
	if err != nil {
		return Extraction{}, "", err
	}
	result, err := selected.Extract(ctx, request)
	if err != nil {
		return Extraction{}, selected.Name(), fmt.Errorf("%s extractor: %w", selected.Name(), err)
	}
	return result, selected.Name(), nil
}
