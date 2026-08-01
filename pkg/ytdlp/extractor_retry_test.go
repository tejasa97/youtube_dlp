package ytdlp

import (
	"context"
	"errors"
	"net/url"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ytdlp-go/ytdlp/internal/extractor"
	"github.com/ytdlp-go/ytdlp/internal/network"
)

func TestExtractorRetryRetriesTransientErrorsInOrderAndRedactsEvents(t *testing.T) {
	fixture := &retryTestExtractor{failures: []error{
		&network.StatusError{Code: 500, URL: "https://media.invalid/watch?token=must-not-leak"},
	}}
	var events []Event
	operation := &operation{
		client: NewClient(WithEventHandler(func(_ context.Context, event Event) error {
			events = append(events, event)
			return nil
		})),
		request: Request{ExtractorRetries: 2, Downloader: DownloaderOptions{RetryBaseDelay: time.Millisecond, RetryMaxDelay: time.Millisecond}},
		extractorRetryWait: func(_ context.Context, delay time.Duration) error {
			if delay != time.Millisecond {
				t.Fatalf("retry delay = %s, want 1ms", delay)
			}
			return nil
		},
	}
	_, err := operation.extractWithRetry(context.Background(), fixture, extractor.Request{URL: "https://media.invalid/watch?sig=secret"}, "https://media.invalid/watch?sig=REDACTED")
	if err != nil {
		t.Fatal(err)
	}
	if fixture.calls.Load() != 2 || len(events) != 1 {
		t.Fatalf("calls=%d events=%#v", fixture.calls.Load(), events)
	}
	if events[0].Kind != EventExtractorRetry || events[0].Attempt != 1 || events[0].URL != "https://media.invalid/watch?sig=REDACTED" || events[0].Message != "transient HTTP status 500" {
		t.Fatalf("retry event = %#v", events[0])
	}
}

func TestExtractorRetryClassificationRejectsPermanentAndCategorizedErrors(t *testing.T) {
	tests := []struct {
		name string
		err  error
	}{
		{name: "unsupported", err: extractor.ErrUnsupported},
		{name: "authentication", err: extractor.ErrAuthentication},
		{name: "geo", err: extractor.ErrRegionRestricted},
		{name: "unavailable", err: extractor.ErrUnavailable},
		{name: "invalid", err: extractor.ErrInvalidMetadata},
		{name: "permanent status", err: &network.StatusError{Code: 404}},
		{name: "categorized auth", err: &Error{Category: ErrorAuthentication, Err: &network.StatusError{Code: 500}}},
		{name: "cancellation", err: context.Canceled},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := &retryTestExtractor{failures: []error{test.err}}
			operation := &operation{client: NewClient(), request: Request{ExtractorRetries: 5}}
			_, err := operation.extractWithRetry(context.Background(), fixture, extractor.Request{URL: "https://media.invalid/video"}, "https://media.invalid/video")
			if !errors.Is(err, test.err) || fixture.calls.Load() != 1 {
				t.Fatalf("error=%v calls=%d", err, fixture.calls.Load())
			}
		})
	}
}

func TestExtractorRetryCancellationStopsBeforeNextEnteredOperation(t *testing.T) {
	fixture := &retryTestExtractor{failures: []error{&network.StatusError{Code: 503}}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	operation := &operation{
		client: NewClient(), request: Request{ExtractorRetries: 3},
		extractorRetryWait: func(waitCtx context.Context, _ time.Duration) error {
			cancel()
			return waitCtx.Err()
		},
	}
	_, err := operation.extractWithRetry(ctx, fixture, extractor.Request{URL: "https://media.invalid/video"}, "https://media.invalid/video")
	if !errors.Is(err, context.Canceled) || fixture.calls.Load() != 1 {
		t.Fatalf("error=%v calls=%d", err, fixture.calls.Load())
	}
}

func TestExtractorRetryIsBoundedAndUsesDeterministicBackoff(t *testing.T) {
	fixture := &retryTestExtractor{failures: []error{
		&network.StatusError{Code: 500}, &network.StatusError{Code: 500}, &network.StatusError{Code: 500}, &network.StatusError{Code: 500},
	}}
	var delays []time.Duration
	operation := &operation{
		client:  NewClient(),
		request: Request{ExtractorRetries: 3, Downloader: DownloaderOptions{RetryBaseDelay: time.Millisecond, RetryMaxDelay: 2 * time.Millisecond}},
		extractorRetryWait: func(_ context.Context, delay time.Duration) error {
			delays = append(delays, delay)
			return nil
		},
	}
	_, err := operation.extractWithRetry(context.Background(), fixture, extractor.Request{URL: "https://media.invalid/video"}, "https://media.invalid/video")
	if err == nil || fixture.calls.Load() != 4 || !reflect.DeepEqual(delays, []time.Duration{time.Millisecond, 2 * time.Millisecond, 2 * time.Millisecond}) {
		t.Fatalf("error=%v calls=%d delays=%v", err, fixture.calls.Load(), delays)
	}
}

type retryTestExtractor struct {
	failures []error
	calls    atomic.Int32
}

func (*retryTestExtractor) Name() string { return "retry-test" }

func (*retryTestExtractor) Suitable(*url.URL) bool { return true }

func (fixture *retryTestExtractor) Extract(context.Context, extractor.Request) (extractor.Extraction, error) {
	attempt := int(fixture.calls.Add(1)) - 1
	if attempt < len(fixture.failures) && fixture.failures[attempt] != nil {
		return extractor.Extraction{}, fixture.failures[attempt]
	}
	return extractor.Extraction{}, nil
}
