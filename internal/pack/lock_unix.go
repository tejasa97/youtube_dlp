//go:build darwin || linux

package pack

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// acquireLock uses a kernel advisory lock rather than lock-file existence.
// The file may persist, but the lock is released by the kernel if the process
// crashes, so no stale PID/token heuristic can steal a live installation.
func acquireLock(packRoot string) (func(), error) {
	path := filepath.Join(packRoot, ".install.lock")
	file, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, fmt.Errorf("%w: open install lock", ErrIO)
	}
	fail := func(err error) (func(), error) {
		_ = file.Close()
		return nil, err
	}
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 || !secureOwnership(info) || !singleLink(info) {
		return fail(ErrUnsafePath)
	}
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) {
			return fail(ErrLocked)
		}
		return fail(fmt.Errorf("%w: lock installation", ErrIO))
	}
	return func() {
		_ = syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
		_ = file.Close()
	}, nil
}
