package extractor

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/tejasa97/youtube_dlp/internal/extraction"
)

const maxExtractorJSONBytes = 16 << 20

var ErrJSONResponseTooLarge = extraction.ErrJSONResponseTooLarge

type HTTPStatusError = extraction.HTTPStatusError

func RequestJSON(ctx context.Context, transport Transport, method, rawURL string, body []byte, headers http.Header, target any) error {
	return extraction.RequestJSON(ctx, transport, method, rawURL, body, headers, target)
}

func RequestJSONWithoutCookies(ctx context.Context, transport Transport, method, rawURL string, body []byte, headers http.Header, target any) error {
	return extraction.RequestJSONWithoutCookies(ctx, transport, method, rawURL, body, headers, target)
}

func RequestJSONWithoutCredentialsNoRedirect(ctx context.Context, transport Transport, method, rawURL string, body []byte, headers http.Header, target any) error {
	return extraction.RequestJSONWithoutCredentialsNoRedirect(ctx, transport, method, rawURL, body, headers, target)
}

func RequestJSONWithScopedAuthorizationNoRedirect(ctx context.Context, transport Transport, method, rawURL string, body []byte, headers http.Header, target any) error {
	return extraction.RequestJSONWithScopedAuthorizationNoRedirect(ctx, transport, method, rawURL, body, headers, target)
}

func requestJSON(ctx context.Context, execute func(context.Context, *http.Request) (*http.Response, error), method, rawURL string, body []byte, headers http.Header, target any) error {
	return extraction.RequestJSONWithExecutor(ctx, execute, method, rawURL, body, headers, target)
}

func extractJSONObject(page []byte, marker string) ([]byte, error) {
	return extraction.ExtractJSONObject(page, marker)
}

func extractJSONObjectFrom(page []byte, offset, maxStartOffset int) ([]byte, int, error) {
	return extraction.ExtractJSONObjectFrom(page, offset, maxStartOffset)
}

func ensureJSONEOF(decoder *json.Decoder) error { return extraction.EnsureJSONEOF(decoder) }
