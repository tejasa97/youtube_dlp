// Package extraction preserves the historical internal extraction surface
// through aliases and thin wrappers to the public engine owner.
package extraction

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/tejasa97/ytdlp-go/engine/provider"
	"github.com/tejasa97/ytdlp-go/engine/value"
)

type (
	Provider[R any]             = provider.Provider[R]
	SearchPrefixProvider[R any] = provider.SearchPrefixProvider[R]
	RetrySafeProvider[R any]    = provider.RetrySafeProvider[R]
	ExplicitOnlyProvider        = provider.ExplicitOnlyProvider
	URLRequest                  = provider.URLRequest
	Registry[R URLRequest]      = provider.Registry[R]

	Transport                                    = provider.Transport
	CookieIsolatedTransport                      = provider.CookieIsolatedTransport
	CredentialIsolatedNoRedirectTransport        = provider.CredentialIsolatedNoRedirectTransport
	RefererCredentialIsolatedNoRedirectTransport = provider.RefererCredentialIsolatedNoRedirectTransport
	ScopedAuthorizationNoRedirectTransport       = provider.ScopedAuthorizationNoRedirectTransport
	ScopedAuthenticationNoRedirectTransport      = provider.ScopedAuthenticationNoRedirectTransport
	CredentialIsolatedProfilePageTransport       = provider.CredentialIsolatedProfilePageTransport
	ProfileTransport                             = provider.ProfileTransport
	ProfiledNoRedirectTransport                  = provider.ProfiledNoRedirectTransport
	ProfiledPageNoRedirectTransport              = provider.ProfiledPageNoRedirectTransport
	Credential                                   = provider.Credential
	CredentialProvider                           = provider.CredentialProvider

	Request                     = provider.Request
	Extraction                  = provider.Extraction
	MetadataEnricher            = provider.MetadataEnricher
	Entry                       = provider.Entry
	EntrySequence               = provider.EntrySequence
	EntryIterator               = provider.EntryIterator
	PageFetcher                 = provider.PageFetcher
	PageFetcherWithContinuation = provider.PageFetcherWithContinuation
	ContinuationFetcher         = provider.ContinuationFetcher
	StatefulContinuationFetcher = provider.StatefulContinuationFetcher
	Metadata                    = provider.Metadata
	MetadataProvider            = provider.MetadataProvider
	HTTPStatusError             = provider.HTTPStatusError
)

var (
	ErrUnsupported          = provider.ErrUnsupported
	ErrInvalidRouting       = provider.ErrInvalidRouting
	ErrUnsupportedRouting   = provider.ErrUnsupportedRouting
	ErrInvalidMetadata      = provider.ErrInvalidMetadata
	ErrUnavailable          = provider.ErrUnavailable
	ErrRegionRestricted     = provider.ErrRegionRestricted
	ErrAuthentication       = provider.ErrAuthentication
	ErrWrongPassword        = provider.ErrWrongPassword
	ErrChallengeSolver      = provider.ErrChallengeSolver
	ErrTransportProfile     = provider.ErrTransportProfile
	ErrTransportIsolation   = provider.ErrTransportIsolation
	ErrInvalidPlaylist      = provider.ErrInvalidPlaylist
	ErrPlaylistLimit        = provider.ErrPlaylistLimit
	ErrInvalidSelection     = provider.ErrInvalidSelection
	ErrSelectionDisabled    = provider.ErrSelectionDisabled
	ErrJSONResponseTooLarge = provider.ErrJSONResponseTooLarge
)

func NewRegistry[R URLRequest](providers ...Provider[R]) *Registry[R] {
	return provider.NewRegistry[R](providers...)
}

func ValidateSelectionRules(rules []string) error { return provider.ValidateSelectionRules(rules) }

func DoWithProfile(ctx context.Context, transport Transport, request *http.Request, profile string) (*http.Response, error) {
	return provider.DoWithProfile(ctx, transport, request, profile)
}

func ReadPageWithProfile(ctx context.Context, transport Transport, rawURL, profile string) ([]byte, http.Header, error) {
	return provider.ReadPageWithProfile(ctx, transport, rawURL, profile)
}

func ReadPageWithProfileWithoutCredentialsNoRedirect(ctx context.Context, transport Transport, rawURL, profile string) ([]byte, http.Header, error) {
	return provider.ReadPageWithProfileWithoutCredentialsNoRedirect(ctx, transport, rawURL, profile)
}

func Media(info value.Info) Extraction          { return provider.Media(info) }
func URLResult(entry Entry) (Extraction, error) { return provider.URLResult(entry) }
func Playlist(info value.Info, entries EntrySequence) (Extraction, error) {
	return provider.Playlist(info, entries)
}
func StaticEntries(entries ...Entry) EntrySequence { return provider.StaticEntries(entries...) }
func OnDemandEntries(pageSize int, fetch PageFetcher) (EntrySequence, error) {
	return provider.OnDemandEntries(pageSize, fetch)
}
func OnDemandEntriesWithContinuation(pageSize int, fetch PageFetcherWithContinuation) (EntrySequence, error) {
	return provider.OnDemandEntriesWithContinuation(pageSize, fetch)
}
func LazyFirstPageEntries(maxEntries int, fetch func(context.Context) ([]Entry, error)) (EntrySequence, error) {
	return provider.LazyFirstPageEntries(maxEntries, fetch)
}
func ContinuationEntries(first []Entry, nextToken string, fetch ContinuationFetcher) (EntrySequence, error) {
	return provider.ContinuationEntries(first, nextToken, fetch)
}
func ContinuationEntriesWithPageLimit(first []Entry, nextToken string, maxPages int, fetch ContinuationFetcher) (EntrySequence, error) {
	return provider.ContinuationEntriesWithPageLimit(first, nextToken, maxPages, fetch)
}
func StatefulContinuationEntries(first []Entry, nextToken, state string, fetch StatefulContinuationFetcher) (EntrySequence, error) {
	return provider.StatefulContinuationEntries(first, nextToken, state, fetch)
}
func CollectEntries(ctx context.Context, sequence EntrySequence, limit int) ([]Entry, error) {
	return provider.CollectEntries(ctx, sequence, limit)
}
func LimitEntries(source EntrySequence, limit int) EntrySequence {
	return provider.LimitEntries(source, limit)
}
func ManifestFormat(id, rawURL, protocolName string) *value.Object {
	return provider.ManifestFormat(id, rawURL, protocolName)
}
func RequestJSON(ctx context.Context, transport Transport, method, rawURL string, body []byte, headers http.Header, target any) error {
	return provider.RequestJSON(ctx, transport, method, rawURL, body, headers, target)
}
func RequestJSONWithoutCookies(ctx context.Context, transport Transport, method, rawURL string, body []byte, headers http.Header, target any) error {
	return provider.RequestJSONWithoutCookies(ctx, transport, method, rawURL, body, headers, target)
}
func RequestJSONWithoutCredentialsNoRedirect(ctx context.Context, transport Transport, method, rawURL string, body []byte, headers http.Header, target any) error {
	return provider.RequestJSONWithoutCredentialsNoRedirect(ctx, transport, method, rawURL, body, headers, target)
}
func RequestJSONWithScopedAuthorizationNoRedirect(ctx context.Context, transport Transport, method, rawURL string, body []byte, headers http.Header, target any) error {
	return provider.RequestJSONWithScopedAuthorizationNoRedirect(ctx, transport, method, rawURL, body, headers, target)
}
func RequestJSONWithExecutor(ctx context.Context, execute func(context.Context, *http.Request) (*http.Response, error), method, rawURL string, body []byte, headers http.Header, target any) error {
	return provider.RequestJSONWithExecutor(ctx, execute, method, rawURL, body, headers, target)
}
func EnsureJSONEOF(decoder *json.Decoder) error { return provider.EnsureJSONEOF(decoder) }
func ExtractJSONObject(page []byte, marker string) ([]byte, error) {
	return provider.ExtractJSONObject(page, marker)
}
func ExtractJSONObjectFrom(page []byte, offset, maxStartOffset int) ([]byte, int, error) {
	return provider.ExtractJSONObjectFrom(page, offset, maxStartOffset)
}
