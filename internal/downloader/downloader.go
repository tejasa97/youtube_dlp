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
)

type Job struct {
	URL         string
	Headers     http.Header
	OutputRoot  string
	Destination string
	Overwrite   bool
	Attempts    int
}

type Result struct {
	Path    string
	Bytes   int64
	Resumed bool
}

type Downloader struct {
	transport network.Doer
}

func New(transport network.Doer) *Downloader {
	return &Downloader{transport: transport}
}

type partialState struct {
	URL   string `json:"url"`
	ETag  string `json:"etag,omitempty"`
	Total int64  `json:"total,omitempty"`
}

func (downloader *Downloader) Download(ctx context.Context, job Job, sink events.Sink) (Result, error) {
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
	if _, err := os.Stat(job.Destination); err == nil && !job.Overwrite {
		return Result{}, ErrDestinationExists
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return Result{}, fmt.Errorf("inspect destination: %w", err)
	}

	partPath := job.Destination + ".part"
	statePath := partPath + ".json"
	if symlink(partPath) || symlink(statePath) {
		return Result{}, ErrUnsafeDestination
	}
	attempts := job.Attempts
	if attempts <= 0 {
		attempts = 3
	}
	eventURL := network.RedactRawURL(job.URL)
	_ = sink.Emit(ctx, events.Event{Kind: events.KindStarting, URL: eventURL, Path: job.Destination})

	var result Result
	var lastErr error
	for attempt := 1; attempt <= attempts; attempt++ {
		result, lastErr = downloader.downloadAttempt(ctx, job, partPath, statePath, sink)
		if lastErr == nil {
			if !job.Overwrite {
				if _, err := os.Stat(job.Destination); err == nil {
					return Result{}, ErrDestinationExists
				} else if !errors.Is(err, os.ErrNotExist) {
					return Result{}, fmt.Errorf("recheck destination: %w", err)
				}
			}
			if err := finalize(partPath, job.Destination, job.Overwrite); err != nil {
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
		if attempt < attempts {
			_ = sink.Emit(ctx, events.Event{Kind: events.KindRetry, URL: eventURL, Path: job.Destination, Attempt: attempt + 1, Message: lastErr.Error()})
			timer := time.NewTimer(time.Duration(attempt) * 25 * time.Millisecond)
			select {
			case <-ctx.Done():
				timer.Stop()
				return Result{}, ctx.Err()
			case <-timer.C:
			}
		}
	}
	return Result{}, lastErr
}

func finalize(partPath, destination string, overwrite bool) error {
	err := os.Rename(partPath, destination)
	if err == nil || !overwrite {
		return err
	}
	// Windows does not atomically replace an existing destination. Only remove
	// it after a complete partial file exists and a direct rename has failed.
	if removeErr := os.Remove(destination); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
		return errors.Join(err, removeErr)
	}
	return os.Rename(partPath, destination)
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
	if err := savePartialState(statePath, state); err != nil {
		return Result{}, err
	}

	flags := os.O_CREATE | os.O_WRONLY
	if offset == 0 {
		flags |= os.O_TRUNC
	}
	file, err := os.OpenFile(partPath, flags, 0o644)
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
	buffer := make([]byte, 64<<10)
	for {
		if err := ctx.Err(); err != nil {
			file.Close()
			return Result{}, err
		}
		count, readErr := response.Body.Read(buffer)
		if count > 0 {
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
	if err := file.Sync(); err != nil {
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

type retryableError struct{ error }

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

func savePartialState(path string, state partialState) error {
	encoded, err := json.Marshal(state)
	if err != nil {
		return err
	}
	temporary := path + ".tmp"
	if err := os.WriteFile(temporary, encoded, 0o600); err != nil {
		return fmt.Errorf("write partial state: %w", err)
	}
	if err := os.Rename(temporary, path); err != nil {
		return fmt.Errorf("finalize partial state: %w", err)
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
