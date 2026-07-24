package safari

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

var defaultRelativePaths = []string{
	filepath.Join("Library", "Cookies", "Cookies.binarycookies"),
	filepath.Join("Library", "Containers", "com.apple.Safari", "Data", "Library", "Cookies", "Cookies.binarycookies"),
}

// Import reads Safari cookies on macOS. Parsing itself is portable and exposed
// through Parse for deterministic conformance tests.
func Import(ctx context.Context, options Options) (Result, error) {
	if runtime.GOOS != "darwin" {
		return Result{}, ErrUnsupportedPlatform
	}
	if ctx == nil {
		ctx = context.Background()
	}
	path, err := locate(options)
	if err != nil {
		return Result{}, err
	}
	data, err := readRegular(ctx, path, options.MaxBytes)
	if err != nil {
		return Result{}, err
	}
	return Parse(ctx, data, options)
}

func locate(options Options) (string, error) {
	if options.DatabasePath != "" {
		path, err := expandPath(options.DatabasePath, options.HomeDir)
		if err != nil {
			return "", err
		}
		return validatePath(path)
	}
	home := options.HomeDir
	if home == "" {
		var err error
		home, err = os.UserHomeDir()
		if err != nil {
			return "", ErrNotFound
		}
	}
	for _, relative := range defaultRelativePaths {
		path, err := validatePath(filepath.Join(home, relative))
		if err == nil {
			return path, nil
		}
		if !errors.Is(err, ErrNotFound) && !errors.Is(err, ErrUnsafePath) {
			return "", err
		}
	}
	return "", ErrNotFound
}

func expandPath(path, home string) (string, error) {
	if strings.ContainsRune(path, 0) {
		return "", ErrUnsafePath
	}
	if strings.HasPrefix(path, "~/") {
		if home == "" {
			var err error
			home, err = os.UserHomeDir()
			if err != nil {
				return "", ErrUnsafePath
			}
		}
		path = filepath.Join(home, path[2:])
	}
	if !filepath.IsAbs(path) {
		return "", ErrUnsafePath
	}
	return filepath.Clean(path), nil
}

func validatePath(path string) (string, error) {
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return "", ErrNotFound
	}
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return "", ErrUnsafePath
	}
	return path, nil
}

func readRegular(ctx context.Context, path string, configuredLimit int64) ([]byte, error) {
	limit := configuredLimit
	if limit <= 0 {
		limit = maxFileBytes
	}
	if limit > maxFileBytes {
		limit = maxFileBytes
	}
	before, err := os.Lstat(path)
	if err != nil || !before.Mode().IsRegular() || before.Mode()&os.ModeSymlink != 0 ||
		before.Size() < 0 {
		return nil, ErrUnsafePath
	}
	if before.Size() > limit {
		return nil, ErrLimit
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, ErrUnsafePath
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !opened.Mode().IsRegular() || !os.SameFile(before, opened) {
		return nil, ErrUnsafePath
	}
	buffer := make([]byte, 0, opened.Size())
	chunk := make([]byte, 64<<10)
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		count, readErr := file.Read(chunk)
		if count > 0 {
			if int64(len(buffer))+int64(count) > limit {
				return nil, ErrLimit
			}
			buffer = append(buffer, chunk[:count]...)
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			return nil, ErrUnsafePath
		}
	}
	after, err := file.Stat()
	if err != nil || !os.SameFile(opened, after) || after.Size() != opened.Size() ||
		int64(len(buffer)) != after.Size() {
		return nil, ErrUnsafePath
	}
	return buffer, nil
}
