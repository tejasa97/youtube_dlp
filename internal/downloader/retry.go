package downloader

import (
	"context"
	"errors"
	"os"
	"syscall"
)

func (downloader *Downloader) openPartial(ctx context.Context, job Job, path string, flags int) (*os.File, error) {
	var file *os.File
	err := downloader.retryFile(ctx, job, func() error { var err error; file, err = os.OpenFile(path, flags, 0o644); return err })
	return file, err
}

func (downloader *Downloader) retryFile(ctx context.Context, job Job, operation func() error) error {
	attempts := job.FileAttempts
	if attempts <= 0 {
		attempts = 3
	}
	if attempts > 10 {
		attempts = 10
	}
	var err error
	for attempt := 1; attempt <= attempts; attempt++ {
		err = operation()
		if err == nil || !transientFileError(err) {
			return err
		}
		if attempt < attempts {
			if sleepErr := downloader.sleep(ctx, retryDelay(job, attempt)); sleepErr != nil {
				return sleepErr
			}
		}
	}
	return err
}

func transientFileError(err error) bool {
	return errors.Is(err, syscall.EINTR) || errors.Is(err, syscall.EAGAIN) || errors.Is(err, syscall.EBUSY) || errors.Is(err, syscall.ETXTBSY)
}
