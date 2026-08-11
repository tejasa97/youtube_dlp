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

	"github.com/tejasa97/youtube_dlp/internal/atomicfile"
	"github.com/tejasa97/youtube_dlp/internal/events"
	"github.com/tejasa97/youtube_dlp/internal/network"
)

var (
	ErrDestinationExists = errors.New("destination already exists")
	ErrUnsafeDestination = errors.New("destination escapes output root")
	ErrIncomplete        = errors.New("download ended before expected size")
	ErrTooManyAttempts   = errors.New("download retry attempts exceed limit")
	ErrThrottled         = errors.New("download response remained below throttle threshold")
	ErrThrottleExhausted = errors.New("download throttle restart limit exhausted")
	ErrInvalidLimits     = errors.New("invalid download resource limits")
	// ErrFileSizeAbort reports a download rejected by the --min-filesize or
	// --max-filesize boundary before transfer, mirroring yt-dlp's
	// downloader/http.py abort. It is deliberately not retryable.
	ErrFileSizeAbort = errors.New("download aborted by file size limit")
)

// FileSizeAbortError carries the exact reference diagnostic for a
// min/max-filesize abort: for example
// "File is smaller than min-filesize (100 bytes < 200 bytes)".
type FileSizeAbortError struct {
	Message string
}

func (err *FileSizeAbortError) Error() string { return err.Message }
func (err *FileSizeAbortError) Unwrap() error { return ErrFileSizeAbort }

// HTTPStatusError identifies a non-success response rejected by the direct
// downloader without exposing its potentially sensitive URL.
type HTTPStatusError struct{ Code int }

func (err *HTTPStatusError) Error() string {
	return fmt.Sprintf("download HTTP status %d", err.Code)
}

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
	// ResumeIdentity is an optional caller-owned stable media identity. When
	// present, a partial may resume after an expiring media URL is refreshed,
	// provided the identity still matches. Empty retains exact-URL matching.
	ResumeIdentity string
	Overwrite      bool
	Attempts       int
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
	// MinFilesize and MaxFilesize reject a response before transfer when its
	// advertised Content-Length (plus the resume offset) falls outside the
	// bounds. Checks are skipped when Content-Encoding is present or the
	// length is unknown, matching yt-dlp's downloader/http.py. Zero disables.
	MinFilesize int64
	MaxFilesize int64
	// ThrottleRate enables slow-response detection when positive. A response
	// below this byte/second rate for ThrottleWindow is restarted resumably.
	ThrottleRate     int64
	ThrottleWindow   time.Duration
	ThrottleRestarts int
	// FileAttempts bounds retry of transient file open/sync/rename operations.
	FileAttempts int
	// NoContinue discards existing partial downloads before starting.
	NoContinue bool
	// NoPart writes in-progress bytes directly to Destination instead of
	// using a .part temporary file.
	NoPart bool
	// Checkpoint opts this direct transfer into durable partial progress. A
	// nil value preserves legacy file-length resume behavior.
	Checkpoint *CheckpointOptions
}

type Result struct {
	Path    string
	Bytes   int64
	Resumed bool
}

type Downloader struct {
	transport          network.Doer
	now                func() time.Time
	sleep              func(context.Context, time.Duration) error
	writePartialState  func(string, partialState) error
	syncPartialPayload func(*os.File) error
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
	return &Downloader{
		transport:          transport,
		now:                now,
		sleep:              sleep,
		writePartialState:  savePartialStateOnce,
		syncPartialPayload: (*os.File).Sync,
	}
}

type partialState struct {
	URL            string `json:"url,omitempty"`
	ResumeIdentity string `json:"resumeIdentity,omitempty"`
	ETag           string `json:"etag,omitempty"`
	LastModified   string `json:"lastModified,omitempty"`
	Total          int64  `json:"total,omitempty"`
	CommittedBytes int64  `json:"committedBytes,omitempty"`
}

func (downloader *Downloader) Download(ctx context.Context, job Job, sink events.Sink) (Result, error) {
	if err := validateJob(job); err != nil {
		return Result{}, err
	}
	plan, err := checkpointPlanForJob(job)
	if err != nil {
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
	if job.NoPart {
		partPath = job.Destination
		statePath = job.Destination + ".part.json"
	}
	if job.NoContinue {
		_ = os.Remove(partPath)
		_ = os.Remove(statePath)
	}
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
		result, lastErr = downloader.downloadAttempt(ctx, job, plan, partPath, statePath, sink)
		if lastErr == nil {
			if plan.enabled {
				if err := ctx.Err(); err != nil {
					_ = sink.Emit(context.Background(), events.Event{Kind: events.KindCancelled, URL: eventURL, Path: job.Destination, Message: err.Error()})
					return Result{}, err
				}
			}
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
			if partPath != job.Destination {
				if err := downloader.finalize(ctx, job, partPath, job.Destination, job.Overwrite); err != nil {
					return Result{}, fmt.Errorf("finalize download: %w", err)
				}
			}
			_ = os.Remove(statePath)
			result.Path = job.Destination
			_ = sink.Emit(ctx, events.Event{Kind: events.KindCompleted, URL: eventURL, Path: job.Destination, Bytes: result.Bytes, Total: result.Bytes, Resuming: result.Resumed})
			return result, nil
		}
		if ctx.Err() != nil {
			cancelErr := ctx.Err()
			_ = sink.Emit(context.Background(), events.Event{Kind: events.KindCancelled, URL: eventURL, Path: job.Destination, Message: cancelErr.Error()})
			if plan.enabled && lastErr != nil && !errors.Is(lastErr, cancelErr) {
				return Result{}, errors.Join(cancelErr, lastErr)
			}
			return Result{}, cancelErr
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
	if errors.Is(lastErr, ErrFileSizeAbort) {
		// In no-part mode the destination is the in-progress file. Preserve an
		// existing destination rather than deleting it during abort cleanup.
		if partPath != job.Destination {
			removeRegularFile(partPath)
		}
		removeRegularFile(statePath)
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

func (downloader *Downloader) downloadAttempt(ctx context.Context, job Job, plan checkpointPlan, partPath, statePath string, sink events.Sink) (Result, error) {
	state, offset := newPartialState(job.URL, job.ResumeIdentity), int64(0)
	if !job.NoContinue {
		var err error
		state, offset, err = downloader.loadPartialWithPlan(ctx, job, plan, partPath, statePath)
		if err != nil {
			return Result{}, err
		}
	}
	if plan.enabled {
		if err := ctx.Err(); err != nil {
			return Result{}, err
		}
	}
	request, err := http.NewRequest(http.MethodGet, job.URL, nil)
	if err != nil {
		if plan.enabled {
			err = redactCheckpointError(job.URL, err)
		}
		return Result{}, fmt.Errorf("create download request: %w", err)
	}
	if job.Headers != nil {
		request.Header = job.Headers.Clone()
	}
	if offset > 0 {
		request.Header.Set("Range", fmt.Sprintf("bytes=%d-", offset))
		if state.ETag != "" {
			request.Header.Set("If-Range", state.ETag)
		} else if state.LastModified != "" {
			request.Header.Set("If-Range", state.LastModified)
		}
	}
	response, err := downloader.transport.Do(ctx, request)
	if err != nil {
		if ctx.Err() != nil {
			return Result{}, ctx.Err()
		}
		if plan.enabled {
			err = redactCheckpointError(job.URL, err)
		}
		return Result{}, retryableError{err}
	}
	defer response.Body.Close()

	if response.StatusCode == http.StatusRequestedRangeNotSatisfiable && offset > 0 && state.Total > 0 && offset == state.Total {
		return Result{Bytes: offset, Resumed: true}, nil
	}
	if response.StatusCode != http.StatusOK && response.StatusCode != http.StatusPartialContent {
		err := &HTTPStatusError{Code: response.StatusCode}
		if network.RetryableStatus(response.StatusCode) {
			return Result{}, retryableError{err}
		}
		return Result{}, err
	}

	resuming := offset > 0 && response.StatusCode == http.StatusPartialContent && validContentRange(response.Header.Get("Content-Range"), offset)
	if offset > 0 && response.StatusCode == http.StatusPartialContent && !resuming {
		if err := downloader.restartPartial(ctx, job, partPath); err != nil {
			return Result{}, err
		}
		job.NoContinue = true
		return downloader.downloadAttempt(ctx, job, plan, partPath, statePath, sink)
	}
	if resuming && (!resumeResponseMatches(state, response) || !plan.responseMatchesBoundary(response)) {
		if err := downloader.restartPartial(ctx, job, partPath); err != nil {
			return Result{}, err
		}
		job.NoContinue = true
		return downloader.downloadAttempt(ctx, job, plan, partPath, statePath, sink)
	}
	previousTotal := state.Total
	responseOffset := offset
	if !resuming {
		responseOffset = 0
	}
	responseTotalBytes := responseTotal(response, responseOffset)
	if resuming && responseTotalBytes > 0 && state.Total > 0 && responseTotalBytes != state.Total {
		if err := downloader.restartPartial(ctx, job, partPath); err != nil {
			return Result{}, err
		}
		job.NoContinue = true
		return downloader.downloadAttempt(ctx, job, plan, partPath, statePath, sink)
	}
	if !resuming {
		offset = 0
		state = newPartialState(job.URL, job.ResumeIdentity)
	}
	state.ETag = response.Header.Get("ETag")
	state.LastModified = response.Header.Get("Last-Modified")
	state.Total = responseTotalBytes
	if state.Total == 0 && resuming {
		state.Total = previousTotal
	}
	if plan.enabled {
		state.CommittedBytes = offset
		if err := validatePartialCheckpointState(state); err != nil {
			return Result{}, err
		}
	}
	if job.MinFilesize > 0 || job.MaxFilesize > 0 {
		// yt-dlp's http.py consults Content-Length plus the resume offset and
		// skips the size decision when Content-Encoding is present or the
		// length is unknown.
		if response.Header.Get("Content-Encoding") == "" && state.Total > 0 {
			if job.MinFilesize > 0 && state.Total < job.MinFilesize {
				return Result{}, &FileSizeAbortError{
					Message: fmt.Sprintf("File is smaller than min-filesize (%d bytes < %d bytes)", state.Total, job.MinFilesize),
				}
			}
			if job.MaxFilesize > 0 && state.Total > job.MaxFilesize {
				return Result{}, &FileSizeAbortError{
					Message: fmt.Sprintf("File is larger than max-filesize (%d bytes > %d bytes)", state.Total, job.MaxFilesize),
				}
			}
		}
	}
	maxBytes := job.MaxBytes
	if maxBytes <= 0 {
		maxBytes = maxDirectBytes
	}
	transferLimit := maxBytes
	if response.Header.Get("Content-Encoding") == "" && job.MaxFilesize > 0 && job.MaxFilesize < transferLimit {
		transferLimit = job.MaxFilesize
	}
	if state.Total > maxBytes {
		return Result{}, fmt.Errorf("%w: advertised %d bytes exceeds %d", ErrIncomplete, state.Total, maxBytes)
	}
	if response.Header.Get("Content-Encoding") == "" && state.Total > transferLimit {
		return Result{}, &FileSizeAbortError{
			Message: fmt.Sprintf("File is larger than max-filesize (%d bytes > %d bytes)", state.Total, job.MaxFilesize),
		}
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
	if plan.enabled && offset > 0 {
		if err := file.Truncate(offset); err != nil {
			_ = file.Close()
			return Result{}, fmt.Errorf("truncate uncommitted partial tail: %w", err)
		}
	}
	if offset > 0 {
		if _, err := file.Seek(offset, io.SeekStart); err != nil {
			_ = file.Close()
			return Result{}, fmt.Errorf("seek partial file: %w", err)
		}
	}

	written := offset
	lastCheckpointBytes := offset
	lastCheckpointAt := time.Time{}
	if plan.enabled {
		lastCheckpointAt = downloader.now()
	}
	limiter := newThrottle(job.RateLimit)
	detector := newThrottleDetector(job.ThrottleRate, job.ThrottleWindow, downloader.now)
	buffer := make([]byte, 64<<10)
	for {
		if err := ctx.Err(); err != nil {
			return Result{}, downloader.finishCancellation(ctx, job, plan, statePath, file, state, written)
		}
		count, readErr := response.Body.Read(buffer)
		if count > 0 {
			if detector.Observe(count) {
				if ctx.Err() != nil {
					return Result{}, downloader.finishCancellation(ctx, job, plan, statePath, file, state, written)
				}
				_ = file.Close()
				return Result{}, retryableError{ErrThrottled}
			}
			if written+int64(count) > transferLimit {
				_ = file.Close()
				if job.MaxFilesize > 0 && transferLimit == job.MaxFilesize {
					return Result{}, &FileSizeAbortError{
						Message: fmt.Sprintf("File is larger than max-filesize (%d bytes > %d bytes)", written+int64(count), job.MaxFilesize),
					}
				}
				return Result{}, fmt.Errorf("%w: response exceeds %d bytes", ErrIncomplete, maxBytes)
			}
			if err := limiter.Wait(ctx, count); err != nil {
				if ctx.Err() != nil {
					return Result{}, downloader.finishCancellation(ctx, job, plan, statePath, file, state, written)
				}
				_ = file.Close()
				return Result{}, err
			}
			writtenCount, writeErr := file.Write(buffer[:count])
			written += int64(writtenCount)
			if writeErr != nil || writtenCount != count {
				_ = file.Close()
				if writeErr == nil {
					writeErr = io.ErrShortWrite
				}
				return Result{}, fmt.Errorf("write partial file: %w", writeErr)
			}
			if err := sink.Emit(ctx, events.Event{Kind: events.KindProgress, URL: network.RedactRawURL(job.URL), Path: job.Destination, Bytes: written, Total: state.Total, Resuming: resuming}); err != nil {
				if ctx.Err() != nil {
					return Result{}, downloader.finishCancellation(ctx, job, plan, statePath, file, state, written)
				}
				_ = file.Close()
				if plan.enabled {
					err = redactCheckpointError(job.URL, err)
				}
				return Result{}, fmt.Errorf("emit progress: %w", err)
			}
			checkpointNow := time.Time{}
			if plan.enabled {
				checkpointNow = downloader.now()
			}
			if plan.due(written, lastCheckpointBytes, lastCheckpointAt, checkpointNow) {
				state.CommittedBytes = written
				if err := downloader.commitPartialCheckpoint(job, plan, statePath, file, state, false); err != nil {
					_ = file.Close()
					return Result{}, err
				}
				lastCheckpointBytes = written
				lastCheckpointAt = checkpointNow
			}
		}
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				break
			}
			if ctx.Err() != nil {
				return Result{}, downloader.finishCancellation(ctx, job, plan, statePath, file, state, written)
			}
			_ = file.Close()
			if plan.enabled {
				readErr = redactCheckpointError(job.URL, readErr)
			}
			return Result{}, retryableError{fmt.Errorf("read download response: %w", readErr)}
		}
	}
	if plan.enabled {
		if written == lastCheckpointBytes {
			if err := file.Close(); err != nil {
				return Result{}, fmt.Errorf("close partial file: %w", err)
			}
		} else {
			state.CommittedBytes = written
			if err := downloader.commitPartialCheckpoint(job, plan, statePath, file, state, true); err != nil {
				_ = file.Close()
				return Result{}, err
			}
		}
	} else {
		if err := downloader.retryFile(ctx, job, func() error { return downloader.syncPayload(file) }); err != nil {
			_ = file.Close()
			return Result{}, fmt.Errorf("sync partial file: %w", err)
		}
		if err := file.Close(); err != nil {
			return Result{}, fmt.Errorf("close partial file: %w", err)
		}
	}
	if plan.enabled {
		if err := ctx.Err(); err != nil {
			return Result{}, err
		}
	}
	if state.Total > 0 && written != state.Total {
		return Result{}, retryableError{fmt.Errorf("%w: got %d, want %d bytes", ErrIncomplete, written, state.Total)}
	}
	if response.Header.Get("Content-Encoding") == "" && job.MinFilesize > 0 && written < job.MinFilesize {
		return Result{}, &FileSizeAbortError{
			Message: fmt.Sprintf("File is smaller than min-filesize (%d bytes < %d bytes)", written, job.MinFilesize),
		}
	}
	return Result{Bytes: written, Resumed: resuming}, nil
}

func resumeResponseMatches(state partialState, response *http.Response) bool {
	if state.ETag != "" && response.Header.Get("ETag") != state.ETag {
		return false
	}
	if state.LastModified != "" && response.Header.Get("Last-Modified") != state.LastModified {
		return false
	}
	return true
}

func (downloader *Downloader) syncPayload(file *os.File) error {
	if downloader.syncPartialPayload != nil {
		return downloader.syncPartialPayload(file)
	}
	return file.Sync()
}

func (downloader *Downloader) truncatePartial(ctx context.Context, job Job, path string, size int64) error {
	if err := ctx.Err(); err != nil {
		return err
	}
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
	file, err := downloader.openPartial(ctx, job, path, os.O_WRONLY)
	if err != nil {
		return fmt.Errorf("open partial for truncation: %w", err)
	}
	if err := file.Truncate(size); err != nil {
		_ = file.Close()
		return fmt.Errorf("truncate partial file: %w", err)
	}
	if err := downloader.retryFile(ctx, job, func() error { return downloader.syncPayload(file) }); err != nil {
		_ = file.Close()
		return fmt.Errorf("sync truncated partial file: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close truncated partial file: %w", err)
	}
	return nil
}

func (downloader *Downloader) restartPartial(ctx context.Context, job Job, partPath string) error {
	if err := downloader.truncatePartial(ctx, job, partPath, 0); err != nil {
		return fmt.Errorf("restart partial download: %w", err)
	}
	return nil
}

func checkpointLocalContext() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), maxDirectCheckpointLocalDuration)
}

func (downloader *Downloader) commitPartialCheckpoint(job Job, plan checkpointPlan, statePath string, file *os.File, state partialState, closeFile bool) error {
	localCtx, cancel := checkpointLocalContext()
	defer cancel()
	if err := downloader.retryFile(localCtx, job, func() error { return downloader.syncPayload(file) }); err != nil {
		return fmt.Errorf("%w: sync partial file: %w", ErrCheckpointCommit, err)
	}
	if closeFile {
		if err := file.Close(); err != nil {
			return fmt.Errorf("%w: close partial file: %w", ErrCheckpointCommit, err)
		}
	}
	if err := validatePartialCheckpointState(state); err != nil {
		return err
	}
	if err := downloader.savePartialState(localCtx, job, statePath, state); err != nil {
		return fmt.Errorf("%w: %w", ErrCheckpointCommit, err)
	}
	if plan.onCommit != nil {
		if err := plan.onCommit(checkpointFromPartial(state)); err != nil {
			return &checkpointCallbackError{cause: err}
		}
	}
	return nil
}

func (downloader *Downloader) finishCancellation(ctx context.Context, job Job, plan checkpointPlan, statePath string, file *os.File, state partialState, written int64) error {
	cancelErr := ctx.Err()
	if cancelErr == nil {
		cancelErr = context.Canceled
	}
	if !plan.enabled {
		_ = file.Close()
		return cancelErr
	}
	state.CommittedBytes = written
	checkpointErr := downloader.commitPartialCheckpoint(job, plan, statePath, file, state, true)
	if checkpointErr != nil {
		return errors.Join(cancelErr, checkpointErr)
	}
	return cancelErr
}

// removeRegularFile cleans up an aborted partial artifact without following
// or deleting a hostile replacement such as a symlink.
func removeRegularFile(path string) {
	info, err := os.Lstat(path)
	if err == nil && info.Mode().IsRegular() {
		_ = os.Remove(path)
	}
}

func validateJob(job Job) error {
	if job.Attempts > maxDirectAttempts {
		return ErrTooManyAttempts
	}
	if job.MinFilesize < 0 || job.MaxFilesize < 0 {
		return fmt.Errorf("%w: negative file size bound", ErrInvalidLimits)
	}
	if job.Attempts < 0 || job.RetryBaseDelay < 0 || job.RetryMaxDelay < 0 || job.RetryBaseDelay > maxDirectRetryDelay || job.RetryMaxDelay > maxDirectRetryDelay || (job.RetryBaseDelay > 0 && job.RetryMaxDelay > 0 && job.RetryBaseDelay > job.RetryMaxDelay) || job.RateLimit < 0 || job.MaxBytes < 0 || job.MaxBytes > maxDirectBytes || job.ThrottleRate < 0 || job.ThrottleWindow < 0 || job.ThrottleWindow > maxDirectRetryDelay || job.ThrottleRestarts < 0 || job.ThrottleRestarts > maxDirectRestarts || job.FileAttempts < 0 || job.FileAttempts > maxDirectFileRetries {
		return ErrInvalidLimits
	}
	if _, err := checkpointPlanForJob(job); err != nil {
		return err
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

func loadPartial(partPath, statePath, rawURL, resumeIdentity string) (partialState, int64) {
	downloader := &Downloader{writePartialState: savePartialStateOnce, syncPartialPayload: (*os.File).Sync}
	state, offset, _ := downloader.loadPartialWithPlan(context.Background(), Job{URL: rawURL, ResumeIdentity: resumeIdentity}, checkpointPlan{}, partPath, statePath)
	return state, offset
}

func (downloader *Downloader) loadPartialWithPlan(ctx context.Context, job Job, plan checkpointPlan, partPath, statePath string) (partialState, int64, error) {
	empty := newPartialState(job.URL, job.ResumeIdentity)
	if plan.enabled {
		if err := checkPartialStateEvidence(statePath); err != nil {
			return empty, 0, err
		}
	}
	info, err := os.Stat(partPath)
	if errors.Is(err, os.ErrNotExist) || err != nil || info.Size() <= 0 {
		return empty, 0, nil
	}

	encoded, readErr := os.ReadFile(statePath)
	if errors.Is(readErr, os.ErrNotExist) {
		if plan.enabled && plan.boundary != nil && plan.boundary.CommittedBytes > 0 && info.Size() >= plan.boundary.CommittedBytes {
			state := stateFromBoundary(job, *plan.boundary)
			if err := downloader.truncatePartial(ctx, job, partPath, plan.boundary.CommittedBytes); err != nil {
				return empty, 0, err
			}
			return state, plan.boundary.CommittedBytes, nil
		}
		if plan.enabled {
			if err := downloader.truncatePartial(ctx, job, partPath, 0); err != nil {
				return empty, 0, err
			}
		}
		return empty, 0, nil
	}
	if readErr != nil {
		if plan.enabled {
			if err := downloader.truncatePartial(ctx, job, partPath, 0); err != nil {
				return empty, 0, err
			}
		}
		return empty, 0, nil
	}

	var decoded partialState
	if json.Unmarshal(encoded, &decoded) != nil {
		if plan.enabled {
			if err := downloader.truncatePartial(ctx, job, partPath, 0); err != nil {
				return empty, 0, err
			}
		}
		return empty, 0, nil
	}
	if !partialStateMatchesJob(decoded, job, plan) {
		if err := downloader.truncatePartial(ctx, job, partPath, 0); err != nil {
			return empty, 0, err
		}
		return empty, 0, nil
	}

	state := newPartialState(job.URL, job.ResumeIdentity)
	state.ETag = decoded.ETag
	state.LastModified = decoded.LastModified
	state.Total = decoded.Total
	state.CommittedBytes = decoded.CommittedBytes
	if plan.enabled {
		if !plan.matchesBoundary(state, job) || state.CommittedBytes <= 0 || state.CommittedBytes > info.Size() || state.CommittedBytes > maxDirectBytes {
			if err := downloader.truncatePartial(ctx, job, partPath, 0); err != nil {
				return empty, 0, err
			}
			return empty, 0, nil
		}
		if plan.boundary != nil {
			if plan.boundary.CommittedBytes < state.CommittedBytes {
				state.CommittedBytes = plan.boundary.CommittedBytes
			}
			if state.ETag == "" {
				state.ETag = plan.boundary.ETag
			}
			if state.LastModified == "" {
				state.LastModified = plan.boundary.LastModified
			}
			if state.Total == 0 {
				state.Total = plan.boundary.Total
			}
		}
		if state.CommittedBytes <= 0 {
			if err := downloader.truncatePartial(ctx, job, partPath, 0); err != nil {
				return empty, 0, err
			}
			return empty, 0, nil
		}
		if info.Size() > state.CommittedBytes {
			if err := downloader.truncatePartial(ctx, job, partPath, state.CommittedBytes); err != nil {
				return empty, 0, err
			}
		}
		if err := validatePartialCheckpointState(state); err != nil {
			return empty, 0, nil
		}
		return state, state.CommittedBytes, nil
	}
	return state, info.Size(), nil
}

func partialStateMatchesJob(state partialState, job Job, plan checkpointPlan) bool {
	if job.ResumeIdentity != "" {
		if state.ResumeIdentity == job.ResumeIdentity {
			return true
		}
		if plan.enabled {
			return false
		}
		// Migrate legacy partial state only across the old exact-URL
		// boundary. A legacy state never authorizes a refreshed URL.
		return state.ResumeIdentity == "" && state.URL == job.URL
	}
	return state.ResumeIdentity == "" && state.URL == job.URL
}

func stateFromBoundary(job Job, boundary Checkpoint) partialState {
	state := newPartialState(job.URL, job.ResumeIdentity)
	state.ResumeIdentity = job.ResumeIdentity
	state.ETag = boundary.ETag
	state.LastModified = boundary.LastModified
	state.Total = boundary.Total
	state.CommittedBytes = boundary.CommittedBytes
	return state
}

func newPartialState(rawURL, resumeIdentity string) partialState {
	if resumeIdentity != "" {
		return partialState{ResumeIdentity: resumeIdentity}
	}
	return partialState{URL: rawURL}
}

func (downloader *Downloader) savePartialState(ctx context.Context, job Job, path string, state partialState) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if job.Checkpoint != nil {
		if err := checkPartialStateEvidence(path); err != nil {
			return err
		}
	}
	writer := downloader.writePartialState
	if writer == nil {
		writer = savePartialStateOnce
	}
	return downloader.retryFile(ctx, job, func() error { return writer(path, state) })
}

func savePartialStateOnce(path string, state partialState) error {
	encoded, err := json.Marshal(state)
	if err != nil {
		return err
	}
	if state.ResumeIdentity != "" || state.CommittedBytes != 0 || state.LastModified != "" {
		if err := validatePartialCheckpointState(state); err != nil {
			return err
		}
		if int64(len(encoded)) > maxCheckpointStateBytes {
			return fmt.Errorf("%w: state image exceeds size bound", ErrInvalidCheckpoint)
		}
	}
	if err := regularOrAbsent(path); err != nil {
		return err
	}
	return atomicfile.Write(path, 0o600, func(writer io.Writer) error {
		_, err := writer.Write(encoded)
		return err
	})
}

func validContentRange(header string, offset int64) bool {
	start, _, _, ok := parseContentRange(header)
	return ok && start == offset
}

func responseTotal(response *http.Response, offset int64) int64 {
	if response.StatusCode == http.StatusPartialContent {
		if _, _, total, ok := parseContentRange(response.Header.Get("Content-Range")); ok && total >= 0 {
			return total
		}
	}
	if response.ContentLength >= 0 {
		return offset + response.ContentLength
	}
	return 0
}

func parseContentRange(header string) (start, end, total int64, ok bool) {
	if !strings.HasPrefix(header, "bytes ") {
		return 0, 0, 0, false
	}
	rangeAndTotal := strings.TrimPrefix(header, "bytes ")
	slash := strings.LastIndexByte(rangeAndTotal, '/')
	if slash < 0 {
		return 0, 0, 0, false
	}
	rangePart := rangeAndTotal[:slash]
	totalPart := rangeAndTotal[slash+1:]
	dash := strings.IndexByte(rangePart, '-')
	if dash <= 0 || dash == len(rangePart)-1 {
		return 0, 0, 0, false
	}
	start, err := strconv.ParseInt(strings.TrimSpace(rangePart[:dash]), 10, 64)
	if err != nil || start < 0 {
		return 0, 0, 0, false
	}
	end, err = strconv.ParseInt(strings.TrimSpace(rangePart[dash+1:]), 10, 64)
	if err != nil || end < start {
		return 0, 0, 0, false
	}
	if totalPart == "*" {
		return start, end, -1, true
	}
	total, err = strconv.ParseInt(strings.TrimSpace(totalPart), 10, 64)
	if err != nil || total <= end {
		return 0, 0, 0, false
	}
	return start, end, total, true
}
