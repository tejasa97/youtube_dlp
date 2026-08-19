//go:build !windows

package cache

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/sys/unix"
)

func lockCacheRoot(ctx context.Context, root string) (*cacheRootLock, error) {
	fd, err := unix.Open(root, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		if errors.Is(err, unix.ELOOP) || errors.Is(err, unix.ENOTDIR) {
			return nil, ErrUnsafePath
		}
		return nil, err
	}
	file := os.NewFile(uintptr(fd), root)
	lock := &cacheRootLock{file: file}
	closeWithError := func(lockErr error) (*cacheRootLock, error) {
		_ = file.Close()
		return nil, lockErr
	}
	initial, err := file.Stat()
	if err != nil || !initial.IsDir() {
		if err == nil {
			err = ErrUnsafePath
		}
		return closeWithError(err)
	}
	pathInfo, err := os.Lstat(root)
	if err != nil {
		return closeWithError(err)
	}
	if pathInfo.Mode()&os.ModeSymlink != 0 || !pathInfo.IsDir() || !os.SameFile(initial, pathInfo) {
		return closeWithError(ErrUnsafePath)
	}
	for {
		if err := ctx.Err(); err != nil {
			return closeWithError(err)
		}
		err = unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB)
		if err == nil {
			break
		}
		if !errors.Is(err, unix.EAGAIN) && !errors.Is(err, unix.EWOULDBLOCK) {
			return closeWithError(fmt.Errorf("%w: lock cache root", ErrIO))
		}
		timer := time.NewTimer(10 * time.Millisecond)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return closeWithError(ctx.Err())
		case <-timer.C:
		}
	}
	locked, err := file.Stat()
	if err != nil {
		return closeWithError(fmt.Errorf("%w: inspect locked cache root", ErrIO))
	}
	pathInfo, err = os.Lstat(root)
	if err != nil {
		return closeWithError(err)
	}
	if pathInfo.Mode()&os.ModeSymlink != 0 || !pathInfo.IsDir() || !os.SameFile(locked, pathInfo) {
		return closeWithError(ErrUnsafePath)
	}
	return lock, nil
}

func unlockCacheRoot(lock *cacheRootLock) {
	if lock == nil || lock.file == nil {
		return
	}
	_ = unix.Flock(int(lock.file.Fd()), unix.LOCK_UN)
	_ = lock.file.Close()
}

func removeRootLocked(ctx context.Context, root string, lock *cacheRootLock) error {
	if lock == nil || lock.file == nil {
		return fmt.Errorf("%w: missing cache root lock", ErrIO)
	}
	rootFile := lock.file
	if err := validateRootEntries(ctx, rootFile); err != nil {
		return err
	}
	if err := removeRootEntries(ctx, rootFile); err != nil {
		return err
	}
	parentPath := filepath.Dir(root)
	base := filepath.Base(root)
	parentFD, err := unix.Open(parentPath, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return fmt.Errorf("%w: open cache parent", ErrIO)
	}
	parent := os.NewFile(uintptr(parentFD), parentPath)
	defer parent.Close()
	current, err := openDirectoryAt(parent, base)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return ErrUnsafePath
	}
	currentInfo, currentErr := current.Stat()
	_ = current.Close()
	lockedInfo, lockedErr := rootFile.Stat()
	if currentErr != nil || lockedErr != nil || !os.SameFile(currentInfo, lockedInfo) {
		return ErrUnsafePath
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := unix.Unlinkat(int(parent.Fd()), base, unix.AT_REMOVEDIR); err != nil {
		if errors.Is(err, unix.ENOENT) {
			return nil
		}
		if errors.Is(err, unix.ENOTDIR) || errors.Is(err, unix.ENOTEMPTY) {
			return ErrUnsafePath
		}
		return fmt.Errorf("%w: remove cache root", ErrIO)
	}
	return nil
}

func validateRootEntries(ctx context.Context, root *os.File) error {
	names, err := directoryNames(root)
	if err != nil {
		return fmt.Errorf("%w: list cache root", ErrIO)
	}
	for _, name := range names {
		if err := ctx.Err(); err != nil {
			return err
		}
		mode, err := entryMode(int(root.Fd()), name)
		if err != nil {
			return fmt.Errorf("%w: inspect cache root entry", ErrIO)
		}
		switch {
		case modeIsDirectory(mode):
			if !validNamespace(name) {
				return ErrUnsafePath
			}
			namespace, err := openDirectoryAt(root, name)
			if err != nil {
				return ErrUnsafePath
			}
			err = validateNamespaceEntries(ctx, namespace)
			_ = namespace.Close()
			if err != nil {
				return err
			}
		default:
			// The cache format stores entries below namespace directories. A
			// root-level file, including .cache-* temp-looking files, is
			// unknown and must abort cleanup rather than be deleted.
			return ErrUnsafePath
		}
	}
	return nil
}

func validateNamespaceEntries(ctx context.Context, namespace *os.File) error {
	names, err := directoryNames(namespace)
	if err != nil {
		return fmt.Errorf("%w: list cache namespace", ErrIO)
	}
	for _, name := range names {
		if err := ctx.Err(); err != nil {
			return err
		}
		if !validEntryFilename(name) {
			return ErrUnsafePath
		}
		mode, err := entryMode(int(namespace.Fd()), name)
		if err != nil || !modeIsRegular(mode) {
			return ErrUnsafePath
		}
	}
	return nil
}

func removeRootEntries(ctx context.Context, root *os.File) error {
	names, err := directoryNames(root)
	if err != nil {
		return fmt.Errorf("%w: list cache root", ErrIO)
	}
	for _, name := range names {
		if err := ctx.Err(); err != nil {
			return err
		}
		namespace, err := openDirectoryAt(root, name)
		if err != nil {
			return ErrUnsafePath
		}
		entries, listErr := directoryNames(namespace)
		if listErr != nil {
			_ = namespace.Close()
			return fmt.Errorf("%w: list cache namespace", ErrIO)
		}
		for _, entry := range entries {
			if err := ctx.Err(); err != nil {
				_ = namespace.Close()
				return err
			}
			if err := unix.Unlinkat(int(namespace.Fd()), entry, 0); err != nil {
				_ = namespace.Close()
				return fmt.Errorf("%w: remove cache entry", ErrIO)
			}
		}
		_ = namespace.Close()
		if err := unix.Unlinkat(int(root.Fd()), name, unix.AT_REMOVEDIR); err != nil {
			return fmt.Errorf("%w: remove cache namespace", ErrIO)
		}
	}
	return nil
}

func directoryNames(directory *os.File) ([]string, error) {
	duplicate, err := unix.Openat(int(directory.Fd()), ".", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	copy := os.NewFile(uintptr(duplicate), directory.Name())
	defer copy.Close()
	return copy.Readdirnames(-1)
}

func openDirectoryAt(parent *os.File, name string) (*os.File, error) {
	fd, err := unix.Openat(int(parent.Fd()), name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(fd), name), nil
}

func entryMode(directoryFD int, name string) (uint32, error) {
	var stat unix.Stat_t
	if err := unix.Fstatat(directoryFD, name, &stat, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return 0, err
	}
	return uint32(stat.Mode), nil
}

func modeIsDirectory(mode uint32) bool { return mode&unix.S_IFMT == unix.S_IFDIR }
func modeIsRegular(mode uint32) bool   { return mode&unix.S_IFMT == unix.S_IFREG }
