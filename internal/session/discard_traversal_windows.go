//go:build windows

package session

import (
	"fmt"
	"os"
	"path/filepath"
	"unsafe"

	"golang.org/x/sys/windows"
)

func openDiscardRoot(path, expectedIdentity string) (*discardDirectory, error) {
	file, err := openWindowsDiscardHandle(`\??\`+filepath.Clean(path), windows.FILE_DIRECTORY_FILE|windows.FILE_SYNCHRONOUS_IO_NONALERT, windows.Handle(0))
	if err != nil {
		return nil, err
	}
	info, details, err := windowsDiscardHandleInfo(file)
	if err != nil || !info.IsDir() {
		_ = file.Close()
		return nil, ErrUnsafePath
	}
	var volumeFlags uint32
	if err := windows.GetVolumeInformationByHandle(windows.Handle(file.Fd()), nil, 0, nil, nil, &volumeFlags, nil, 0); err != nil ||
		volumeFlags&(windows.FILE_SUPPORTS_OBJECT_IDS|windows.FILE_SUPPORTS_OPEN_BY_FILE_ID) != (windows.FILE_SUPPORTS_OBJECT_IDS|windows.FILE_SUPPORTS_OPEN_BY_FILE_ID) {
		_ = file.Close()
		return nil, ErrWorkspaceUnavailable
	}
	identity := windowsDiscardIdentity(details)
	if expectedIdentity != "" && identity != expectedIdentity {
		_ = file.Close()
		return nil, ErrNeedsReconciliation
	}
	return &discardDirectory{file: file, path: path, identity: identity}, nil
}

func openDiscardEntry(parent *discardDirectory, name string, expected os.FileInfo, wantDirectory bool) (*discardEntryHandle, error) {
	if parent == nil || parent.file == nil || !validDiscardEntryName(name) {
		return nil, ErrUnsafePath
	}
	options := uint32(windows.FILE_SYNCHRONOUS_IO_NONALERT)
	if wantDirectory {
		options |= windows.FILE_DIRECTORY_FILE
	} else {
		options |= windows.FILE_NON_DIRECTORY_FILE
	}
	file, err := openWindowsDiscardHandle(name, options, windows.Handle(parent.file.Fd()))
	if err != nil {
		return nil, err
	}
	info, details, err := windowsDiscardHandleInfo(file)
	if err != nil {
		_ = file.Close()
		return nil, err
	}
	if (details.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT) != 0 ||
		(wantDirectory && !info.IsDir()) || (!wantDirectory && !info.Mode().IsRegular()) {
		_ = file.Close()
		return nil, ErrUnsafePath
	}
	if expected != nil && !os.SameFile(expected, info) {
		_ = file.Close()
		return nil, ErrNeedsReconciliation
	}
	if !wantDirectory && details.NumberOfLinks != 1 {
		_ = file.Close()
		return nil, ErrUnsafePath
	}
	identity := windowsDiscardIdentity(details)
	return &discardEntryHandle{
		file: file, parent: parent, path: filepath.Join(parent.path, name), name: name,
		expected: info, identity: identity, directory: wantDirectory,
	}, nil
}

func (entry *discardEntryHandle) remove() error {
	if entry == nil || entry.file == nil {
		return ErrWorkspaceClosed
	}
	// Delete is committed against the opened object handle, not a pathname.
	// POSIX semantics also prevents a replacement name from being interpreted
	// as the object that was opened for this entry.
	info := windowsFileDispositionInfoEx{Flags: windows.FILE_DISPOSITION_DELETE | windows.FILE_DISPOSITION_POSIX_SEMANTICS | windows.FILE_DISPOSITION_IGNORE_READONLY_ATTRIBUTE}
	return windows.SetFileInformationByHandle(
		windows.Handle(entry.file.Fd()),
		windows.FileDispositionInfoEx,
		(*byte)(unsafe.Pointer(&info)),
		uint32(unsafe.Sizeof(info)),
	)
}

func syncDiscardDirectoryHandle(_ *discardDirectory) error { return nil }

func openWindowsDiscardHandle(name string, options uint32, parent windows.Handle) (*os.File, error) {
	objectName, err := windows.NewNTUnicodeString(name)
	if err != nil {
		return nil, ErrUnsafePath
	}
	attributes := &windows.OBJECT_ATTRIBUTES{
		Length:        uint32(unsafe.Sizeof(windows.OBJECT_ATTRIBUTES{})),
		RootDirectory: parent,
		ObjectName:    objectName,
		Attributes:    windows.OBJ_CASE_INSENSITIVE | windows.OBJ_DONT_REPARSE,
	}
	var handle windows.Handle
	var status windows.IO_STATUS_BLOCK
	err = windows.NtCreateFile(
		&handle,
		windows.FILE_GENERIC_READ|windows.DELETE,
		attributes,
		&status,
		nil,
		0,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		windows.FILE_OPEN,
		options,
		0,
		0,
	)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(handle), name), nil
}

func windowsDiscardHandleInfo(file *os.File) (os.FileInfo, windows.ByHandleFileInformation, error) {
	var details windows.ByHandleFileInformation
	handle := windows.Handle(file.Fd())
	if err := windows.GetFileInformationByHandle(handle, &details); err != nil {
		return nil, details, err
	}
	if details.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		return nil, details, ErrUnsafePath
	}
	info, err := file.Stat()
	return info, details, err
}

func windowsDiscardIdentity(info windows.ByHandleFileInformation) string {
	return fmt.Sprintf("windows:%08x:%08x%08x", info.VolumeSerialNumber, info.FileIndexHigh, info.FileIndexLow)
}

type windowsFileDispositionInfoEx struct {
	Flags uint32
}
