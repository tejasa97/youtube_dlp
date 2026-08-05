// Package extraction preserves the historical internal extraction surface
// through aliases and thin wrappers to the public engine owner.
package extraction

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/tejasa97/youtube_dlp/engine"
	"github.com/tejasa97/youtube_dlp/engine/value"
)

type (
	Provider[R any]             = engine.Provider[R]
	SearchPrefixProvider[R any] = engine.SearchPrefixProvider[R]
	RetrySafeProvider[R any]    = engine.RetrySafeProvider[R]
	ExplicitOnlyProvider        = engine.ExplicitOnlyProvider
	URLRequest                  = engine.URLRequest
	Registry[R URLRequest]      = engine.Registry[R]

	Transport                                    = engine.Transport
	CookieIsolatedTransport                      = engine.CookieIsolatedTransport
	CredentialIsolatedNoRedirectTransport        = engine.CredentialIsolatedNoRedirectTransport
	RefererCredentialIsolatedNoRedirectTransport = engine.RefererCredentialIsolatedNoRedirectTransport
	ScopedAuthorizationNoRedirectTransport       = engine.ScopedAuthorizationNoRedirectTransport
	ScopedAuthenticationNoRedirectTransport      = engine.ScopedAuthenticationNoRedirectTransport
	CredentialIsolatedProfilePageTransport       = engine.CredentialIsolatedProfilePageTransport
	ProfileTransport                             = engine.ProfileTransport
	ProfiledNoRedirectTransport                  = engine.ProfiledNoRedirectTransport
	ProfiledPageNoRedirectTransport              = engine.ProfiledPageNoRedirectTransport
	Credential                                   = engine.Credential
	CredentialProvider                           = engine.CredentialProvider

	Request                     = engine.Request
	Extraction                  = engine.Extraction
	MetadataEnricher            = engine.MetadataEnricher
	Entry                       = engine.Entry
	EntrySequence               = engine.EntrySequence
	EntryIterator               = engine.EntryIterator
	PageFetcher                 = engine.PageFetcher
	PageFetcherWithContinuation = engine.PageFetcherWithContinuation
	ContinuationFetcher         = engine.ContinuationFetcher
	StatefulContinuationFetcher = engine.StatefulContinuationFetcher
	Metadata                    = engine.Metadata
	MetadataProvider            = engine.MetadataProvider
	HTTPStatusError             = engine.HTTPStatusError
)

var (
	ErrUnsupported          = engine.ErrUnsupported
	ErrInvalidRouting       = engine.ErrInvalidRouting
	ErrUnsupportedRouting   = engine.ErrUnsupportedRouting
	ErrInvalidMetadata      = engine.ErrInvalidMetadata
	ErrUnavailable          = engine.ErrUnavailable
	ErrRegionRestricted     = engine.ErrRegionRestricted
	ErrAuthentication       = engine.ErrAuthentication
	ErrWrongPassword        = engine.ErrWrongPassword
	ErrChallengeSolver      = engine.ErrChallengeSolver
	ErrTransportProfile     = engine.ErrTransportProfile
	ErrTransportIsolation   = engine.ErrTransportIsolation
	ErrInvalidPlaylist      = engine.ErrInvalidPlaylist
	ErrPlaylistLimit        = engine.ErrPlaylistLimit
	ErrInvalidSelection     = engine.ErrInvalidSelection
	ErrSelectionDisabled    = engine.ErrSelectionDisabled
	ErrJSONResponseTooLarge = engine.ErrJSONResponseTooLarge
)

func NewRegistry[R URLRequest](providers ...Provider[R]) *Registry[R] {
	return engine.NewRegistry[R](providers...)
}

func ValidateSelectionRules(rules []string) error { return engine.ValidateSelectionRules(rules) }

func DoWithProfile(ctx context.Context, transport Transport, request *http.Request, profile string) (*http.Response, error) {
	return engine.DoWithProfile(ctx, transport, request, profile)
}

func ReadPageWithProfile(ctx context.Context, transport Transport, rawURL, profile string) ([]byte, http.Header, error) {
	return engine.ReadPageWithProfile(ctx, transport, rawURL, profile)
}

func ReadPageWithProfileWithoutCredentialsNoRedirect(ctx context.Context, transport Transport, rawURL, profile string) ([]byte, http.Header, error) {
	return engine.ReadPageWithProfileWithoutCredentialsNoRedirect(ctx, transport, rawURL, profile)
}

func Media(info value.Info) Extraction          { return engine.Media(info) }
func URLResult(entry Entry) (Extraction, error) { return engine.URLResult(entry) }
func Playlist(info value.Info, entries EntrySequence) (Extraction, error) {
	return engine.Playlist(info, entries)
}
func StaticEntries(entries ...Entry) EntrySequence { return engine.StaticEntries(entries...) }
func OnDemandEntries(pageSize int, fetch PageFetcher) (EntrySequence, error) {
	return engine.OnDemandEntries(pageSize, fetch)
}
func OnDemandEntriesWithContinuation(pageSize int, fetch PageFetcherWithContinuation) (EntrySequence, error) {
	return engine.OnDemandEntriesWithContinuation(pageSize, fetch)
}
func LazyFirstPageEntries(maxEntries int, fetch func(context.Context) ([]Entry, error)) (EntrySequence, error) {
	return engine.LazyFirstPageEntries(maxEntries, fetch)
}
func ContinuationEntries(first []Entry, nextToken string, fetch ContinuationFetcher) (EntrySequence, error) {
	return engine.ContinuationEntries(first, nextToken, fetch)
}
func ContinuationEntriesWithPageLimit(first []Entry, nextToken string, maxPages int, fetch ContinuationFetcher) (EntrySequence, error) {
	return engine.ContinuationEntriesWithPageLimit(first, nextToken, maxPages, fetch)
}
func StatefulContinuationEntries(first []Entry, nextToken, state string, fetch StatefulContinuationFetcher) (EntrySequence, error) {
	return engine.StatefulContinuationEntries(first, nextToken, state, fetch)
}
func CollectEntries(ctx context.Context, sequence EntrySequence, limit int) ([]Entry, error) {
	return engine.CollectEntries(ctx, sequence, limit)
}
func LimitEntries(source EntrySequence, limit int) EntrySequence {
	return engine.LimitEntries(source, limit)
}
func ManifestFormat(id, rawURL, protocolName string) *value.Object {
	return engine.ManifestFormat(id, rawURL, protocolName)
}
func RequestJSON(ctx context.Context, transport Transport, method, rawURL string, body []byte, headers http.Header, target any) error {
	return engine.RequestJSON(ctx, transport, method, rawURL, body, headers, target)
}
func RequestJSONWithoutCookies(ctx context.Context, transport Transport, method, rawURL string, body []byte, headers http.Header, target any) error {
	return engine.RequestJSONWithoutCookies(ctx, transport, method, rawURL, body, headers, target)
}
func RequestJSONWithoutCredentialsNoRedirect(ctx context.Context, transport Transport, method, rawURL string, body []byte, headers http.Header, target any) error {
	return engine.RequestJSONWithoutCredentialsNoRedirect(ctx, transport, method, rawURL, body, headers, target)
}
func RequestJSONWithScopedAuthorizationNoRedirect(ctx context.Context, transport Transport, method, rawURL string, body []byte, headers http.Header, target any) error {
	return engine.RequestJSONWithScopedAuthorizationNoRedirect(ctx, transport, method, rawURL, body, headers, target)
}
func RequestJSONWithExecutor(ctx context.Context, execute func(context.Context, *http.Request) (*http.Response, error), method, rawURL string, body []byte, headers http.Header, target any) error {
	return engine.RequestJSONWithExecutor(ctx, execute, method, rawURL, body, headers, target)
}
func EnsureJSONEOF(decoder *json.Decoder) error { return engine.EnsureJSONEOF(decoder) }
func ExtractJSONObject(page []byte, marker string) ([]byte, error) {
	return engine.ExtractJSONObject(page, marker)
}
func ExtractJSONObjectFrom(page []byte, offset, maxStartOffset int) ([]byte, int, error) {
	return engine.ExtractJSONObjectFrom(page, offset, maxStartOffset)
}
