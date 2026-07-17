package chromium

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

const maxDatabaseBytes int64 = 2 << 30

func copyDatabaseSnapshot(ctx context.Context, source, tempRoot string) (string, func(), error) {
	if err := ctx.Err(); err != nil {
		return "", nil, err
	}
	directory, err := os.MkdirTemp(tempRoot, "ytdlp-go-chromium-cookies-")
	if err != nil {
		return "", nil, fmt.Errorf("%w: create private directory", ErrSnapshot)
	}
	cleanup := func() { _ = os.RemoveAll(directory) }
	if err := os.Chmod(directory, 0o700); err != nil {
		cleanup()
		return "", nil, fmt.Errorf("%w: secure private directory", ErrSnapshot)
	}
	destination := filepath.Join(directory, "Cookies")
	for attempt := 0; attempt < 3; attempt++ {
		beforeDatabase, err := inspectSourceFile(source, false)
		if err != nil {
			cleanup()
			return "", nil, err
		}
		beforeWAL, err := inspectSourceFile(source+"-wal", true)
		if err != nil {
			cleanup()
			return "", nil, err
		}
		_ = os.Remove(destination)
		_ = os.Remove(destination + "-wal")
		if err := copyStableRegularFile(ctx, source, destination, false); err != nil {
			cleanup()
			return "", nil, err
		}
		if err := copyStableRegularFile(ctx, source+"-wal", destination+"-wal", true); err != nil {
			cleanup()
			return "", nil, err
		}
		afterDatabase, err := inspectSourceFile(source, false)
		if err != nil {
			cleanup()
			return "", nil, err
		}
		afterWAL, err := inspectSourceFile(source+"-wal", true)
		if err != nil {
			cleanup()
			return "", nil, err
		}
		if sameSourceFile(beforeDatabase, afterDatabase) && sameSourceFile(beforeWAL, afterWAL) {
			return destination, cleanup, nil
		}
	}
	cleanup()
	return "", nil, fmt.Errorf("%w: database and WAL changed while copying", ErrSnapshot)
}

type sourceFileState struct {
	exists bool
	info   os.FileInfo
}

func inspectSourceFile(path string, optional bool) (sourceFileState, error) {
	info, err := os.Lstat(path)
	if err != nil {
		if optional && errors.Is(err, os.ErrNotExist) {
			return sourceFileState{}, nil
		}
		if errors.Is(err, os.ErrNotExist) {
			return sourceFileState{}, ErrDatabaseNotFound
		}
		return sourceFileState{}, fmt.Errorf("%w: inspect source", ErrSnapshot)
	}
	if !info.Mode().IsRegular() || info.Size() < 0 || info.Size() > maxDatabaseBytes {
		return sourceFileState{}, ErrUnsafeDatabase
	}
	return sourceFileState{exists: true, info: info}, nil
}

func sameSourceFile(left, right sourceFileState) bool {
	if left.exists != right.exists {
		return false
	}
	if !left.exists {
		return true
	}
	return os.SameFile(left.info, right.info) && left.info.Size() == right.info.Size() && left.info.ModTime() == right.info.ModTime()
}

func copyStableRegularFile(ctx context.Context, source, destination string, optional bool) error {
	for attempt := 0; attempt < 3; attempt++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		before, err := os.Lstat(source)
		if err != nil {
			if optional && errors.Is(err, os.ErrNotExist) {
				return nil
			}
			if errors.Is(err, os.ErrNotExist) {
				return ErrDatabaseNotFound
			}
			return fmt.Errorf("%w: inspect source", ErrSnapshot)
		}
		if !before.Mode().IsRegular() || before.Size() < 0 || before.Size() > maxDatabaseBytes {
			return ErrUnsafeDatabase
		}
		input, err := os.Open(source)
		if err != nil {
			return fmt.Errorf("%w: open source", ErrSnapshot)
		}
		opened, statErr := input.Stat()
		if statErr != nil || !os.SameFile(before, opened) {
			_ = input.Close()
			return ErrUnsafeDatabase
		}
		temporary := destination + ".copy"
		output, err := os.OpenFile(temporary, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
		if err != nil {
			_ = input.Close()
			return fmt.Errorf("%w: create copy", ErrSnapshot)
		}
		written, copyErr := copyWithContext(ctx, output, input, maxDatabaseBytes+1)
		closeOutputErr := output.Close()
		after, afterErr := input.Stat()
		closeInputErr := input.Close()
		if copyErr != nil || closeOutputErr != nil || closeInputErr != nil {
			_ = os.Remove(temporary)
			return fmt.Errorf("%w: copy source", ErrSnapshot)
		}
		if written > maxDatabaseBytes {
			_ = os.Remove(temporary)
			return ErrUnsafeDatabase
		}
		if afterErr == nil && opened.Size() == after.Size() && opened.ModTime() == after.ModTime() {
			if err := os.Rename(temporary, destination); err != nil {
				_ = os.Remove(temporary)
				return fmt.Errorf("%w: finalize copy", ErrSnapshot)
			}
			return nil
		}
		_ = os.Remove(temporary)
	}
	return fmt.Errorf("%w: source changed while copying", ErrSnapshot)
}

func copyWithContext(ctx context.Context, destination, source *os.File, limit int64) (int64, error) {
	buffer := make([]byte, 64*1024)
	var written int64
	for written < limit {
		if err := ctx.Err(); err != nil {
			return written, err
		}
		remaining := limit - written
		chunk := buffer
		if int64(len(chunk)) > remaining {
			chunk = chunk[:remaining]
		}
		read, readErr := source.Read(chunk)
		if read > 0 {
			count, writeErr := destination.Write(chunk[:read])
			written += int64(count)
			if writeErr != nil {
				return written, writeErr
			}
			if count != read {
				return written, errors.New("short cookie snapshot write")
			}
		}
		if readErr != nil {
			if errors.Is(readErr, os.ErrClosed) {
				return written, readErr
			}
			if errors.Is(readErr, io.EOF) {
				return written, nil
			}
			return written, readErr
		}
	}
	return written, nil
}
