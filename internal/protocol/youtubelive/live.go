package youtubelive

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/ytdlp-go/ytdlp/internal/events"
	"github.com/ytdlp-go/ytdlp/internal/fragment"
)

var (
	ErrLiveInvalidConfig  = errors.New("invalid YouTube live downloader configuration")
	ErrLiveProbeFailed    = errors.New("YouTube live sequence probe failed")
	ErrLiveHeadSequence   = errors.New("invalid or missing live X-Head-Seqnum")
	ErrLiveNoProgress     = errors.New("YouTube live stream made no progress")
	ErrLivePollLimit      = errors.New("YouTube live polling limit exceeded")
	ErrLiveRefreshFailed  = errors.New("YouTube live URL refresh failed")
	ErrLiveDownloadFailed = errors.New("YouTube live segment download failed")
)

const (
	defaultLivePollInterval       = 5 * time.Second
	defaultLiveRefreshInterval    = 5 * time.Hour
	defaultLiveMaxPolls           = 10_000
	defaultLiveMaxNoProgress      = 12
	defaultLiveMaxProbeFailures   = 3
	defaultLiveAggressiveRefresh  = 3
	defaultLiveMaxRefreshFailures = 3
	maxLivePolls                  = 100_000
	maxLiveNoProgress             = 10_000
	maxLiveRefreshInterval        = 24 * time.Hour
)

// LiveRefreshRequest describes the expiring adaptive URL currently in use.
// URL and Headers may contain credentials and must not be logged.
type LiveRefreshRequest struct {
	URL     string
	Headers http.Header
}

// LiveRefreshResult supplies a renewed adaptive URL. StillLive=false is a
// terminal, successful transition and causes the already downloaded media to
// be published.
type LiveRefreshResult struct {
	URL       string
	Headers   http.Header
	StillLive bool
}

// LiveRefreshFunc renews an expiring adaptive URL and reports whether the
// broadcast is still active.
type LiveRefreshFunc func(context.Context, LiveRefreshRequest) (LiveRefreshResult, error)

// LiveConfig bounds active polling, URL refresh, network and storage work.
type LiveConfig struct {
	Headers http.Header

	PollInterval       time.Duration
	RefreshInterval    time.Duration
	MaxPolls           int
	MaxNoProgressPolls int
	MaxProbeFailures   int
	AggressiveRefresh  int
	MaxRefreshFailures int
	Refresh            LiveRefreshFunc

	MaxSegments    int
	MaxSegmentSize int64
	Attempts       int
	RetryBaseDelay time.Duration
	RetryMaxDelay  time.Duration

	TargetDuration     time.Duration
	LiveStartTimestamp int64
	Now                func() time.Time
	Wait               func(context.Context, time.Duration) error
}

type LiveDownloader struct {
	transport Transport
	config    LiveConfig
}

// LiveResult is an atomically published active-live artifact.
type LiveResult struct {
	Path     string
	Bytes    int64
	Segments int
	Last     int64
}

func NewLiveDownloader(transport Transport, config LiveConfig) *LiveDownloader {
	config.Headers = config.Headers.Clone()
	return &LiveDownloader{transport: transport, config: config}
}

// Download follows an active adaptive URL from its first available sequence.
// Every observed head H is inclusive, so a first head of 2 downloads sq=0,
// sq=1 and sq=2. Polling and refresh are bounded and injectable for tests.
func (downloader *LiveDownloader) Download(ctx context.Context, baseURL, outputRoot, destination string, overwrite bool, sink events.Sink) (result LiveResult, err error) {
	config, err := downloader.normalizedConfig()
	if err != nil {
		return LiveResult{}, err
	}
	if _, err := parseBaseURL(baseURL); err != nil {
		return LiveResult{}, err
	}
	if err := validateDestination(outputRoot, destination); err != nil {
		return LiveResult{}, err
	}
	if err := liveOutputPreflight(destination, overwrite); err != nil {
		return LiveResult{}, err
	}
	if sink == nil {
		sink = events.Nop()
	}
	sink = categorizeSink(sink)
	eventURL := redactMediaEventURL(baseURL)
	if err := sink.Emit(ctx, events.Event{Kind: events.KindStarting, URL: eventURL, Path: destination}); err != nil {
		return LiveResult{}, err
	}

	workDir := destination + ".live.fragments"
	temporary := destination + ".part"
	published := false
	defer func() {
		if !published {
			_ = os.Remove(temporary)
			_ = os.RemoveAll(workDir)
		}
	}()
	if err := prepareLiveWorkDir(workDir); err != nil {
		return LiveResult{}, err
	}

	currentURL := baseURL
	currentHeaders := config.Headers.Clone()
	nextSequence := int64(0)
	lastSequence := int64(-1)
	noProgress := 0
	probeFailures := 0
	refreshFailures := 0
	polls := 0
	lastRefresh := config.Now()
	stopAfterProbe := false
	clampWarningEmitted := false

	for {
		if polls >= config.MaxPolls {
			return LiveResult{}, ErrLivePollLimit
		}
		if config.Refresh != nil && config.Now().Sub(lastRefresh) >= config.RefreshInterval {
			var refreshErr error
			var ended bool
			currentURL, currentHeaders, ended, refreshErr = refreshLive(ctx, config.Refresh, currentURL, currentHeaders)
			if refreshErr != nil {
				if ctx.Err() != nil {
					downloader.emitLiveCancellation(ctx, sink, eventURL, destination)
					return LiveResult{}, ctx.Err()
				}
				refreshFailures++
				if refreshFailures >= config.MaxRefreshFailures {
					return LiveResult{}, ErrLiveRefreshFailed
				}
			} else {
				refreshFailures = 0
				lastRefresh = config.Now()
			}
			stopAfterProbe = ended
		}

		head, probeErr := probeLiveHead(ctx, downloader.transport, currentURL, currentHeaders)
		polls++
		if probeErr != nil {
			if ctx.Err() != nil {
				downloader.emitLiveCancellation(ctx, sink, eventURL, destination)
				return LiveResult{}, ctx.Err()
			}
			probeFailures++
			noProgress++
		} else {
			probeFailures = 0
			if lastSequence < 0 {
				nextSequence = liveBeginSequence(head, config.TargetDuration, config.LiveStartTimestamp, config.Now())
				if nextSequence > 0 && !clampWarningEmitted {
					if err := sink.Emit(ctx, events.Event{
						Kind: events.KindMetadataWarning, URL: eventURL, Path: destination,
						Message: "starting from YouTube's last 120 hours of available live media",
					}); err != nil {
						return LiveResult{}, err
					}
					clampWarningEmitted = true
				}
			}
			if head >= nextSequence {
				remaining := int64(config.MaxSegments - result.Segments)
				if head-nextSequence >= remaining {
					return LiveResult{}, errors.Join(ErrLiveDownloadFailed, fragment.ErrTooManySegments)
				}
				for sequence := nextSequence; ; sequence++ {
					if err := downloader.fetchLiveSegment(ctx, currentURL, currentHeaders, workDir, destination, sequence, result.Segments, config, sink); err != nil {
						if ctx.Err() != nil {
							downloader.emitLiveCancellation(ctx, sink, eventURL, destination)
							return LiveResult{}, ctx.Err()
						}
						return LiveResult{}, safeLiveError(err)
					}
					result.Segments++
					lastSequence = sequence
					if sequence == head {
						break
					}
				}
				nextSequence = head + 1
				noProgress = 0
			} else {
				noProgress++
			}
		}

		// A refresh that observes the active-to-ended transition retains the
		// active URL feed for one final probe. This collects sq through the
		// final head inclusively before stopping.
		if stopAfterProbe {
			break
		}
		if noProgress >= config.AggressiveRefresh && config.Refresh != nil {
			var refreshErr error
			var ended bool
			currentURL, currentHeaders, ended, refreshErr = refreshLive(ctx, config.Refresh, currentURL, currentHeaders)
			if refreshErr != nil {
				if ctx.Err() != nil {
					downloader.emitLiveCancellation(ctx, sink, eventURL, destination)
					return LiveResult{}, ctx.Err()
				}
				refreshFailures++
				if refreshFailures >= config.MaxRefreshFailures {
					return LiveResult{}, ErrLiveRefreshFailed
				}
			} else {
				refreshFailures = 0
				lastRefresh = config.Now()
			}
			if ended {
				stopAfterProbe = true
				continue
			}
		}
		if probeFailures >= config.MaxProbeFailures {
			return LiveResult{}, safeLiveError(probeErr)
		}
		if probeErr != nil {
			if err := sink.Emit(ctx, events.Event{
				Kind: events.KindRetry, URL: eventURL, Path: destination,
				Attempt: probeFailures + 1, Message: "transient live sequence probe failure",
			}); err != nil {
				return LiveResult{}, err
			}
		}
		if noProgress >= config.MaxNoProgressPolls {
			return LiveResult{}, ErrLiveNoProgress
		}
		if err := config.Wait(ctx, config.PollInterval); err != nil {
			downloader.emitLiveCancellation(ctx, sink, eventURL, destination)
			return LiveResult{}, err
		}
	}

	if result.Segments == 0 {
		return LiveResult{}, errors.Join(ErrLiveDownloadFailed, fragment.ErrNoSegments)
	}
	bytesWritten, err := assembleLive(workDir, result.Segments, temporary, destination, overwrite)
	if err != nil {
		return LiveResult{}, safeLiveError(err)
	}
	if err := os.RemoveAll(workDir); err != nil {
		return LiveResult{}, safeLiveError(err)
	}
	published = true
	result.Path = destination
	result.Bytes = bytesWritten
	result.Last = lastSequence
	_ = sink.Emit(ctx, events.Event{Kind: events.KindCompleted, URL: eventURL, Path: destination, Bytes: bytesWritten, Fragments: result.Segments})
	return result, nil
}

func (downloader *LiveDownloader) normalizedConfig() (LiveConfig, error) {
	if downloader == nil || downloader.transport == nil {
		return LiveConfig{}, ErrLiveInvalidConfig
	}
	config := downloader.config
	config.Headers = config.Headers.Clone()
	if config.PollInterval == 0 {
		config.PollInterval = defaultLivePollInterval
	}
	if config.RefreshInterval == 0 {
		config.RefreshInterval = defaultLiveRefreshInterval
	}
	if config.MaxPolls == 0 {
		config.MaxPolls = defaultLiveMaxPolls
	}
	if config.MaxNoProgressPolls == 0 {
		config.MaxNoProgressPolls = defaultLiveMaxNoProgress
	}
	if config.MaxProbeFailures == 0 {
		config.MaxProbeFailures = defaultLiveMaxProbeFailures
	}
	if config.AggressiveRefresh == 0 {
		config.AggressiveRefresh = defaultLiveAggressiveRefresh
		if config.AggressiveRefresh > config.MaxNoProgressPolls {
			config.AggressiveRefresh = config.MaxNoProgressPolls
		}
	}
	if config.MaxRefreshFailures == 0 {
		config.MaxRefreshFailures = defaultLiveMaxRefreshFailures
	}
	if config.MaxSegments == 0 {
		config.MaxSegments = defaultMaxSegments
	}
	if config.MaxSegmentSize == 0 {
		config.MaxSegmentSize = 64 << 20
	}
	if config.Attempts == 0 {
		config.Attempts = 3
	}
	if config.Now == nil {
		config.Now = time.Now
	}
	if config.Wait == nil {
		config.Wait = waitLive
	}
	if config.PollInterval < 0 || config.PollInterval > time.Hour ||
		config.RefreshInterval <= 0 || config.RefreshInterval > maxLiveRefreshInterval ||
		config.MaxPolls <= 0 || config.MaxPolls > maxLivePolls ||
		config.MaxNoProgressPolls <= 0 || config.MaxNoProgressPolls > maxLiveNoProgress ||
		config.MaxProbeFailures <= 0 || config.MaxProbeFailures > maxLiveNoProgress ||
		config.AggressiveRefresh <= 0 || config.AggressiveRefresh > config.MaxNoProgressPolls ||
		config.MaxRefreshFailures <= 0 || config.MaxRefreshFailures > maxLiveNoProgress ||
		config.MaxSegments <= 0 || config.MaxSegments > maxSegments ||
		config.MaxSegmentSize <= 0 || config.MaxSegmentSize > maxSegmentSize ||
		config.Attempts <= 0 || config.Attempts > maxAttempts ||
		config.RetryBaseDelay < 0 || config.RetryMaxDelay < 0 ||
		config.RetryBaseDelay > maxRetryDelay || config.RetryMaxDelay > maxRetryDelay ||
		(config.RetryBaseDelay > 0 && config.RetryMaxDelay > 0 && config.RetryBaseDelay > config.RetryMaxDelay) ||
		config.TargetDuration <= 0 || config.TargetDuration > maxTargetDuration {
		return LiveConfig{}, ErrLiveInvalidConfig
	}
	return config, nil
}

func probeLiveHead(ctx context.Context, transport Transport, rawURL string, headers http.Header) (int64, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return 0, ErrInvalidBaseURL
	}
	request.Header = cloneLiveHeaders(headers)
	response, err := transport.Do(ctx, request)
	if err != nil {
		return 0, ErrLiveProbeFailed
	}
	if response == nil || response.Body == nil {
		return 0, ErrLiveProbeFailed
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return 0, ErrLiveProbeFailed
	}
	head, err := parseHeadSequence(response.Header.Get("X-Head-Seqnum"))
	if err != nil || head == int64(^uint64(0)>>1) {
		return 0, errors.Join(ErrLiveProbeFailed, ErrLiveHeadSequence)
	}
	return head, nil
}

func refreshLive(ctx context.Context, refresh LiveRefreshFunc, rawURL string, headers http.Header) (string, http.Header, bool, error) {
	result, err := refresh(ctx, LiveRefreshRequest{URL: rawURL, Headers: headers.Clone()})
	if err != nil {
		if ctx.Err() != nil {
			return rawURL, headers, false, ctx.Err()
		}
		return rawURL, headers, false, ErrLiveRefreshFailed
	}
	if !result.StillLive {
		if result.URL == "" {
			return rawURL, headers, true, nil
		}
		if _, err := parseBaseURL(result.URL); err != nil {
			return rawURL, headers, false, ErrLiveRefreshFailed
		}
		return result.URL, result.Headers.Clone(), true, nil
	}
	if _, err := parseBaseURL(result.URL); err != nil {
		return rawURL, headers, false, ErrLiveRefreshFailed
	}
	return result.URL, result.Headers.Clone(), false, nil
}

func liveBeginSequence(head int64, targetDuration time.Duration, liveStartTimestamp int64, now time.Time) int64 {
	if liveStartTimestamp == 0 || !now.After(time.Unix(liveStartTimestamp, 0).Add(maxAvailableAge)) {
		return 0
	}
	available := int64(maxAvailableAge / targetDuration)
	begin := head - available + 1
	if begin < 0 {
		return 0
	}
	return begin
}

func (downloader *LiveDownloader) fetchLiveSegment(ctx context.Context, baseURL string, headers http.Header, workDir, destination string, sequence int64, index int, config LiveConfig, sink events.Sink) error {
	parsed, err := parseBaseURL(baseURL)
	if err != nil {
		return err
	}
	rawURL, err := sequenceURL(parsed, sequence)
	if err != nil {
		return err
	}
	eventURL := redactMediaEventURL(rawURL)
	if err := sink.Emit(ctx, events.Event{Kind: events.KindFragmentStarting, URL: eventURL, Path: destination, Fragment: index + 1}); err != nil {
		return err
	}
	path := filepath.Join(workDir, fmt.Sprintf("%08d.frag", index))
	var lastErr error
	for attempt := 1; attempt <= config.Attempts; attempt++ {
		lastErr = fetchLiveOnce(ctx, downloader.transport, rawURL, headers, path, config.MaxSegmentSize)
		if lastErr == nil {
			break
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if errors.Is(lastErr, fragment.ErrSegmentTooLarge) {
			return lastErr
		}
		if attempt < config.Attempts {
			if err := sink.Emit(ctx, events.Event{Kind: events.KindRetry, URL: eventURL, Path: destination, Fragment: index + 1, Attempt: attempt + 1, Message: "transient live fragment failure"}); err != nil {
				return err
			}
			if err := config.Wait(ctx, retryDelay(Config{RetryBaseDelay: config.RetryBaseDelay, RetryMaxDelay: config.RetryMaxDelay}, attempt)); err != nil {
				return err
			}
		}
	}
	if lastErr != nil {
		return lastErr
	}
	return sink.Emit(ctx, events.Event{Kind: events.KindFragmentCompleted, URL: eventURL, Path: destination, Fragment: index + 1})
}

func fetchLiveOnce(ctx context.Context, transport Transport, rawURL string, headers http.Header, destination string, maxSize int64) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return ErrLiveDownloadFailed
	}
	request.Header = cloneLiveHeaders(headers)
	response, err := transport.Do(ctx, request)
	if err != nil {
		return ErrLiveDownloadFailed
	}
	if response == nil || response.Body == nil {
		return ErrLiveDownloadFailed
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return ErrLiveDownloadFailed
	}
	file, err := os.OpenFile(destination+".tmp", os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return ErrLiveDownloadFailed
	}
	written, copyErr := io.Copy(file, io.LimitReader(response.Body, maxSize+1))
	closeErr := file.Close()
	if copyErr != nil || closeErr != nil || written > maxSize {
		_ = os.Remove(destination + ".tmp")
		if written > maxSize {
			return fragment.ErrSegmentTooLarge
		}
		return ErrLiveDownloadFailed
	}
	if written == 0 {
		_ = os.Remove(destination + ".tmp")
		return ErrLiveDownloadFailed
	}
	if err := os.Rename(destination+".tmp", destination); err != nil {
		_ = os.Remove(destination + ".tmp")
		return ErrLiveDownloadFailed
	}
	return nil
}

func liveOutputPreflight(destination string, overwrite bool) error {
	for _, path := range []string{destination, destination + ".part", destination + ".live.fragments"} {
		info, err := os.Lstat(path)
		if err == nil {
			if info.Mode()&os.ModeSymlink != 0 {
				return ErrUnsafeOutput
			}
			if path != destination || !overwrite {
				return ErrOutputExists
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return ErrUnsafeOutput
		}
	}
	return nil
}

func prepareLiveWorkDir(path string) error {
	if err := os.RemoveAll(path); err != nil {
		return ErrUnsafeOutput
	}
	if err := os.MkdirAll(path, 0o700); err != nil {
		return ErrUnsafeOutput
	}
	return nil
}

func assembleLive(workDir string, count int, temporary, destination string, overwrite bool) (int64, error) {
	output, err := os.OpenFile(temporary, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		return 0, err
	}
	var total int64
	for index := 0; index < count; index++ {
		input, openErr := os.Open(filepath.Join(workDir, fmt.Sprintf("%08d.frag", index)))
		if openErr != nil {
			_ = output.Close()
			return 0, openErr
		}
		written, copyErr := io.Copy(output, input)
		closeErr := input.Close()
		total += written
		if copyErr != nil || closeErr != nil {
			_ = output.Close()
			return 0, errors.Join(copyErr, closeErr)
		}
	}
	if err := output.Sync(); err != nil {
		_ = output.Close()
		return 0, err
	}
	if err := output.Close(); err != nil {
		return 0, err
	}
	if err := os.Rename(temporary, destination); err == nil {
		return total, nil
	} else if !overwrite {
		return 0, err
	}
	if err := os.Remove(destination); err != nil && !errors.Is(err, os.ErrNotExist) {
		return 0, err
	}
	return total, os.Rename(temporary, destination)
}

func safeLiveError(err error) error {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	if errors.Is(err, ErrLiveHeadSequence) {
		return errors.Join(ErrLiveDownloadFailed, ErrLiveProbeFailed, ErrLiveHeadSequence)
	}
	for _, sentinel := range []error{
		ErrLiveProbeFailed, ErrLivePollLimit, ErrLiveNoProgress, ErrLiveRefreshFailed,
		fragment.ErrNoSegments, fragment.ErrTooManySegments, fragment.ErrSegmentTooLarge,
		ErrEventSink,
	} {
		if errors.Is(err, sentinel) {
			return errors.Join(ErrLiveDownloadFailed, sentinel)
		}
	}
	return ErrLiveDownloadFailed
}

func waitLive(ctx context.Context, duration time.Duration) error {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func cloneLiveHeaders(headers http.Header) http.Header {
	cloned := headers.Clone()
	if cloned == nil {
		cloned = make(http.Header)
	}
	return cloned
}

func (downloader *LiveDownloader) emitLiveCancellation(ctx context.Context, sink events.Sink, eventURL, destination string) {
	_ = sink.Emit(context.WithoutCancel(ctx), events.Event{
		Kind: events.KindCancelled, URL: eventURL, Path: destination, Message: "live-from-start download cancelled",
	})
}
