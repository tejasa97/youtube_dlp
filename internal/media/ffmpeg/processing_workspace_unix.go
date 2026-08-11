//go:build !windows

package ffmpeg

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

func createProtectedProcessingDirectory(path string) error { return os.Mkdir(path, 0o700) }

func processingDirectoryProtected(_ string, info os.FileInfo) (bool, error) {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return false, fmt.Errorf("inspect processing directory owner")
	}
	return info.Mode().Perm()&0o077 == 0 && uint32(stat.Uid) == uint32(os.Getuid()), nil
}

func processingFileLinkCount(path string) (uint64, error) {
	file, info, err := openProcessingEvidence(path)
	if err != nil {
		return 0, err
	}
	defer file.Close()
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, fmt.Errorf("inspect processing file link count")
	}
	return uint64(stat.Nlink), nil
}

func openProcessingEvidence(path string) (*os.File, os.FileInfo, error) {
	file, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, nil, err
	}
	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return nil, nil, err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || stat.Nlink != 1 || uint32(stat.Uid) != uint32(os.Getuid()) || info.Mode().Perm()&0o077 != 0 {
		_ = file.Close()
		return nil, nil, fmt.Errorf("processing evidence is not a private regular owner file")
	}
	return file, info, nil
}

func openProcessingDirectory(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
}

func hardenProcessingStage(path string) error {
	file, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.Mode().IsRegular() || stat.Nlink != 1 || uint32(stat.Uid) != uint32(os.Getuid()) {
		return fmt.Errorf("processing output is not an owned regular file")
	}
	return file.Chmod(0o600)
}

func processingPathIsReparse(_ string) (bool, error) { return false, nil }

func processingPathIdentity(path string) (string, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return "", err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("processing path is a symlink")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return "", fmt.Errorf("inspect processing path identity")
	}
	return fmt.Sprintf("%d:%d:%o", stat.Dev, stat.Ino, info.Mode()), nil
}

func processingHandleIdentity(file *os.File) (string, error) {
	info, err := file.Stat()
	if err != nil {
		return "", err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return "", fmt.Errorf("inspect processing lease handle identity")
	}
	return fmt.Sprintf("%d:%d:%o:%d", stat.Dev, stat.Ino, info.Mode().Type(), stat.Nlink), nil
}

func processingLeasePathIdentity(path string) (string, error) {
	file, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return "", err
	}
	defer file.Close()
	return processingHandleIdentity(file)
}

func removeProcessingLeasePath(path string) error { return os.Remove(path) }

func openAndLockProcessingUnixFile(path string, create bool) (*os.File, string, error) {
	for {
		file, err := os.OpenFile(path, os.O_RDWR|syscall.O_NOFOLLOW, 0)
		if errors.Is(err, os.ErrNotExist) {
			if !create {
				return nil, "", err
			}
			file, err = os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, 0o600)
			if errors.Is(err, os.ErrExist) {
				continue
			}
		}
		if err != nil {
			return nil, "", err
		}
		if err := validateProcessingLeaseFile(file); err != nil {
			_ = file.Close()
			return nil, "", err
		}
		if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
			_ = file.Close()
			return nil, "", err
		}
		identity, err := processingHandleIdentity(file)
		if err != nil {
			_ = syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
			_ = file.Close()
			return nil, "", err
		}
		namedIdentity, err := processingLeasePathIdentity(path)
		if err != nil || namedIdentity != identity {
			_ = syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
			_ = file.Close()
			return nil, "", fmt.Errorf("%w: processing path does not name the locked file", errProcessingLeaseIdentity)
		}
		return file, identity, nil
	}
}

func releaseProcessingUnixFile(file *os.File) error {
	if file == nil {
		return nil
	}
	unlockErr := syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
	closeErr := file.Close()
	return errors.Join(unlockErr, closeErr)
}

func acquireProcessingLease(path string) (*processingLease, error) {
	guardLockPath := filepath.Dir(path) + processingGuardLockSuffix
	guardLock, guardLockIdentity, err := openAndLockProcessingUnixFile(guardLockPath, true)
	if err != nil {
		return nil, err
	}
	leaseFile, identity, err := openAndLockProcessingUnixFile(path, true)
	if err != nil {
		_ = releaseProcessingUnixFile(guardLock)
		return nil, err
	}
	unlinked := false
	guardLockUnlinked := false
	return &processingLease{
		identity:          identity,
		guardLockIdentity: guardLockIdentity,
		validateFn: func(leaseHeld, guardLockHeld bool) error {
			if leaseHeld {
				current, err := processingHandleIdentity(leaseFile)
				if err != nil || !processingLeaseHandleIdentityMatches(identity, current, unlinked) {
					return errProcessingLeaseIdentity
				}
			}
			if guardLockHeld {
				current, err := processingHandleIdentity(guardLock)
				if err != nil || !processingLeaseHandleIdentityMatches(guardLockIdentity, current, guardLockUnlinked) {
					return errProcessingLeaseIdentity
				}
			}
			return nil
		},
		removeFn: func() error {
			err := removeProcessingLeasePath(path)
			if err == nil {
				unlinked = true
			}
			return err
		},
		removeGuardLockFn: func() error {
			err := removeProcessingLeasePath(guardLockPath)
			if err == nil {
				guardLockUnlinked = true
			}
			return err
		},
		markFn: func(workspace ProcessingWorkspace) error {
			if err := markProcessingLeaseComplete(leaseFile, workspace); err != nil {
				return err
			}
			return markProcessingLeaseComplete(guardLock, workspace)
		},
		releaseLeaseFn:     func() error { return releaseProcessingUnixFile(leaseFile) },
		releaseGuardLockFn: func() error { return releaseProcessingUnixFile(guardLock) },
	}, nil
}

func acquireProcessingGuardRecovery(path string) (*processingGuardRecovery, error) {
	file, identity, err := openAndLockProcessingUnixFile(path, false)
	if err != nil {
		return nil, err
	}
	unlinked := false
	return &processingGuardRecovery{
		identity: identity,
		validateFn: func() error {
			current, err := processingHandleIdentity(file)
			if err != nil || !processingLeaseHandleIdentityMatches(identity, current, unlinked) {
				return errProcessingLeaseIdentity
			}
			return nil
		},
		removeFn: func() error {
			err := removeProcessingLeasePath(path)
			if err == nil {
				unlinked = true
			}
			return err
		},
		releaseFn: func() error { return releaseProcessingUnixFile(file) },
	}, nil
}

func validateProcessingLeaseFile(file *os.File) error {
	info, err := file.Stat()
	if err != nil {
		return err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.Mode().IsRegular() || stat.Nlink != 1 || uint32(stat.Uid) != uint32(os.Getuid()) || info.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("processing lease is not a private owner-only single-link regular file")
	}
	return nil
}

func syncProcessingDirectory(path string) error {
	directory, err := openProcessingDirectory(path)
	if err != nil {
		return err
	}
	if err := directory.Sync(); err != nil {
		_ = directory.Close()
		return err
	}
	return directory.Close()
}

func processingPublishNoClobber(source, destination string) error {
	file, err := os.Open(source)
	if err != nil {
		return &processingPublicationError{operation: "open source", cause: err}
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return &processingPublicationError{operation: "sync source", cause: err}
	}
	if err := file.Close(); err != nil {
		return &processingPublicationError{operation: "close source", cause: err}
	}
	if err := os.Link(source, destination); err != nil {
		return &processingPublicationError{operation: "install destination link", cause: err}
	}
	if err := syncProcessingDirectory(filepath.Dir(destination)); err != nil {
		return &processingPublicationError{operation: "sync destination directory", cause: err, committed: true}
	}
	if err := os.Remove(source); err != nil {
		return &processingPublicationError{operation: "remove source name", cause: err, committed: true}
	}
	if err := syncProcessingDirectory(filepath.Dir(source)); err != nil {
		return &processingPublicationError{operation: "sync source directory", cause: err, committed: true}
	}
	return nil
}
