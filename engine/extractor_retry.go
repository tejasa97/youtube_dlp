package engine

import (
	"context"
	"errors"
	"fmt"
	"net"
	"time"

	providerapi "github.com/tejasa97/youtube_dlp/engine/provider"
	"github.com/tejasa97/youtube_dlp/internal/network"
)

type extractorRetryWaitFunc func(context.Context, time.Duration) error

func (operation *operation) extractWithRetry(ctx context.Context, selected providerapi.Selected[compositionState], request providerapi.Operation, eventURL string) (Extraction, error) {
	extract := func() (Extraction, error) {
		result, err := selected.Extract(ctx, request, operation.client.compositionState(operation.request))
		return result, operation.classifyProviderError(err)
	}
	retries := operation.request.ExtractorRetries
	if retries <= 0 {
		return extract()
	}
	if !selected.RetrySafe() {
		return extract()
	}
	retryEventURL := network.RedactRawURL(eventURL)
	wait := operation.extractorRetryWait
	if wait == nil {
		wait = waitForExtractorRetry
	}
	for retry := 0; ; retry++ {
		if err := ctx.Err(); err != nil {
			return Extraction{}, err
		}
		extracted, err := extract()
		if err == nil {
			return extracted, nil
		}
		if retry >= retries || !isExtractorRetryable(err) {
			return Extraction{}, err
		}
		attempt := retry + 1
		if emitErr := operation.client.emit(ctx, Event{
			Kind: string(EventExtractorRetry), Extractor: selected.Name(), URL: retryEventURL,
			Attempt: attempt, Message: extractorRetryMessage(err),
		}); emitErr != nil {
			return Extraction{}, &Error{Category: ErrorInternal, Op: "emit extractor retry event", Err: emitErr}
		}
		if err := wait(ctx, extractorRetryDelay(operation.request.Downloader, attempt)); err != nil {
			return Extraction{}, err
		}
	}
}

func (operation *operation) classifyProviderError(err error) error {
	if err == nil || operation == nil || operation.registry == nil {
		return err
	}
	classify := operation.registry.Hooks().ClassifyError
	if classify == nil {
		return err
	}
	class, ok := classify(err)
	if !ok {
		return err
	}
	category := ErrorInternal
	switch class {
	case providerapi.ErrorAuthentication:
		category = ErrorAuthentication
	case providerapi.ErrorInvalidInput:
		category = ErrorInvalidInput
	case providerapi.ErrorNetwork:
		category = ErrorNetwork
	case providerapi.ErrorSecurity:
		category = ErrorSecurity
	case providerapi.ErrorUnsupported:
		category = ErrorUnsupported
	}
	return &Error{Category: category, Err: err}
}

func isExtractorRetryable(err error) bool {
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	var categorizedErr *Error
	if errors.As(err, &categorizedErr) {
		switch categorizedErr.Category {
		case ErrorAuthentication, ErrorUnsupported, ErrorInvalidInput, ErrorSecurity, ErrorCancelled:
			return false
		}
	}
	if errors.Is(err, ErrInvalidMetadata) || errors.Is(err, ErrInvalidPlaylist) ||
		errors.Is(err, ErrUnsupported) || errors.Is(err, ErrUnavailable) ||
		errors.Is(err, ErrRegionRestricted) || errors.Is(err, ErrAuthentication) ||
		errors.Is(err, ErrWrongPassword) || errors.Is(err, ErrTransportIsolation) ||
		errors.Is(err, ErrTransportProfile) {
		return false
	}
	if code, ok := extractorHTTPStatusCode(err); ok {
		return network.RetryableStatus(code)
	}
	return network.IsRetryableError(err)
}

func extractorHTTPStatusCode(err error) (int, bool) {
	var status *HTTPStatusError
	if errors.As(err, &status) {
		return status.Code, true
	}
	var statusErr *network.StatusError
	if errors.As(err, &statusErr) {
		return statusErr.Code, true
	}
	return 0, false
}

func extractorRetryMessage(err error) string {
	if code, ok := extractorHTTPStatusCode(err); ok {
		return fmt.Sprintf("transient HTTP status %d", code)
	}
	var requestErr *network.RequestError
	if errors.As(err, &requestErr) {
		var netErr net.Error
		if errors.As(requestErr.Err, &netErr) && netErr.Timeout() {
			return "network timeout"
		}
		return "transient network request failure"
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return "network timeout"
	}
	return "transient network failure"
}

func extractorRetryDelay(options DownloaderOptions, attempt int) time.Duration {
	base := options.RetryBaseDelay
	if base <= 0 {
		base = 25 * time.Millisecond
	}
	maximum := options.RetryMaxDelay
	if maximum <= 0 {
		maximum = time.Second
	}
	if base > maximum {
		return maximum
	}
	for index := 1; index < attempt; index++ {
		if base >= maximum || base > maximum/2 {
			return maximum
		}
		base *= 2
	}
	return base
}

func waitForExtractorRetry(ctx context.Context, delay time.Duration) error {
	if delay <= 0 {
		return nil
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
