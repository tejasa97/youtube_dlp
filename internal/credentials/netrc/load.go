package netrc

import (
	"context"
	"errors"
	"fmt"
	"os"
)

// Load opens without following a final symlink, validates the opened handle,
// and parses from that same handle to avoid path substitution races.
func Load(ctx context.Context, path string, limits Limits) (*Store, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	normalized, err := limits.normalized()
	if err != nil {
		return nil, err
	}
	file, err := openSecure(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("%w: %w", ErrIO, err)
		}
		return nil, fmt.Errorf("%w: open failed", ErrUnsafeFile)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() < 0 || info.Size() > normalized.MaxBytes {
		if info != nil && info.Size() > normalized.MaxBytes {
			return nil, ErrLimit
		}
		return nil, ErrUnsafeFile
	}
	if err := validateSecureHandle(file, info); err != nil {
		return nil, err
	}
	return Parse(ctx, file, normalized)
}
