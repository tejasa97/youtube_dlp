package extractor

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
)

const maxExtractorJSONBytes int64 = 16 << 20

var ErrJSONResponseTooLarge = errors.New("extractor JSON response too large")

type HTTPStatusError struct{ Code int }

func (err *HTTPStatusError) Error() string { return fmt.Sprintf("HTTP status %d", err.Code) }

// RequestJSON executes a bounded JSON request through the shared operation
// transport. Request and response bodies are never included in errors.
func RequestJSON(ctx context.Context, transport Transport, method, rawURL string, body []byte, headers http.Header, target any) error {
	if transport == nil {
		return errors.New("invalid JSON request")
	}
	return requestJSON(ctx, transport.Do, method, rawURL, body, headers, target)
}

// RequestJSONWithoutCookies executes a bounded JSON request only when the
// transport can guarantee that neither its jar nor explicit request headers
// attach cookies. It fails closed when that capability is unavailable.
func RequestJSONWithoutCookies(ctx context.Context, transport Transport, method, rawURL string, body []byte, headers http.Header, target any) error {
	if transport == nil {
		return errors.New("invalid JSON request")
	}
	isolated, ok := transport.(CookieIsolatedTransport)
	if !ok {
		return ErrTransportIsolation
	}
	return requestJSON(ctx, isolated.DoWithoutCookies, method, rawURL, body, headers, target)
}

func requestJSON(ctx context.Context, execute func(context.Context, *http.Request) (*http.Response, error), method, rawURL string, body []byte, headers http.Header, target any) error {
	if execute == nil || target == nil {
		return errors.New("invalid JSON request")
	}
	request, err := http.NewRequestWithContext(ctx, method, rawURL, bytes.NewReader(body))
	if err != nil {
		return errors.New("invalid JSON request")
	}
	request.Header = headers.Clone()
	response, err := execute(ctx, request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return &HTTPStatusError{Code: response.StatusCode}
	}
	reader := &io.LimitedReader{R: response.Body, N: maxExtractorJSONBytes + 1}
	data, err := io.ReadAll(reader)
	if err != nil {
		return errors.New("read extractor JSON response failed")
	}
	if int64(len(data)) > maxExtractorJSONBytes {
		return ErrJSONResponseTooLarge
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("%w: invalid JSON response", ErrInvalidMetadata)
	}
	if err := ensureJSONEOF(decoder); err != nil {
		return fmt.Errorf("%w: trailing JSON response", ErrInvalidMetadata)
	}
	return nil
}

func ensureJSONEOF(decoder *json.Decoder) error {
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("trailing JSON value")
		}
		return err
	}
	return nil
}
