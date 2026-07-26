package downloader

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/ytdlp-go/ytdlp/internal/events"
	"github.com/ytdlp-go/ytdlp/internal/network"
)

// WriteJob stores a bounded local payload with the same destination safety as
// Download: output-root validation, regular-file checks, partial writes, and
// atomic finalization.
type WriteJob struct {
	OutputRoot   string
	Destination  string
	Payload      []byte
	Overwrite    bool
	MaxBytes     int64
	FileAttempts int
}

func (downloader *Downloader) Write(ctx context.Context, job WriteJob, sink events.Sink) (Result, error) {
	if err := validateWriteJob(job); err != nil {
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

	eventURL := network.RedactRawURL(job.Destination)
	_ = sink.Emit(ctx, events.Event{Kind: events.KindStarting, URL: eventURL, Path: job.Destination})

	partPath := job.Destination + ".part"
	if err := regularOrAbsent(partPath); err != nil {
		return Result{}, err
	}
	if err := downloader.writePartial(ctx, job, partPath); err != nil {
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
		return Result{}, fmt.Errorf("recheck destination: %w", err)
	}
	if err := downloader.finalize(ctx, WriteToDownloadJob(job), partPath, job.Destination, job.Overwrite); err != nil {
		return Result{}, fmt.Errorf("finalize write: %w", err)
	}
	result := Result{Path: job.Destination, Bytes: int64(len(job.Payload))}
	_ = sink.Emit(ctx, events.Event{Kind: events.KindCompleted, URL: eventURL, Path: job.Destination, Bytes: result.Bytes, Total: result.Bytes})
	return result, nil
}

func validateWriteJob(job WriteJob) error {
	maxBytes := job.MaxBytes
	if maxBytes <= 0 {
		maxBytes = maxDirectBytes
	}
	if maxBytes > maxDirectBytes {
		return ErrInvalidLimits
	}
	if int64(len(job.Payload)) > maxBytes {
		return fmt.Errorf("%w: payload exceeds %d bytes", ErrIncomplete, maxBytes)
	}
	if job.FileAttempts < 0 || job.FileAttempts > maxDirectFileRetries {
		return ErrInvalidLimits
	}
	return nil
}

func (downloader *Downloader) writePartial(ctx context.Context, job WriteJob, partPath string) error {
	return downloader.retryFile(ctx, WriteToDownloadJob(job), func() error {
		if err := regularOrAbsent(partPath); err != nil {
			return err
		}
		temporary, err := os.CreateTemp(filepath.Dir(partPath), "."+filepath.Base(partPath)+".tmp-*")
		if err != nil {
			return fmt.Errorf("create partial write: %w", err)
		}
		temporaryPath := temporary.Name()
		defer os.Remove(temporaryPath)
		if _, err = temporary.Write(job.Payload); err == nil {
			err = temporary.Sync()
		}
		if closeErr := temporary.Close(); err == nil {
			err = closeErr
		}
		if err != nil {
			return fmt.Errorf("write partial payload: %w", err)
		}
		if err := regularOrAbsent(partPath); err != nil {
			return err
		}
		return os.Rename(temporaryPath, partPath)
	})
}

func WriteToDownloadJob(job WriteJob) Job {
	return Job{
		OutputRoot: job.OutputRoot, Destination: job.Destination,
		Overwrite: job.Overwrite, MaxBytes: job.MaxBytes, FileAttempts: job.FileAttempts,
	}
}
