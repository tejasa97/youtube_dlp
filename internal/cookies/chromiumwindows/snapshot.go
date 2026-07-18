package chromiumwindows

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
)

const snapshotAttempts = 3

type snapshotSource struct {
	file     *os.File
	info     os.FileInfo
	suffix   string
	optional bool
}

func snapshotDatabase(ctx context.Context, source, tempRoot string, maximum int64) (string, func(), error) {
	if err := ctx.Err(); err != nil {
		return "", func() {}, err
	}
	for range snapshotAttempts {
		directory, err := os.MkdirTemp(tempRoot, "ytdlp-chromium-windows-")
		if err != nil {
			return "", func() {}, ErrSnapshot
		}
		cleanup := func() { _ = os.RemoveAll(directory) }
		if err := os.Chmod(directory, 0o700); err != nil {
			cleanup()
			return "", func() {}, ErrSnapshot
		}
		destination := filepath.Join(directory, "Cookies")
		stable, err := snapshotAttempt(ctx, source, destination, maximum)
		if err == nil && stable {
			return destination, cleanup, nil
		}
		cleanup()
		if err != nil && !errors.Is(err, ErrSnapshot) {
			return "", func() {}, err
		}
		if err := ctx.Err(); err != nil {
			return "", func() {}, err
		}
	}
	return "", func() {}, ErrSnapshot
}

func snapshotAttempt(ctx context.Context, source, destination string, maximum int64) (bool, error) {
	sources := make([]snapshotSource, 0, 2)
	for _, candidate := range []snapshotSource{{suffix: ""}, {suffix: "-wal", optional: true}} {
		file, info, err := openSecureSource(source+candidate.suffix, maximum)
		if candidate.optional && errors.Is(err, ErrNotFound) {
			continue
		}
		if err != nil {
			closeSnapshotSources(sources)
			return false, err
		}
		candidate.file, candidate.info = file, info
		sources = append(sources, candidate)
	}
	defer closeSnapshotSources(sources)
	for _, candidate := range sources {
		if err := copySnapshotFile(ctx, candidate.file, destination+candidate.suffix, candidate.info.Size()); err != nil {
			return false, err
		}
	}
	for _, candidate := range sources {
		after, err := candidate.file.Stat()
		if err != nil || after.Size() != candidate.info.Size() || !after.ModTime().Equal(candidate.info.ModTime()) {
			return false, ErrSnapshot
		}
	}
	return true, nil
}

func closeSnapshotSources(sources []snapshotSource) {
	for _, source := range sources {
		_ = source.file.Close()
	}
}

func copySnapshotFile(ctx context.Context, source *os.File, destination string, size int64) error {
	output, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return ErrSnapshot
	}
	defer output.Close()
	buffer := make([]byte, 64<<10)
	var copied int64
	for copied < size {
		if err := ctx.Err(); err != nil {
			return err
		}
		want := len(buffer)
		if remaining := size - copied; remaining < int64(want) {
			want = int(remaining)
		}
		read, err := io.ReadFull(source, buffer[:want])
		if err != nil || read != want {
			return ErrSnapshot
		}
		if _, err := output.Write(buffer[:read]); err != nil {
			return ErrSnapshot
		}
		copied += int64(read)
	}
	if err := output.Sync(); err != nil {
		return ErrSnapshot
	}
	return nil
}
