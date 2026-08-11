//go:build windows

package session

import (
	"fmt"

	"golang.org/x/sys/windows"
)

func directoryIdentity(path string) (string, error) {
	pathPointer, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return "", ErrUnsafePath
	}
	handle, err := windows.CreateFile(
		pathPointer,
		windows.FILE_READ_ATTRIBUTES,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil,
		windows.OPEN_EXISTING,
		windows.FILE_FLAG_BACKUP_SEMANTICS|windows.FILE_FLAG_OPEN_REPARSE_POINT,
		0,
	)
	if err != nil {
		return "", ErrWorkspaceUnavailable
	}
	defer windows.CloseHandle(handle)
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(handle, &info); err != nil ||
		info.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY == 0 ||
		info.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		return "", ErrUnsafePath
	}
	return fmt.Sprintf("windows:%08x:%08x%08x", info.VolumeSerialNumber, info.FileIndexHigh, info.FileIndexLow), nil
}

func validRootIdentity(identity string) bool {
	return len(identity) == len("windows:00000000:0000000000000000")
}

func validateOutputRootCapabilities(path string) error {
	// Traversal requires directory-entry file IDs so os.File.ReadDir can
	// compare a no-follow child handle to the parent-handle enumeration. A
	// volume without both capabilities is rejected here, rather than allowing
	// public root validation to succeed and maintenance to fail later.
	pathPointer, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return ErrUnsafePath
	}
	handle, err := windows.CreateFile(
		pathPointer,
		windows.FILE_READ_ATTRIBUTES,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil,
		windows.OPEN_EXISTING,
		windows.FILE_FLAG_BACKUP_SEMANTICS|windows.FILE_FLAG_OPEN_REPARSE_POINT,
		0,
	)
	if err != nil {
		return ErrWorkspaceUnavailable
	}
	defer windows.CloseHandle(handle)
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(handle, &info); err != nil ||
		info.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY == 0 || info.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		return ErrUnsafePath
	}
	var volumeFlags uint32
	if err := windows.GetVolumeInformationByHandle(handle, nil, 0, nil, nil, &volumeFlags, nil, 0); err != nil || !windowsDiscardStableVolumeCapabilities(volumeFlags) {
		return ErrWorkspaceUnavailable
	}
	return nil
}
