//go:build !windows

package update

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
)

func validateDirectorySecurity(root string) error {
	info, err := os.Lstat(root)
	if err != nil || !info.IsDir() || info.Mode().Perm()&0o022 != 0 {
		return ErrUnsafePath
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || int(stat.Uid) != os.Geteuid() {
		return ErrUnsafePath
	}
	return nil
}

func secureRegular(info os.FileInfo) bool {
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return false
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && stat.Nlink == 1 && int(stat.Uid) == os.Geteuid()
}

func replaceFile(source, destination string) error { return os.Rename(source, destination) }

func createLockObject(path string, owner []byte) (bool, error) {
	if err := os.Mkdir(path, 0o700); err != nil {
		return false, err
	}
	if err := os.WriteFile(filepath.Join(path, "owner"), owner, 0o600); err != nil {
		_ = os.RemoveAll(path)
		return false, err
	}
	return true, nil
}

func lockContention(err error) bool { return errors.Is(err, os.ErrExist) }
func validLockObject(info os.FileInfo) bool {
	return info.IsDir() && info.Mode()&os.ModeSymlink == 0
}
func validateLockSecurity(path string) error { return validateDirectorySecurity(path) }
func readLockOwner(path string) ([]byte, error) {
	return os.ReadFile(filepath.Join(path, "owner"))
}
func removeLockObject(path string) { _ = os.RemoveAll(path) }

func processAlive(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil || err == syscall.EPERM
}

func syncDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}
