// Package downloader implements resumable direct HTTP downloads.
package downloader

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/ytdlp-go/ytdlp/internal/events"
	"github.com/ytdlp-go/ytdlp/internal/network"
)

var (
	ErrDestinationExists = errors.New("destination already exists")
	ErrUnsafeDestination = errors.New("destination escapes output root")
	ErrIncomplete        = errors.New("download ended before expected size")
	ErrTooManyAttempts   = errors.New("download retry attempts exceed limit")
	ErrThrottled         = errors.New("download response remained below throttle threshold")
	ErrThrottleExhausted = errors.New("download throttle restart limit exhausted")
	ErrInvalidLimits     = errors.New("invalid download resource limits")
)

const (
	maxDirectBytes       = 8 << 30
	maxDirectAttempts    = 100
	maxDirectFileRetries = 10
	maxDirectRestarts    = 10
	maxDirectRetryDelay  = time.Minute
)

type Job struct {
	URL         string
	Headers     http.Header
	OutputRoot  string
	Destination string
	Overwrite   bool
	Attempts    int
	// RetryBaseDelay and RetryMaxDelay define a deterministic exponential
	// backoff. Zero values retain the intentionally small native defaults.
	RetryBaseDelay time.Duration
	RetryMaxDelay  time.Duration
	// RateLimit is an optional sustained byte/second limit. It is applied
	// while writing, so resumed downloads and unknown-length responses obey it.
	RateLimit int64
	// MaxBytes bounds a response even where the server omits Content-Length.
	// Zero means the direct downloader's conservative 8 GiB ceiling.
	MaxBytes int64
	// ThrottleRate enables slow-response detection when positive. A response
	// below this byte/second rate for ThrottleWindow is restarted resumably.
	ThrottleRate     int64
	ThrottleWindow   time.Duration
	ThrottleRestarts int
	// FileAttempts bounds retry of transient file open/sync/rename operations.
	FileAttempts int
}

type Result struct {
	Path    string
	Bytes   int64
	Resumed bool
}

type Downloader struct {
	transport network.Doer
	now       func() time.Time
	sleep     func(context.Context, time.Duration) error
}

func New(transport network.Doer) *Downloader {
	return NewWithHooks(transport, time.Now, waitFor)
}

// NewWithHooks supplies deterministic time hooks for native retry and
// throttling tests. Production callers should use New.
func NewWithHooks(transport network.Doer, now func() time.Time, sleep func(context.Context, time.Duration) error) *Downloader {
	if now == nil {
		now = time.Now
	}
	if sleep == nil {
		sleep = waitFor
	}
	return &Downloader{transport: transport, now: now, sleep: sleep}
}

type partialState struct {
	URL   string `json:"url"`
	ETag  string `json:"etag,omitempty"`
	Total int64  `json:"total,omitempty"`
}

func (downloader *Downloader) Download(ctx context.Context, job Job, sink events.Sink) (Result, error) {
	if err := validateJob(job); err != nil {
		return Result{}, err
	}
	if sink == nil {
		sink = events.Nop()
	}
	if err := os.MkdirAll(job.OutputRoot, 0o755); err != nil {
		return Result{}, fmt.Errorf("create output root: %w", err)
	}
	if err := validateDestination(job.OutputRoot, job.Destination); err != nil {
		return Result{}, err
	}
	if err := os.MkdirAll(filepath.Dir(job.Destination), 0o755); err != nil {
		return Result{}, fmt.Errorf("create output directory: %w", err)
	}
	if err := validateDestination(job.OutputRoot, job.Destination); err != nil {
		return Result{}, err
	}
	if info, err := os.Lstat(job.Destination); err == nil {
		if !info.Mode().IsRegular() {
			return Result{}, ErrUnsafeDestination
		}
		if !job.Overwrite {
			return Result{}, ErrDestinationExists
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return Result{}, fmt.Errorf("inspect destination: %w", err)
	}

	partPath := job.Destination + ".part"
	statePath := partPath + ".json"
	if err := regularOrAbsent(partPath); err != nil {
		return Result{}, err
	}
	if err := regularOrAbsent(statePath); err != nil {
		return Result{}, err
	}
	attempts := job.Attempts
	if attempts <= 0 {
		attempts = 3
	}
	if attempts > maxDirectAttempts {
		return Result{}, ErrTooManyAttempts
	}
	eventURL := network.RedactRawURL(job.URL)
	_ = sink.Emit(ctx, events.Event{Kind: events.KindStarting, URL: eventURL, Path: job.Destination})

	var result Result
	var lastErr error
	throttleRestarts := 0
	for attempt := 1; attempt <= attempts; attempt++ {
		result, lastErr = downloader.downloadAttempt(ctx, job, partPath, statePath, sink)
		if lastErr == nil {
			if info, err := os.Lstat(job.Destination); err == nil {
				if !info.Mode().IsRegular() {
					return Result{}, ErrUnsafeDestination
				}
				if !job.Overwrite {
					return Result{}, ErrDestinationExists
				}
			} else if !errors.Is(err, os.ErrNotExist) {
				return Result{}, fmt.Errorf("recheck destination: %w", err)
			}
			if err := downloader.finalize(ctx, job, partPath, job.Destination, job.Overwrite); err != nil {
				return Result{}, fmt.Errorf("finalize download: %w", err)
			}
			_ = os.Remove(statePath)
			result.Path = job.Destination
			_ = sink.Emit(ctx, events.Event{Kind: events.KindCompleted, URL: eventURL, Path: job.Destination, Bytes: result.Bytes, Total: result.Bytes, Resuming: result.Resumed})
			return result, nil
		}
		if ctx.Err() != nil {
			_ = sink.Emit(context.Background(), events.Event{Kind: events.KindCancelled, URL: eventURL, Path: job.Destination, Message: ctx.Err().Error()})
			return Result{}, ctx.Err()
		}
		if !isRetryable(lastErr) {
			break
		}
		if errors.Is(lastErr, ErrThrottled) {
			throttleRestarts++
			limit := job.ThrottleRestarts
			if limit <= 0 {
				limit = 2
			}
			if throttleRestarts > limit {
				return Result{}, fmt.Errorf("%w: %w", ErrThrottleExhausted, lastErr)
			}
		}
		if attempt < attempts {
			_ = sink.Emit(ctx, events.Event{Kind: events.KindRetry, URL: eventURL, Path: job.Destination, Attempt: attempt + 1, Message: lastErr.Error()})
			if err := downloader.sleep(ctx, retryDelay(job, attempt)); err != nil {
				return Result{}, err
			}
		}
	}
	return Result{}, lastErr
}

func (downloader *Downloader) finalize(ctx context.Context, job Job, partPath, destination string, overwrite bool) error {
	return downloader.retryFile(ctx, job, func() error { return finalizeOnce(partPath, destination, overwrite) })
}

func finalizeOnce(partPath, destination string, overwrite bool) error {
	if overwrite {
		return replaceDestination(partPath, destination)
	}
	return installDestination(partPath, destination)
}

func (downloader *Downloader) downloadAttempt(ctx context.Context, job Job, partPath, statePath string, sink events.Sink) (Result, error) {
	state, offset := loadPartial(partPath, statePath, job.URL)
	request, err := http.NewRequest(http.MethodGet, job.URL, nil)
	if err != nil {
		return Result{}, fmt.Errorf("create download request: %w", err)
	}
	if job.Headers != nil {
		request.Header = job.Headers.Clone()
	}
	if offset > 0 {
		request.Header.Set("Range", fmt.Sprintf("bytes=%d-", offset))
		if state.ETag != "" {
			request.Header.Set("If-Range", state.ETag)
		}
	}
	response, err := downloader.transport.Do(ctx, request)
	if err != nil {
		return Result{}, retryableError{err}
	}
	defer response.Body.Close()

	if response.StatusCode == http.StatusRequestedRangeNotSatisfiable && offset > 0 && state.Total > 0 && offset == state.Total {
		return Result{Bytes: offset, Resumed: true}, nil
	}
	if response.StatusCode != http.StatusOK && response.StatusCode != http.StatusPartialContent {
		err := fmt.Errorf("download HTTP status %d", response.StatusCode)
		if network.RetryableStatus(response.StatusCode) {
			return Result{}, retryableError{err}
		}
		return Result{}, err
	}
	resuming := offset > 0 && response.StatusCode == http.StatusPartialContent && validContentRange(response.Header.Get("Content-Range"), offset)
	if !resuming {
		offset = 0
		state = partialState{URL: job.URL}
	}
	state.ETag = response.Header.Get("ETag")
	state.Total = responseTotal(response, offset)
	maxBytes := job.MaxBytes
	if maxBytes <= 0 {
		maxBytes = maxDirectBytes
	}
	if state.Total > maxBytes {
		return Result{}, fmt.Errorf("%w: advertised %d bytes exceeds %d", ErrIncomplete, state.Total, maxBytes)
	}
	if err := downloader.savePartialState(ctx, job, statePath, state); err != nil {
		return Result{}, err
	}

	flags := os.O_CREATE | os.O_WRONLY
	if offset == 0 {
		flags |= os.O_TRUNC
	}
	file, err := downloader.openPartial(ctx, job, partPath, flags)
	if err != nil {
		return Result{}, fmt.Errorf("open partial file: %w", err)
	}
	if offset > 0 {
		if _, err := file.Seek(offset, io.SeekStart); err != nil {
			file.Close()
			return Result{}, fmt.Errorf("seek partial file: %w", err)
		}
	}

	written := offset
	limiter := newThrottle(job.RateLimit)
	detector := newThrottleDetector(job.ThrottleRate, job.ThrottleWindow, downloader.now)
	buffer := make([]byte, 64<<10)
	for {
		if err := ctx.Err(); err != nil {
			file.Close()
			return Result{}, err
		}
		count, readErr := response.Body.Read(buffer)
		if count > 0 {
			if detector.Observe(count) {
				file.Close()
				return Result{}, retryableError{ErrThrottled}
			}
			if written+int64(count) > maxBytes {
				file.Close()
				return Result{}, fmt.Errorf("%w: response exceeds %d bytes", ErrIncomplete, maxBytes)
			}
			if err := limiter.Wait(ctx, count); err != nil {
				file.Close()
				return Result{}, err
			}
			writtenCount, writeErr := file.Write(buffer[:count])
			written += int64(writtenCount)
			if writeErr != nil || writtenCount != count {
				file.Close()
				if writeErr == nil {
					writeErr = io.ErrShortWrite
				}
				return Result{}, fmt.Errorf("write partial file: %w", writeErr)
			}
			if err := sink.Emit(ctx, events.Event{Kind: events.KindProgress, URL: network.RedactRawURL(job.URL), Path: job.Destination, Bytes: written, Total: state.Total, Resuming: resuming}); err != nil {
				file.Close()
				return Result{}, fmt.Errorf("emit progress: %w", err)
			}
		}
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				break
			}
			file.Close()
			return Result{}, retryableError{fmt.Errorf("read download response: %w", readErr)}
		}
	}
	if err := downloader.retryFile(ctx, job, func() error { return file.Sync() }); err != nil {
		file.Close()
		return Result{}, fmt.Errorf("sync partial file: %w", err)
	}
	if err := file.Close(); err != nil {
		return Result{}, fmt.Errorf("close partial file: %w", err)
	}
	if state.Total > 0 && written != state.Total {
		return Result{}, retryableError{fmt.Errorf("%w: got %d, want %d bytes", ErrIncomplete, written, state.Total)}
	}
	return Result{Bytes: written, Resumed: resuming}, nil
}

func validateJob(job Job) error {
	if job.Attempts > maxDirectAttempts {
		return ErrTooManyAttempts
	}
	if job.Attempts < 0 || job.RetryBaseDelay < 0 || job.RetryMaxDelay < 0 || job.RetryBaseDelay > maxDirectRetryDelay || job.RetryMaxDelay > maxDirectRetryDelay || (job.RetryBaseDelay > 0 && job.RetryMaxDelay > 0 && job.RetryBaseDelay > job.RetryMaxDelay) || job.RateLimit < 0 || job.MaxBytes < 0 || job.MaxBytes > maxDirectBytes || job.ThrottleRate < 0 || job.ThrottleWindow < 0 || job.ThrottleWindow > maxDirectRetryDelay || job.ThrottleRestarts < 0 || job.ThrottleRestarts > maxDirectRestarts || job.FileAttempts < 0 || job.FileAttempts > maxDirectFileRetries {
		return ErrInvalidLimits
	}
	return nil
}

func retryDelay(job Job, attempt int) time.Duration {
	base := job.RetryBaseDelay
	if base <= 0 {
		base = 25 * time.Millisecond
	}
	max := job.RetryMaxDelay
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

type retryableError struct{ error }

func (err retryableError) Unwrap() error { return err.error }

func isRetryable(err error) bool {
	var target retryableError
	return errors.As(err, &target)
}

func validateDestination(root, destination string) error {
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return err
	}
	destinationAbs, err := filepath.Abs(destination)
	if err != nil {
		return err
	}
	relative, err := filepath.Rel(rootAbs, destinationAbs)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return ErrUnsafeDestination
	}
	current := rootAbs
	components := strings.Split(filepath.Dir(relative), string(filepath.Separator))
	for _, component := range components {
		if component == "" || component == "." {
			continue
		}
		current = filepath.Join(current, component)
		if symlink(current) {
			return ErrUnsafeDestination
		}
	}
	return nil
}

func symlink(path string) bool {
	info, err := os.Lstat(path)
	return err == nil && info.Mode()&os.ModeSymlink != 0
}

func regularOrAbsent(path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return ErrUnsafeDestination
	}
	return nil
}

func loadPartial(partPath, statePath, rawURL string) (partialState, int64) {
	info, err := os.Stat(partPath)
	if err != nil || info.Size() <= 0 {
		return partialState{URL: rawURL}, 0
	}
	encoded, err := os.ReadFile(statePath)
	if err != nil {
		return partialState{URL: rawURL}, 0
	}
	var state partialState
	if json.Unmarshal(encoded, &state) != nil || state.URL != rawURL {
		return partialState{URL: rawURL}, 0
	}
	return state, info.Size()
}

func (downloader *Downloader) savePartialState(ctx context.Context, job Job, path string, state partialState) error {
	return downloader.retryFile(ctx, job, func() error { return savePartialStateOnce(path, state) })
}

func savePartialStateOnce(path string, state partialState) error {
	encoded, err := json.Marshal(state)
	if err != nil {
		return err
	}
	if err := regularOrAbsent(path); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("create partial state: %w", err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if _, err = temporary.Write(encoded); err == nil {
		err = temporary.Sync()
	}
	if closeErr := temporary.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return fmt.Errorf("write partial state: %w", err)
	}
	if err := regularOrAbsent(path); err != nil {
		return err
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		// Windows cannot replace an existing state file. State metadata is
		// recoverable from the partial payload, so a checked regular-file
		// replacement is safe here (unlike replacing the final media output).
		if replaceErr := regularOrAbsent(path); replaceErr != nil {
			return replaceErr
		}
		if removeErr := os.Remove(path); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
			return fmt.Errorf("replace partial state: %w", removeErr)
		}
		if retryErr := os.Rename(temporaryPath, path); retryErr != nil {
			return fmt.Errorf("finalize partial state: %w", retryErr)
		}
	}
	return nil
}

func validContentRange(header string, offset int64) bool {
	return strings.HasPrefix(header, "bytes "+strconv.FormatInt(offset, 10)+"-")
}

func responseTotal(response *http.Response, offset int64) int64 {
	if response.StatusCode == http.StatusPartialContent {
		if slash := strings.LastIndexByte(response.Header.Get("Content-Range"), '/'); slash >= 0 {
			if total, err := strconv.ParseInt(response.Header.Get("Content-Range")[slash+1:], 10, 64); err == nil {
				return total
			}
		}
	}
	if response.ContentLength >= 0 {
		return offset + response.ContentLength
	}
	return 0
}
