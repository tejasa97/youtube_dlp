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
	ErrWrongPassword      = errors.New("wrong video password")
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

// CredentialIsolatedNoRedirectTransport is an optional capability for hop-by-hop
// redirect resolution that must not follow redirects and must not forward
// operation-jar cookies or credential-bearing headers.
type CredentialIsolatedNoRedirectTransport interface {
	DoWithoutCredentialsNoRedirect(context.Context, *http.Request) (*http.Response, error)
}

// ScopedAuthorizationNoRedirectTransport executes a request with only the
// caller-supplied Authorization header while excluding operation credentials,
// cookies, response-cookie persistence, and redirects. It is intended for
// short-lived application tokens obtained inside one extraction.
type ScopedAuthorizationNoRedirectTransport interface {
	DoWithScopedAuthorizationNoRedirect(context.Context, *http.Request) (*http.Response, error)
}

// CredentialIsolatedProfilePageTransport is an optional capability for bounded
// browser-profile page reads that must not inherit operation-jar cookies,
// explicit/default Authorization/Proxy-Authorization/Cookie headers, or follow
// redirects. Extractors that claim anonymous public profile fetches require it.
type CredentialIsolatedProfilePageTransport interface {
	ReadPageProfileWithoutCredentialsNoRedirect(context.Context, string, string) ([]byte, http.Header, error)
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

// ReadPageWithProfileWithoutCredentialsNoRedirect performs a bounded profile
// page read without credentials and without redirects. Transports that only
// implement ProfileTransport fail closed before any network access.
func ReadPageWithProfileWithoutCredentialsNoRedirect(ctx context.Context, transport Transport, rawURL, profile string) ([]byte, http.Header, error) {
	if profile == "" {
		return nil, nil, fmt.Errorf("%w: missing profile", ErrTransportProfile)
	}
	isolated, ok := transport.(CredentialIsolatedProfilePageTransport)
	if !ok {
		return nil, nil, fmt.Errorf("%w: %s", ErrTransportIsolation, profile)
	}
	return isolated.ReadPageProfileWithoutCredentialsNoRedirect(ctx, rawURL, profile)
}

type Request struct {
	URL string
	// Referer is an optional validated HTTPS embedding page URL propagated from
	// bounded playlist recursion. It must never carry cookies, Authorization, or
	// arbitrary caller headers.
	Referer         string
	Transport       Transport
	ChallengeSolver YouTubeChallengeSolver
	// Credentials resolves a stable extractor machine key. It must never be
	// embedded in metadata, events, or diagnostic errors. The same is true of
	// VideoPassword, which is consumed by extractors that gate media behind a
	// per-video secret and is never echoed back by the formatter.
	Credentials               CredentialProvider
	VideoPassword             string
	YouTubePOT                *youtubepot.Director
	YouTubeTranslatedCaptions bool
	YouTubeLiveFromStart      bool
	YouTubeComments           YouTubeCommentOptions
	SoundCloudComments        SoundCloudCommentOptions
}

// String and GoString deliberately render Request as a fixed opaque value so
// diagnostic formatting cannot expose URL credentials, transports, providers,
// or VideoPassword. Value receivers also cover *Request formatting.
func (Request) String() string   { return "[redacted extractor request]" }
func (Request) GoString() string { return "extractor.Request{[redacted]}" }

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

type SoundCloudCommentOptions struct {
	Enabled     bool
	Sort        string
	MaxComments int
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
