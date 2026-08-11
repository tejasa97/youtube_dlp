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

func platformCleanupTemp(path string) error {
	if err := os.Chmod(path, 0o600); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
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
			cleanupErr := os.Remove(backup)
			moveErr := moveFileEx(
				sourcePointer,
				destinationPointer,
				source,
				destination,
			)
			if moveErr != nil && cleanupErr != nil && !errors.Is(cleanupErr, os.ErrNotExist) {
				var commitErr CommitError
				if errors.As(moveErr, &commitErr) && commitErr.Indeterminate() {
					return indeterminateFailure(
						"clean replacement backup after failed move",
						errors.Join(moveErr, cleanupErr),
					)
				}
				return failure(
					"clean replacement backup after failed move",
					errors.Join(moveErr, cleanupErr),
					errors.As(moveErr, &commitErr) && commitErr.Committed(),
				)
			}
			if moveErr != nil {
				return moveErr
			}
			if cleanupErr != nil && !errors.Is(cleanupErr, os.ErrNotExist) {
				return failure("clean replacement backup after move", cleanupErr, true)
			}
			return nil
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

	return moveFileEx(
		sourcePointer,
		destinationPointer,
		source,
		destination,
	)
}

type moveRecoveryOps struct {
	sourceExists      func(string) (bool, error)
	destinationExists func(string) (bool, error)
}

// handleMoveFileFailure classifies a failed MoveFileExW by inspecting both
// names. MoveFileExW has no backup/recovery protocol, so a missing source and
// present destination is the only observable committed outcome; contradictory
// or uninspectable names require reconciliation.
func handleMoveFileFailure(
	moveErr error,
	source, destination string,
	ops moveRecoveryOps,
) error {
	sourceExists, sourceErr := ops.sourceExists(source)
	if sourceErr != nil {
		return indeterminateFailure(
			"inspect source after failed move",
			errors.Join(moveErr, sourceErr),
		)
	}
	destinationExists, destinationErr := ops.destinationExists(destination)
	if destinationErr != nil {
		return indeterminateFailure(
			"inspect destination after failed move",
			errors.Join(moveErr, destinationErr),
		)
	}
	switch {
	case !sourceExists && destinationExists:
		return failure("replace destination", moveErr, true)
	case sourceExists:
		return failure("replace destination", moveErr, false)
	default:
		return indeterminateFailure(
			"locate files after failed move",
			errors.Join(moveErr, errors.New("source and destination are both missing")),
		)
	}
}

func moveFileEx(
	sourcePointer, destinationPointer *uint16,
	source, destination string,
) error {
	moveErr := windows.MoveFileEx(
		sourcePointer,
		destinationPointer,
		windows.MOVEFILE_REPLACE_EXISTING|windows.MOVEFILE_WRITE_THROUGH,
	)
	if moveErr == nil {
		return nil
	}
	return handleMoveFileFailure(moveErr, source, destination, moveRecoveryOps{
		sourceExists:      pathExists,
		destinationExists: pathExists,
	})
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
