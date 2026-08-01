//go:build windows

package cache

import (
	"context"
	"os"
)

// Windows cleanup is deliberately rejected: the native implementation cannot
// provide the same no-follow descriptor boundary as the Unix implementation.
// Ordinary cache use still gets the process-wide root gate.
func lockCacheRoot(ctx context.Context, root string) (*os.File, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	file, err := os.Open(root)
	if err != nil {
		return nil, err
	}
	pathInfo, pathErr := os.Lstat(root)
	fileInfo, fileErr := file.Stat()
	if pathErr != nil || fileErr != nil || !pathInfo.IsDir() || !os.SameFile(pathInfo, fileInfo) {
		_ = file.Close()
		if pathErr != nil {
			return nil, pathErr
		}
		return nil, ErrUnsafePath
	}
	return file, nil
}

func unlockCacheRoot(file *os.File) {
	if file != nil {
		_ = file.Close()
	}
}

func removeRootLocked(context.Context, string, *os.File) error {
	return ErrUnsafePath
}
