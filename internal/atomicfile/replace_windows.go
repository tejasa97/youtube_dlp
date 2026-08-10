//go:build windows

package atomicfile

import (
	"errors"
	"os"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

var replaceFileW = windows.NewLazySystemDLL("kernel32.dll").NewProc("ReplaceFileW")

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
		result, _, callErr := replaceFileW.Call(
			uintptr(unsafe.Pointer(destinationPointer)),
			uintptr(unsafe.Pointer(sourcePointer)),
			0,
			0,
			0,
			0,
		)
		if result != 0 {
			return nil
		}
		if !errors.Is(callErr, windows.ERROR_FILE_NOT_FOUND) &&
			!errors.Is(callErr, windows.ERROR_PATH_NOT_FOUND) {
			if callErr == syscall.Errno(0) {
				return syscall.EINVAL
			}
			return callErr
		}
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return statErr
	}

	return windows.MoveFileEx(
		sourcePointer,
		destinationPointer,
		windows.MOVEFILE_REPLACE_EXISTING|windows.MOVEFILE_WRITE_THROUGH,
	)
}

// Windows has no portable directory fsync operation. First creation and the
// race fallback use MoveFileExW's write-through flag; ReplaceFileW supplies
// the platform's atomic existing-file replacement operation.
func platformSyncParent(string) error { return nil }
