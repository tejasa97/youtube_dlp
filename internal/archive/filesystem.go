package archive

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

type archiveEntries struct {
	order []string
	set   map[string]struct{}
}

func validatePath(path string) error {
	if path == "" || strings.IndexByte(path, 0) >= 0 || filepath.Clean(path) != path {
		return ErrUnsafePath
	}
	info, err := os.Lstat(path)
	if err == nil && !info.Mode().IsRegular() {
		return ErrUnsafePath
	}
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("%w: inspect archive", ErrIO)
	}
	return nil
}

func (store *Store) read(ctx context.Context) (archiveEntries, error) {
	result := archiveEntries{set: make(map[string]struct{})}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	if err := validatePath(store.path); err != nil {
		return result, err
	}
	before, beforeErr := os.Lstat(store.path)
	if beforeErr != nil && !errors.Is(beforeErr, os.ErrNotExist) {
		return result, fmt.Errorf("%w: inspect archive", ErrIO)
	}
	file, err := os.Open(store.path)
	if errors.Is(err, os.ErrNotExist) {
		return result, nil
	}
	if err != nil {
		return result, fmt.Errorf("%w: open archive", ErrIO)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || (beforeErr == nil && !os.SameFile(before, info)) {
		return result, ErrUnsafePath
	}
	if info.Size() < 0 || info.Size() > store.options.MaxFileBytes {
		return result, ErrTooLarge
	}
	reader := bufio.NewReaderSize(io.LimitReader(file, store.options.MaxFileBytes+1), min(store.options.MaxRecordBytes+1, 64<<10))
	var consumed int64
	for {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		line, readErr := reader.ReadString('\n')
		consumed += int64(len(line))
		if consumed > store.options.MaxFileBytes {
			return result, ErrTooLarge
		}
		if len(line) > store.options.MaxRecordBytes+1 {
			return result, ErrTooLarge
		}
		// Pinned yt-dlp preload_download_archive adds line.strip() to its set.
		// TrimSpace gives matching UTF-8 whitespace behavior while retaining
		// the ordered opaque content between the trimmed edges.
		line = strings.TrimSpace(line)
		if line != "" {
			if err := validateRecord(line, store.options.MaxRecordBytes); err != nil {
				return result, err
			}
			result.order = append(result.order, line)
			result.set[line] = struct{}{}
		}
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				break
			}
			return result, fmt.Errorf("%w: read archive", ErrIO)
		}
	}
	return result, nil
}

func (store *Store) write(ctx context.Context, records []string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	parent := filepath.Dir(store.path)
	if err := os.MkdirAll(parent, 0o700); err != nil {
		return fmt.Errorf("%w: create archive directory", ErrIO)
	}
	temporary, err := os.CreateTemp(parent, ".ytdlp-archive-*")
	if err != nil {
		return fmt.Errorf("%w: create temporary archive", ErrIO)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		temporary.Close()
		return fmt.Errorf("%w: secure temporary archive", ErrIO)
	}
	buffer := bufio.NewWriterSize(temporary, 64<<10)
	var total int64
	for _, record := range records {
		if err := ctx.Err(); err != nil {
			temporary.Close()
			return err
		}
		if err := validateRecord(record, store.options.MaxRecordBytes); err != nil {
			temporary.Close()
			return err
		}
		total += int64(len(record) + 1)
		if total > store.options.MaxFileBytes {
			temporary.Close()
			return ErrTooLarge
		}
		if _, err := buffer.WriteString(record + "\n"); err != nil {
			temporary.Close()
			return fmt.Errorf("%w: write temporary archive", ErrIO)
		}
	}
	if err := ctx.Err(); err != nil {
		temporary.Close()
		return err
	}
	if err := buffer.Flush(); err != nil {
		temporary.Close()
		return fmt.Errorf("%w: flush temporary archive", ErrIO)
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return fmt.Errorf("%w: sync temporary archive", ErrIO)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("%w: close temporary archive", ErrIO)
	}
	if err := validatePath(store.path); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := os.Rename(temporaryPath, store.path); err != nil {
		return fmt.Errorf("%w: replace archive", ErrIO)
	}
	return nil
}
