package session

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"
)

type workspaceLease struct {
	mu     sync.Mutex
	file   *os.File
	native nativeLease
	closed bool
}

func acquireWorkspaceLease(path string, create, writeMetadata bool) (*workspaceLease, error) {
	if path == "" || !filepath.IsAbs(path) || filepath.Base(path) != LeaseFileName || containsNUL(path) {
		return nil, ErrUnsafePath
	}
	info, statErr := os.Lstat(path)
	created := false
	if statErr == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || !ownerOnlyFileAt(path, info) {
			return nil, ErrUnsafePath
		}
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return nil, ErrLeaseUnavailable
	} else if !create {
		return nil, ErrMissingLease
	} else {
		created = true
	}
	flags := os.O_RDWR
	if create {
		flags |= os.O_CREATE
	}
	file, err := os.OpenFile(path, flags, 0o600)
	if err != nil {
		return nil, ErrLeaseUnavailable
	}
	info, err = file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || !ownerOnlyFileAt(path, info) {
		_ = file.Close()
		return nil, ErrUnsafePath
	}
	if created {
		if err := secureFilePath(path); err != nil {
			_ = file.Close()
			return nil, err
		}
	}
	info, err = file.Stat()
	if err != nil || !info.Mode().IsRegular() || !ownerOnlyFileAt(path, info) {
		_ = file.Close()
		return nil, ErrPermissionUnavailable
	}
	native, err := tryNativeLock(file)
	if err != nil {
		_ = file.Close()
		return nil, err
	}
	lease := &workspaceLease{file: file, native: native}
	if writeMetadata {
		// Holder metadata is best-effort diagnostics. It never participates in
		// lease acquisition, stale detection, or release decisions.
		_ = writeHolderMetadata(file)
	}
	return lease, nil
}

func inspectWorkspaceLease(path string) (bool, error) {
	if path == "" || !filepath.IsAbs(path) || filepath.Base(path) != LeaseFileName || containsNUL(path) {
		return false, ErrUnsafePath
	}
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, ErrMissingLease
	}
	if err != nil {
		return false, ErrLeaseUnavailable
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || !ownerOnlyFileAt(path, info) {
		return false, ErrUnsafePath
	}
	file, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		return false, ErrLeaseUnavailable
	}
	native, err := tryNativeLock(file)
	if errors.Is(err, ErrLeaseContended) {
		_ = file.Close()
		return true, nil
	}
	if err != nil {
		_ = file.Close()
		return false, err
	}
	unlockErr := releaseNativeLock(file, native)
	closeErr := file.Close()
	if unlockErr != nil || closeErr != nil {
		return false, ErrLeaseUnavailable
	}
	return false, nil
}

func (lease *workspaceLease) Close() error {
	if lease == nil {
		return nil
	}
	lease.mu.Lock()
	defer lease.mu.Unlock()
	if lease.closed {
		return nil
	}
	lease.closed = true
	unlockErr := releaseNativeLock(lease.file, lease.native)
	closeErr := lease.file.Close()
	if unlockErr != nil || closeErr != nil {
		return ErrLeaseUnavailable
	}
	return nil
}

type holderMetadata struct {
	PID        int       `json:"pid"`
	AcquiredAt time.Time `json:"acquired_at"`
}

func writeHolderMetadata(file *os.File) error {
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return err
	}
	if err := file.Truncate(0); err != nil {
		return err
	}
	metadata := holderMetadata{PID: os.Getpid(), AcquiredAt: time.Now().UTC()}
	if err := json.NewEncoder(file).Encode(metadata); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	_, err := file.Seek(0, io.SeekStart)
	return err
}
