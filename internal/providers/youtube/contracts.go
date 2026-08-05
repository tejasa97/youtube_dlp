package youtube

import (
	"context"
	"net/http"

	"github.com/tejasa97/youtube_dlp/internal/extraction"
	"github.com/tejasa97/youtube_dlp/internal/value"
)

// Provider implementation aliases remain local to the complete YouTube
// family while their ownership is provider-neutral.
type (
	Transport                   = extraction.Transport
	CookieIsolatedTransport     = extraction.CookieIsolatedTransport
	Extraction                  = extraction.Extraction
	Entry                       = extraction.Entry
	EntrySequence               = extraction.EntrySequence
	EntryIterator               = extraction.EntryIterator
	Metadata                    = extraction.Metadata
	ContinuationFetcher         = extraction.ContinuationFetcher
	StatefulContinuationFetcher = extraction.StatefulContinuationFetcher
	Provider                    = extraction.Provider[Request]
	Extractor                   = extraction.Provider[Request]
	Registry                    = extraction.Registry[Request]
)

const (
	defaultMaxPlaylistEntries = 100_000
	maxExtractorJSONBytes     = 16 << 20
)

var (
	ErrUnsupported          = extraction.ErrUnsupported
	ErrInvalidMetadata      = extraction.ErrInvalidMetadata
	ErrUnavailable          = extraction.ErrUnavailable
	ErrAuthentication       = extraction.ErrAuthentication
	ErrChallengeSolver      = extraction.ErrChallengeSolver
	ErrTransportIsolation   = extraction.ErrTransportIsolation
	ErrInvalidPlaylist      = extraction.ErrInvalidPlaylist
	ErrPlaylistLimit        = extraction.ErrPlaylistLimit
	ErrJSONResponseTooLarge = extraction.ErrJSONResponseTooLarge
)

type HTTPStatusError = extraction.HTTPStatusError

func NewRegistry(providers ...Provider) *Registry {
	return extraction.NewRegistry[Request](providers...)
}

func Media(info value.Info) Extraction { return extraction.Media(info) }

func URLResult(entry Entry) (Extraction, error) { return extraction.URLResult(entry) }

func Playlist(info value.Info, entries EntrySequence) (Extraction, error) {
	return extraction.Playlist(info, entries)
}

func StaticEntries(entries ...Entry) EntrySequence { return extraction.StaticEntries(entries...) }

func ContinuationEntries(first []Entry, nextToken string, fetch extraction.ContinuationFetcher) (EntrySequence, error) {
	return extraction.ContinuationEntries(first, nextToken, fetch)
}

func StatefulContinuationEntries(first []Entry, nextToken, state string, fetch extraction.StatefulContinuationFetcher) (EntrySequence, error) {
	return extraction.StatefulContinuationEntries(first, nextToken, state, fetch)
}

func CollectEntries(ctx context.Context, sequence EntrySequence, limit int) ([]Entry, error) {
	return extraction.CollectEntries(ctx, sequence, limit)
}

func limitEntries(source EntrySequence, limit int) EntrySequence {
	return extraction.LimitEntries(source, limit)
}

func contextError(ctx context.Context) error {
	if ctx == nil {
		return nil
	}
	return ctx.Err()
}

func RequestJSON(ctx context.Context, transport Transport, method, rawURL string, body []byte, headers http.Header, target any) error {
	return extraction.RequestJSON(ctx, transport, method, rawURL, body, headers, target)
}

func RequestJSONWithoutCookies(ctx context.Context, transport Transport, method, rawURL string, body []byte, headers http.Header, target any) error {
	return extraction.RequestJSONWithoutCookies(ctx, transport, method, rawURL, body, headers, target)
}

func requestJSON(ctx context.Context, execute func(context.Context, *http.Request) (*http.Response, error), method, rawURL string, body []byte, headers http.Header, target any) error {
	return extraction.RequestJSONWithExecutor(ctx, execute, method, rawURL, body, headers, target)
}

func firstNonEmpty(inputs ...string) string {
	for _, input := range inputs {
		if input != "" {
			return input
		}
	}
	return ""
}

func extractJSONObject(page []byte, marker string) ([]byte, error) {
	return extraction.ExtractJSONObject(page, marker)
}

func extractJSONObjectFrom(page []byte, offset, maxStartOffset int) ([]byte, int, error) {
	return extraction.ExtractJSONObjectFrom(page, offset, maxStartOffset)
}

func manifestFormat(id, rawURL, protocolName string) *value.Object {
	return extraction.ManifestFormat(id, rawURL, protocolName)
}
