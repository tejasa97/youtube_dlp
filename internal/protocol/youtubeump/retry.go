package youtubeump

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/tejasa97/ytdlp-go/internal/network"
)

type retryableError struct{ error }

func (err retryableError) Unwrap() error { return err.error }

func isRetryable(err error) bool {
	var target retryableError
	return errors.As(err, &target)
}

func retryDelay(config Config, attempt int) time.Duration {
	base := config.RetryBaseDelay
	if base <= 0 {
		base = 25 * time.Millisecond
	}
	max := config.RetryMaxDelay
	if max <= 0 {
		max = time.Second
	}
	if base > max {
		return max
	}
	for index := 1; index < attempt; index++ {
		if base >= max || base > max/2 {
			return max
		}
		base *= 2
	}
	return base
}

func sleep(ctx context.Context, delay time.Duration) error {
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

func requestFailure(err error, serverURL string) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%w: %s: %w", ErrDownloadFailed, redactURL(serverURL), err)
}

func responseFailure(status int, serverURL string) error {
	if network.RetryableStatus(status) {
		return retryableError{fmt.Errorf("%w: %s", ErrDownloadFailed, redactURL(serverURL))}
	}
	return fmt.Errorf("%w: %s", ErrDownloadFailed, redactURL(serverURL))
}

func redirectFailure(serverURL string) error {
	return fmt.Errorf("%w: %s", ErrRedirect, redactURL(serverURL))
}

func isRedirectResponse(response *http.Response) bool {
	switch response.StatusCode {
	case http.StatusMovedPermanently, http.StatusFound, http.StatusSeeOther,
		http.StatusTemporaryRedirect, http.StatusPermanentRedirect:
		return response.Header.Get("Location") != ""
	default:
		return false
	}
}
