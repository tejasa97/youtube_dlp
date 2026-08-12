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
		if created {
			flags |= os.O_EXCL
		}
	}
	file, err := os.OpenFile(path, flags, 0o600)
	if err != nil && created && errors.Is(err, os.ErrExist) {
		// Another process won the create race. Reopen its lease and apply the
		// ordinary strict validation below; never treat a raced-in file as ours.
		created = false
		file, err = os.OpenFile(path, os.O_RDWR, 0o600)
	}
	if err != nil {
		return nil, ErrLeaseUnavailable
	}
	info, err = file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		_ = file.Close()
		return nil, ErrUnsafePath
	}
	if created {
		if err := secureFilePath(path); err != nil {
			_ = file.Close()
			return nil, err
		}
	}
	current, statErr := os.Lstat(path)
	if statErr != nil || current.Mode()&os.ModeSymlink != 0 || !current.Mode().IsRegular() || !os.SameFile(info, current) || !ownerOnlyFileAt(path, current) {
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
