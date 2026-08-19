//go:build windows

package cache

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/sys/windows"
)

const cacheRootLockName = ".ytdlp-cache.lock"

// lockCacheRoot takes an exclusive byte-range lock on a private sentinel in
// the cache root. Unlike an open directory handle, LockFileEx is a real
// cross-process exclusion primitive on Windows. The separately retained root
// handle has no delete sharing: it pins the verified directory identity until
// unlock, so the root cannot be swapped for a junction while a cache operation
// is in progress.
func lockCacheRoot(ctx context.Context, root string) (*cacheRootLock, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	rootHandle, rootInfo, err := openVerifiedRoot(root)
	if err != nil {
		return nil, err
	}
	closeRoot := func(lockErr error) (*cacheRootLock, error) {
		_ = windows.CloseHandle(rootHandle)
		return nil, lockErr
	}

	lockPath := filepath.Join(root, cacheRootLockName)
	path, err := windows.UTF16PtrFromString(lockPath)
	if err != nil {
		return closeRoot(ErrUnsafePath)
	}
	handle, err := windows.CreateFile(path,
		windows.GENERIC_READ|windows.GENERIC_WRITE,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE,
		nil, windows.OPEN_ALWAYS,
		windows.FILE_ATTRIBUTE_HIDDEN|windows.FILE_FLAG_OPEN_REPARSE_POINT,
		0)
	if err != nil {
		return closeRoot(fmt.Errorf("%w: open cache root lock", ErrIO))
	}
	file := os.NewFile(uintptr(handle), lockPath)
	closeAll := func(lockErr error) (*cacheRootLock, error) {
		_ = file.Close()
		_ = windows.CloseHandle(rootHandle)
		return nil, lockErr
	}
	if err := secureFile(lockPath); err != nil {
		return closeAll(err)
	}
	var lockInfo windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(handle, &lockInfo); err != nil ||
		lockInfo.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 ||
		lockInfo.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY != 0 ||
		lockInfo.NumberOfLinks != 1 {
		return closeAll(ErrUnsafePath)
	}

	overlapped := windows.Overlapped{}
	for {
		if err := ctx.Err(); err != nil {
			return closeAll(err)
		}
		err = windows.LockFileEx(handle, windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, 1, 0, &overlapped)
		if err == nil {
			break
		}
		if !errors.Is(err, windows.ERROR_LOCK_VIOLATION) {
			return closeAll(fmt.Errorf("%w: lock cache root", ErrIO))
		}
		timer := time.NewTimer(10 * time.Millisecond)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return closeAll(ctx.Err())
		case <-timer.C:
		}
	}
	// The root handle blocks replace/delete, but repeat the handle identity
	// check after taking the inter-process lock before trusting its children.
	var lockedRoot windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(rootHandle, &lockedRoot); err != nil || !sameWindowsFile(rootInfo, lockedRoot) ||
		lockedRoot.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 ||
		lockedRoot.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY == 0 {
		_ = windows.UnlockFileEx(handle, 0, 1, 0, &overlapped)
		return closeAll(ErrUnsafePath)
	}
	return &cacheRootLock{file: file, extra: os.NewFile(uintptr(rootHandle), root)}, nil
}

func unlockCacheRoot(lock *cacheRootLock) {
	if lock == nil {
		return
	}
	if lock.file != nil {
		_ = windows.UnlockFileEx(windows.Handle(lock.file.Fd()), 0, 1, 0, &windows.Overlapped{})
		_ = lock.file.Close()
	}
	if lock.extra != nil {
		_ = lock.extra.Close()
	}
}

func openVerifiedRoot(root string) (windows.Handle, windows.ByHandleFileInformation, error) {
	var empty windows.ByHandleFileInformation
	path, err := windows.UTF16PtrFromString(root)
	if err != nil {
		return 0, empty, ErrUnsafePath
	}
	handle, err := windows.CreateFile(path, windows.GENERIC_READ|windows.READ_CONTROL,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, nil, windows.OPEN_EXISTING,
		windows.FILE_FLAG_BACKUP_SEMANTICS|windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		if errors.Is(err, windows.ERROR_FILE_NOT_FOUND) || errors.Is(err, windows.ERROR_PATH_NOT_FOUND) {
			return 0, empty, os.ErrNotExist
		}
		return 0, empty, fmt.Errorf("%w: open cache root", ErrIO)
	}
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(handle, &info); err != nil ||
		info.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 ||
		info.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY == 0 {
		_ = windows.CloseHandle(handle)
		return 0, empty, ErrUnsafePath
	}
	pathInfo, err := os.Lstat(root)
	if err != nil || !pathInfo.IsDir() || pathInfo.Mode()&os.ModeSymlink != 0 {
		_ = windows.CloseHandle(handle)
		if err != nil {
			return 0, empty, err
		}
		return 0, empty, ErrUnsafePath
	}
	return handle, info, nil
}

func sameWindowsFile(left, right windows.ByHandleFileInformation) bool {
	return left.VolumeSerialNumber == right.VolumeSerialNumber && left.FileIndexHigh == right.FileIndexHigh && left.FileIndexLow == right.FileIndexLow
}

// Descriptor-relative safe deletion is not exposed by the Go Windows API.
// Keep root-wide removal fail-closed rather than releasing the identity-pinning
// handle and recursively following a path. Namespace clear remains supported.
func removeRootLocked(context.Context, string, *cacheRootLock) error { return ErrUnsafePath }
