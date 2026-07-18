//go:build windows

package update

import (
	"errors"
	"os"

	"golang.org/x/sys/windows"
)

// The standard library exposes neither owner nor writable ACL evaluation on
// Windows. Product integration must select a per-user protected root; this
// function still rejects reparse-point roots through Lstat's symlink bit.
func validateDirectorySecurity(root string) error {
	info, err := os.Lstat(root)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return ErrUnsafePath
	}
	return nil
}

func secureRegular(info os.FileInfo) bool {
	return info.Mode().IsRegular() && info.Mode()&os.ModeSymlink == 0
}

func replaceFile(source, destination string) error {
	sourcePointer, err := windows.UTF16PtrFromString(source)
	if err != nil {
		return err
	}
	destinationPointer, err := windows.UTF16PtrFromString(destination)
	if err != nil {
		return err
	}
	return windows.MoveFileEx(sourcePointer, destinationPointer, windows.MOVEFILE_REPLACE_EXISTING|windows.MOVEFILE_WRITE_THROUGH)
}

func createLockObject(path string, owner []byte) (bool, error) {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return false, err
	}
	if _, err = file.Write(owner); err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		_ = os.Remove(path)
		return false, err
	}
	return true, nil
}

// Deleting or renaming a lock file while another process has just opened it
// can briefly report access denied on Windows. Treat that state as contention,
// never as proof that the updater lock is unusable.
func lockContention(err error) bool {
	return errors.Is(err, os.ErrExist) || errors.Is(err, os.ErrPermission) || errors.Is(err, windows.ERROR_SHARING_VIOLATION)
}

func validLockObject(info os.FileInfo) bool {
	return info.Mode().IsRegular() && info.Mode()&os.ModeSymlink == 0
}
func validateLockSecurity(path string) error {
	info, err := os.Lstat(path)
	if err != nil || !validLockObject(info) {
		return ErrUnsafePath
	}
	return nil
}
func readLockOwner(path string) ([]byte, error) { return os.ReadFile(path) }
func removeLockObject(path string)              { _ = os.Remove(path) }

func processAlive(pid int) bool {
	handle, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return errors.Is(err, windows.ERROR_ACCESS_DENIED)
	}
	defer windows.CloseHandle(handle)
	var exitCode uint32
	const stillActive = 259
	return windows.GetExitCodeProcess(handle, &exitCode) == nil && exitCode == stillActive
}

// Windows has no portable directory fsync primitive. Atomic pointer updates
// still use a synced temporary file followed by same-directory rename.
func syncDirectory(string) error { return nil }
