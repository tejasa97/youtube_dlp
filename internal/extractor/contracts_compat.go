package extractor

import (
	"context"
	"net/http"

	"github.com/ytdlp-go/ytdlp/internal/extraction"
	"github.com/ytdlp-go/ytdlp/internal/value"
)

// These aliases preserve the existing internal/extractor surface while the
// implementation lives in the provider-neutral extraction package.
type (
	Extractor             = extraction.Provider[Request]
	SearchPrefixExtractor = extraction.SearchPrefixProvider[Request]
	RetrySafeExtractor    = extraction.RetrySafeProvider[Request]
	ExplicitOnlyExtractor = extraction.ExplicitOnlyProvider
	Registry              = extraction.Registry[Request]

	Transport                                    = extraction.Transport
	CookieIsolatedTransport                      = extraction.CookieIsolatedTransport
	CredentialIsolatedNoRedirectTransport        = extraction.CredentialIsolatedNoRedirectTransport
	RefererCredentialIsolatedNoRedirectTransport = extraction.RefererCredentialIsolatedNoRedirectTransport
	ScopedAuthorizationNoRedirectTransport       = extraction.ScopedAuthorizationNoRedirectTransport
	ScopedAuthenticationNoRedirectTransport      = extraction.ScopedAuthenticationNoRedirectTransport
	CredentialIsolatedProfilePageTransport       = extraction.CredentialIsolatedProfilePageTransport
	ProfileTransport                             = extraction.ProfileTransport
	ProfiledNoRedirectTransport                  = extraction.ProfiledNoRedirectTransport
	ProfiledPageNoRedirectTransport              = extraction.ProfiledPageNoRedirectTransport
	Credential                                   = extraction.Credential
	CredentialProvider                           = extraction.CredentialProvider

	Extraction                  = extraction.Extraction
	MetadataEnricher            = extraction.MetadataEnricher
	Entry                       = extraction.Entry
	EntrySequence               = extraction.EntrySequence
	EntryIterator               = extraction.EntryIterator
	PageFetcher                 = extraction.PageFetcher
	PageFetcherWithContinuation = extraction.PageFetcherWithContinuation
	ContinuationFetcher         = extraction.ContinuationFetcher
	StatefulContinuationFetcher = extraction.StatefulContinuationFetcher

	Metadata         = extraction.Metadata
	MetadataProvider = extraction.MetadataProvider
)

var (
	ErrUnsupported        = extraction.ErrUnsupported
	ErrInvalidRouting     = extraction.ErrInvalidRouting
	ErrUnsupportedRouting = extraction.ErrUnsupportedRouting
	ErrInvalidMetadata    = extraction.ErrInvalidMetadata
	ErrUnavailable        = extraction.ErrUnavailable
	ErrRegionRestricted   = extraction.ErrRegionRestricted
	ErrAuthentication     = extraction.ErrAuthentication
	ErrWrongPassword      = extraction.ErrWrongPassword
	ErrChallengeSolver    = extraction.ErrChallengeSolver
	ErrTransportProfile   = extraction.ErrTransportProfile
	ErrTransportIsolation = extraction.ErrTransportIsolation
	ErrInvalidPlaylist    = extraction.ErrInvalidPlaylist
	ErrPlaylistLimit      = extraction.ErrPlaylistLimit
	ErrInvalidSelection   = extraction.ErrInvalidSelection
	ErrSelectionDisabled  = extraction.ErrSelectionDisabled
)

const (
	defaultMaxPlaylistPages      = 10_000
	defaultMaxPlaylistEntries    = 100_000
	maxExtractorDescriptionBytes = 256
)

func NewRegistry(extractors ...Extractor) *Registry {
	return extraction.NewRegistry[Request](extractors...)
}

func ValidateSelectionRules(rules []string) error {
	return extraction.ValidateSelectionRules(rules)
}

func DoWithProfile(ctx context.Context, transport Transport, request *http.Request, profile string) (*http.Response, error) {
	return extraction.DoWithProfile(ctx, transport, request, profile)
}

func ReadPageWithProfile(ctx context.Context, transport Transport, rawURL, profile string) ([]byte, http.Header, error) {
	return extraction.ReadPageWithProfile(ctx, transport, rawURL, profile)
}

func ReadPageWithProfileWithoutCredentialsNoRedirect(ctx context.Context, transport Transport, rawURL, profile string) ([]byte, http.Header, error) {
	return extraction.ReadPageWithProfileWithoutCredentialsNoRedirect(ctx, transport, rawURL, profile)
}

func Media(info value.Info) Extraction { return extraction.Media(info) }

func URLResult(entry Entry) (Extraction, error) { return extraction.URLResult(entry) }

func Playlist(info value.Info, entries EntrySequence) (Extraction, error) {
	return extraction.Playlist(info, entries)
}

func StaticEntries(entries ...Entry) EntrySequence { return extraction.StaticEntries(entries...) }

func OnDemandEntries(pageSize int, fetch PageFetcher) (EntrySequence, error) {
	return extraction.OnDemandEntries(pageSize, fetch)
}

func OnDemandEntriesWithContinuation(pageSize int, fetch PageFetcherWithContinuation) (EntrySequence, error) {
	return extraction.OnDemandEntriesWithContinuation(pageSize, fetch)
}

func LazyFirstPageEntries(maxEntries int, fetch func(context.Context) ([]Entry, error)) (EntrySequence, error) {
	return extraction.LazyFirstPageEntries(maxEntries, fetch)
}

func ContinuationEntries(first []Entry, nextToken string, fetch ContinuationFetcher) (EntrySequence, error) {
	return extraction.ContinuationEntries(first, nextToken, fetch)
}

func continuationEntriesWithPageLimit(first []Entry, nextToken string, maxPages int, fetch ContinuationFetcher) (EntrySequence, error) {
	return extraction.ContinuationEntriesWithPageLimit(first, nextToken, maxPages, fetch)
}

func StatefulContinuationEntries(first []Entry, nextToken, state string, fetch StatefulContinuationFetcher) (EntrySequence, error) {
	return extraction.StatefulContinuationEntries(first, nextToken, state, fetch)
}

func CollectEntries(ctx context.Context, sequence EntrySequence, limit int) ([]Entry, error) {
	return extraction.CollectEntries(ctx, sequence, limit)
}

func contextError(ctx context.Context) error {
	if ctx == nil {
		return nil
	}
	return ctx.Err()
}
