package youtube

import (
	"context"
	"net/http"

	"github.com/tejasa97/youtube_dlp/engine/provider"
	"github.com/tejasa97/youtube_dlp/internal/value"
)

// Provider implementation aliases remain local to the complete YouTube
// family while their ownership is provider-neutral.
type (
	Transport                   = provider.Transport
	CookieIsolatedTransport     = provider.CookieIsolatedTransport
	Extraction                  = provider.Extraction
	Entry                       = provider.Entry
	EntrySequence               = provider.EntrySequence
	EntryIterator               = provider.EntryIterator
	Metadata                    = provider.Metadata
	ContinuationFetcher         = provider.ContinuationFetcher
	StatefulContinuationFetcher = provider.StatefulContinuationFetcher
	Provider                    = provider.Provider[Request]
	Extractor                   = provider.Provider[Request]
	Registry                    = provider.Registry[Request]
)

const (
	defaultMaxPlaylistEntries = 100_000
	maxExtractorJSONBytes     = 16 << 20
)

var (
	ErrUnsupported          = provider.ErrUnsupported
	ErrInvalidMetadata      = provider.ErrInvalidMetadata
	ErrUnavailable          = provider.ErrUnavailable
	ErrAuthentication       = provider.ErrAuthentication
	ErrChallengeSolver      = provider.ErrChallengeSolver
	ErrTransportIsolation   = provider.ErrTransportIsolation
	ErrInvalidPlaylist      = provider.ErrInvalidPlaylist
	ErrPlaylistLimit        = provider.ErrPlaylistLimit
	ErrJSONResponseTooLarge = provider.ErrJSONResponseTooLarge
)

type HTTPStatusError = provider.HTTPStatusError

func NewRegistry(providers ...Provider) *Registry {
	return provider.NewRegistry[Request](providers...)
}

func Media(info value.Info) Extraction { return provider.Media(info) }

func URLResult(entry Entry) (Extraction, error) { return provider.URLResult(entry) }

func Playlist(info value.Info, entries EntrySequence) (Extraction, error) {
	return provider.Playlist(info, entries)
}

func StaticEntries(entries ...Entry) EntrySequence { return provider.StaticEntries(entries...) }

func ContinuationEntries(first []Entry, nextToken string, fetch provider.ContinuationFetcher) (EntrySequence, error) {
	return provider.ContinuationEntries(first, nextToken, fetch)
}

func StatefulContinuationEntries(first []Entry, nextToken, state string, fetch provider.StatefulContinuationFetcher) (EntrySequence, error) {
	return provider.StatefulContinuationEntries(first, nextToken, state, fetch)
}

func CollectEntries(ctx context.Context, sequence EntrySequence, limit int) ([]Entry, error) {
	return provider.CollectEntries(ctx, sequence, limit)
}

func limitEntries(source EntrySequence, limit int) EntrySequence {
	return provider.LimitEntries(source, limit)
}

func contextError(ctx context.Context) error {
	if ctx == nil {
		return nil
	}
	return ctx.Err()
}

func RequestJSON(ctx context.Context, transport Transport, method, rawURL string, body []byte, headers http.Header, target any) error {
	return provider.RequestJSON(ctx, transport, method, rawURL, body, headers, target)
}

func RequestJSONWithoutCookies(ctx context.Context, transport Transport, method, rawURL string, body []byte, headers http.Header, target any) error {
	return provider.RequestJSONWithoutCookies(ctx, transport, method, rawURL, body, headers, target)
}

func requestJSON(ctx context.Context, execute func(context.Context, *http.Request) (*http.Response, error), method, rawURL string, body []byte, headers http.Header, target any) error {
	return provider.RequestJSONWithExecutor(ctx, execute, method, rawURL, body, headers, target)
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
	return provider.ExtractJSONObject(page, marker)
}

func extractJSONObjectFrom(page []byte, offset, maxStartOffset int) ([]byte, int, error) {
	return provider.ExtractJSONObjectFrom(page, offset, maxStartOffset)
}

func manifestFormat(id, rawURL, protocolName string) *value.Object {
	return provider.ManifestFormat(id, rawURL, protocolName)
}
