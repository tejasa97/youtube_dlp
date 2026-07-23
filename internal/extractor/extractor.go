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
	"github.com/ytdlp-go/ytdlp/internal/youtubepot"
)

var (
	ErrUnsupported        = errors.New("unsupported URL")
	ErrInvalidMetadata    = errors.New("invalid extractor metadata")
	ErrUnavailable        = errors.New("media unavailable")
	ErrRegionRestricted   = errors.New("media region restricted")
	ErrAuthentication     = errors.New("authentication required")
	ErrChallengeSolver    = errors.New("JavaScript challenge solver unavailable")
	ErrTransportProfile   = errors.New("transport profile unavailable")
	ErrTransportIsolation = errors.New("cookie-isolated transport unavailable")
)

type Transport interface {
	Do(context.Context, *http.Request) (*http.Response, error)
	ReadPage(context.Context, string) ([]byte, http.Header, error)
}

// CookieIsolatedTransport is an optional capability for requests that must not
// inherit the operation cookie jar or an explicit Cookie header. Extractors use
// it for clients whose protocol explicitly does not support browser cookies.
type CookieIsolatedTransport interface {
	DoWithoutCookies(context.Context, *http.Request) (*http.Response, error)
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
	URL                       string
	Transport                 Transport
	ChallengeSolver           YouTubeChallengeSolver
	Credentials               CredentialProvider
	YouTubePOT                *youtubepot.Director
	YouTubeTranslatedCaptions bool
	YouTubeLiveFromStart      bool
	YouTubeComments           YouTubeCommentOptions
}

// YouTubeCommentOptions controls opt-in comment retrieval. Zero Max selects
// the extractor's bounded default. Sort accepts "top" or "new".
type YouTubeCommentOptions struct {
	Enabled             bool
	Sort                string
	MaxComments         int
	MaxParents          int
	MaxReplies          int
	MaxRepliesPerThread int
	MaxDepth            int
}

// Credential is an extractor-scoped authentication tuple. It must never be
// included in metadata, events, or diagnostic errors.
type Credential struct {
	Username string
	Password string
}

func (Credential) String() string   { return "[redacted extractor credential]" }
func (Credential) GoString() string { return "extractor.Credential{[redacted]}" }

// CredentialProvider resolves a stable extractor machine key. Extractors must
// request credentials explicitly; credentials are never attached globally to
// arbitrary requests or redirect targets.
type CredentialProvider interface {
	Lookup(context.Context, string) (Credential, bool, error)
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

// Names returns extractor identifiers in deterministic routing-priority order.
func (registry *Registry) Names() []string {
	if registry == nil {
		return nil
	}
	names := make([]string, 0, len(registry.extractors))
	for _, candidate := range registry.extractors {
		if candidate != nil {
			names = append(names, candidate.Name())
		}
	}
	return names
}

func NewRegistry(extractors ...Extractor) *Registry {
	return &Registry{extractors: append([]Extractor(nil), extractors...)}
}

// Select returns the first suitable extractor, making registration order the
// explicit and deterministic priority rule.
func (registry *Registry) Select(rawURL string) (Extractor, error) {
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" && parsed.Opaque == "" {
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
	if err != nil || parsed.Scheme == "" || parsed.Host == "" && parsed.Opaque == "" {
		return nil, fmt.Errorf("%w: invalid URL result", ErrUnsupported)
	}
	for _, candidate := range registry.extractors {
		if strings.EqualFold(candidate.Name(), extractorKey) {
			if parsed.Host == "" && !candidate.Suitable(parsed) {
				return nil, fmt.Errorf("%w: invalid opaque URL result", ErrUnsupported)
			}
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
