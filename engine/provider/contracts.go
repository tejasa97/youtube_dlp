package provider

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
)

var (
	ErrUnsupported        = errors.New("unsupported URL")
	ErrInvalidRouting     = errors.New("invalid routing input")
	ErrUnsupportedRouting = errors.New("unsupported routing control")
	ErrInvalidMetadata    = errors.New("invalid extractor metadata")
	ErrUnavailable        = errors.New("media unavailable")
	ErrRegionRestricted   = errors.New("media region restricted")
	ErrAuthentication     = errors.New("authentication required")
	ErrWrongPassword      = errors.New("wrong video password")
	ErrChallengeSolver    = errors.New("JavaScript challenge solver unavailable")
	ErrTransportProfile   = errors.New("transport profile unavailable")
	ErrTransportIsolation = errors.New("cookie-isolated transport unavailable")
)

// Provider is the smallest contract needed by registry routing. R is owned by
// the composition boundary, so the registry does not depend on any provider's
// request options.
type Provider[R any] interface {
	Name() string
	Suitable(*url.URL) bool
	Extract(context.Context, R) (Extraction, error)
}

// SearchPrefixProvider marks a provider that owns one or more opaque search
// URL schemes.
type SearchPrefixProvider[R any] interface {
	Provider[R]
	SupportsSearchPrefix(string) bool
	SearchQueryAllowed(string) bool
}

// RetrySafeProvider opts into replay of a complete Extract operation.
type RetrySafeProvider[R any] interface {
	Provider[R]
	RetrySafe()
}

type Transport interface {
	Do(context.Context, *http.Request) (*http.Response, error)
	ReadPage(context.Context, string) ([]byte, http.Header, error)
}

// CookieIsolatedTransport is an optional capability for requests that must not
// inherit the operation cookie jar or an explicit Cookie header.
type CookieIsolatedTransport interface {
	DoWithoutCookies(context.Context, *http.Request) (*http.Response, error)
}

type CredentialIsolatedNoRedirectTransport interface {
	DoWithoutCredentialsNoRedirect(context.Context, *http.Request) (*http.Response, error)
}

type RefererCredentialIsolatedNoRedirectTransport interface {
	DoWithoutCredentialsNoRedirectWithReferer(context.Context, *http.Request) (*http.Response, error)
}

type ScopedAuthorizationNoRedirectTransport interface {
	DoWithScopedAuthorizationNoRedirect(context.Context, *http.Request) (*http.Response, error)
}

type ScopedAuthenticationNoRedirectTransport interface {
	DoWithScopedAuthenticationNoRedirect(context.Context, *http.Request) (*http.Response, error)
}

type CredentialIsolatedProfilePageTransport interface {
	ReadPageProfileWithoutCredentialsNoRedirect(context.Context, string, string) ([]byte, http.Header, error)
}

type ProfileTransport interface {
	DoProfile(context.Context, *http.Request, string) (*http.Response, error)
	ReadPageProfile(context.Context, string, string) ([]byte, http.Header, error)
}

type ProfiledNoRedirectTransport interface {
	DoProfiledNoRedirect(context.Context, *http.Request, string) (*http.Response, error)
}

type ProfiledPageNoRedirectTransport interface {
	DoProfiledPageNoRedirect(context.Context, *http.Request, string) (*http.Response, error)
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

// Credential is a provider-scoped authentication tuple.
type Credential struct {
	Username string
	Password string
}

func (Credential) String() string   { return "[redacted extractor credential]" }
func (Credential) GoString() string { return "extractor.Credential{[redacted]}" }

type CredentialProvider interface {
	Lookup(context.Context, string) (Credential, bool, error)
}
