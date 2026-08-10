//go:build windows

package atomicfile

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

var replaceFileW = windows.NewLazySystemDLL("kernel32.dll").NewProc("ReplaceFileW")

func platformOpenForSync(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_WRONLY, 0)
}

func platformReplace(source, destination string) error {
	sourcePointer, err := windows.UTF16PtrFromString(source)
	if err != nil {
		return err
	}
	destinationPointer, err := windows.UTF16PtrFromString(destination)
	if err != nil {
		return err
	}

	if _, statErr := os.Lstat(destination); statErr == nil {
		backup, backupErr := reserveBackupName(destination)
		if backupErr != nil {
			return backupErr
		}
		backupPointer, pointerErr := windows.UTF16PtrFromString(backup)
		if pointerErr != nil {
			return pointerErr
		}
		result, _, callErr := replaceFileW.Call(
			uintptr(unsafe.Pointer(destinationPointer)),
			uintptr(unsafe.Pointer(sourcePointer)),
			uintptr(unsafe.Pointer(backupPointer)),
			0,
			0,
			0,
		)
		var replaceErr error
		if result != 0 {
			replaceErr = nil
		} else if !errors.Is(callErr, windows.ERROR_FILE_NOT_FOUND) &&
			!errors.Is(callErr, windows.ERROR_PATH_NOT_FOUND) {
			if callErr == syscall.Errno(0) {
				replaceErr = syscall.EINVAL
			} else {
				replaceErr = callErr
			}
		} else {
			// The destination disappeared after Lstat. ReplaceFileW did not
			// commit, so let MoveFileExW handle the creation/race below.
			_ = os.Remove(backup)
			return windows.MoveFileEx(
				sourcePointer,
				destinationPointer,
				windows.MOVEFILE_REPLACE_EXISTING|windows.MOVEFILE_WRITE_THROUGH,
			)
		}
		return handleExistingReplaceResult(replaceErr, backup, destination, replaceRecoveryOps{
			backupExists:      pathExists,
			destinationExists: pathExists,
			restoreBackup:     restoreReplacementBackup,
			removeBackup:      os.Remove,
		})
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return statErr
	}

	return windows.MoveFileEx(
		sourcePointer,
		destinationPointer,
		windows.MOVEFILE_REPLACE_EXISTING|windows.MOVEFILE_WRITE_THROUGH,
	)
}

func reserveBackupName(destination string) (string, error) {
	file, err := os.CreateTemp(filepath.Dir(destination), ".atomic-backup-*")
	if err != nil {
		return "", err
	}
	name := file.Name()
	if err := file.Close(); err != nil {
		_ = os.Remove(name)
		return "", err
	}
	if err := os.Remove(name); err != nil {
		return "", err
	}
	return name, nil
}

func pathExists(path string) (bool, error) {
	_, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	return err == nil, err
}

func restoreReplacementBackup(backup, destination string) error {
	backupPointer, err := windows.UTF16PtrFromString(backup)
	if err != nil {
		return err
	}
	destinationPointer, err := windows.UTF16PtrFromString(destination)
	if err != nil {
		return err
	}
	return windows.MoveFileEx(
		backupPointer,
		destinationPointer,
		windows.MOVEFILE_REPLACE_EXISTING|windows.MOVEFILE_WRITE_THROUGH,
	)
}

// Windows has no portable directory fsync operation. First creation and the
// race fallback use MoveFileExW's write-through flag; ReplaceFileW supplies
// the platform's atomic existing-file replacement operation.
func platformSyncParent(string) error { return nil }
