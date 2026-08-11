//go:build windows

package ffmpeg

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"unsafe"

	"golang.org/x/sys/windows"
)

func createProtectedProcessingDirectory(path string) error {
	attributes, err := protectedProcessingSecurityAttributes()
	if err != nil {
		return err
	}
	pointer, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return err
	}
	return windows.CreateDirectory(pointer, &attributes)
}

func protectedProcessingSecurityAttributes() (windows.SecurityAttributes, error) {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return windows.SecurityAttributes{}, err
	}
	sid := user.User.Sid.String()
	descriptor, err := windows.SecurityDescriptorFromString("O:" + sid + "G:" + sid + "D:P(A;OICI;FA;;;" + sid + ")(A;OICI;FA;;;SY)(A;OICI;FA;;;BA)")
	if err != nil {
		return windows.SecurityAttributes{}, err
	}
	return windows.SecurityAttributes{Length: uint32(unsafe.Sizeof(windows.SecurityAttributes{})), SecurityDescriptor: descriptor}, nil
}

func processingDirectoryProtected(path string, _ os.FileInfo) (bool, error) {
	reparse, err := processingPathIsReparse(path)
	if err != nil || reparse {
		return false, err
	}
	descriptor, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil || descriptor == nil {
		return false, err
	}
	return processingSecurityDescriptorProtected(descriptor, true)
}

func processingSecurityDescriptorProtected(descriptor *windows.SECURITY_DESCRIPTOR, requireProtected bool) (bool, error) {
	if descriptor == nil {
		return false, nil
	}
	control, _, err := descriptor.Control()
	if err != nil || (requireProtected && control&windows.SE_DACL_PROTECTED == 0) {
		return false, err
	}
	owner, _, err := descriptor.Owner()
	if err != nil {
		return false, err
	}
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil || owner == nil || !owner.Equals(user.User.Sid) {
		return false, err
	}
	system, err := windows.CreateWellKnownSid(windows.WinLocalSystemSid)
	if err != nil {
		return false, err
	}
	admins, err := windows.CreateWellKnownSid(windows.WinBuiltinAdministratorsSid)
	if err != nil {
		return false, err
	}
	dacl, _, err := descriptor.DACL()
	if err != nil || dacl == nil {
		return false, err
	}
	ownerAllowed := false
	for index := uint32(0); index < uint32(dacl.AceCount); index++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(dacl, index, &ace); err != nil {
			return false, err
		}
		if ace == nil || ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE {
			return false, nil
		}
		sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
		switch {
		case sid.Equals(user.User.Sid):
			ownerAllowed = true
		case sid.Equals(system), sid.Equals(admins):
		default:
			return false, nil
		}
	}
	return ownerAllowed, nil
}

func processingFileLinkCount(path string) (uint64, error) {
	file, _, err := openProcessingEvidence(path)
	if err != nil {
		return 0, err
	}
	defer file.Close()
	var information windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(windows.Handle(file.Fd()), &information); err != nil {
		return 0, err
	}
	return uint64(information.NumberOfLinks), nil
}

func openProcessingWindowsHandle(path string, directory bool) (windows.Handle, error) {
	flags := uint32(windows.FILE_FLAG_OPEN_REPARSE_POINT)
	if directory {
		flags |= windows.FILE_FLAG_BACKUP_SEMANTICS
	}
	pointer, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return 0, err
	}
	handle, err := windows.CreateFile(pointer, windows.FILE_GENERIC_READ, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil, windows.OPEN_EXISTING, flags, 0)
	if err != nil {
		return 0, err
	}
	return handle, nil
}

func openProcessingEvidence(path string) (*os.File, os.FileInfo, error) {
	handle, err := openProcessingWindowsHandle(path, false)
	if err != nil {
		return nil, nil, err
	}
	file := os.NewFile(uintptr(handle), path)
	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return nil, nil, err
	}
	var information windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(handle, &information); err != nil {
		_ = file.Close()
		return nil, nil, err
	}
	if information.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 || !info.Mode().IsRegular() || information.NumberOfLinks != 1 {
		_ = file.Close()
		return nil, nil, fmt.Errorf("processing evidence is a reparse, non-regular, or linked file")
	}
	descriptor, err := windows.GetSecurityInfo(handle, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		_ = file.Close()
		return nil, nil, err
	}
	protected, err := processingSecurityDescriptorProtected(descriptor, false)
	if err != nil || !protected {
		_ = file.Close()
		if err == nil {
			err = fmt.Errorf("processing evidence ACL is not owner-authorized")
		}
		return nil, nil, err
	}
	return file, info, nil
}

func openProcessingDirectory(path string) (*os.File, error) {
	handle, err := openProcessingWindowsHandle(path, true)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(handle), path), nil
}

func hardenProcessingStage(path string) error {
	file, _, err := openProcessingEvidence(path)
	if err != nil {
		return err
	}
	return file.Close()
}

func processingPathIsReparse(path string) (bool, error) {
	handle, err := openProcessingWindowsHandle(path, true)
	if err != nil {
		return false, err
	}
	defer windows.CloseHandle(handle)
	var information windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(handle, &information); err != nil {
		return false, err
	}
	return information.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0, nil
}

func processingPathIdentity(path string) (string, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return "", err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("processing path is a reparse point")
	}
	handle, err := openProcessingWindowsHandle(path, info.IsDir())
	if err != nil {
		return "", err
	}
	defer windows.CloseHandle(handle)
	return processingObjectIdentity(handle)
}

func processingObjectIdentity(handle windows.Handle) (string, error) {
	var information windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(handle, &information); err != nil {
		return "", err
	}
	if information.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		return "", fmt.Errorf("processing path is a reparse point")
	}
	typeID := 0
	if information.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY != 0 {
		typeID = 1
	}
	return fmt.Sprintf("%d:%d:%d:%d", information.VolumeSerialNumber, information.FileIndexHigh, information.FileIndexLow, typeID), nil
}

func processingHandleIdentity(handle windows.Handle) (string, error) {
	var information windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(handle, &information); err != nil {
		return "", err
	}
	if information.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		return "", fmt.Errorf("processing path is a reparse point")
	}
	typeID := 0
	if information.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY != 0 {
		typeID = 1
	}
	return fmt.Sprintf("%d:%d:%d:%d:%d", information.VolumeSerialNumber, information.FileIndexHigh, information.FileIndexLow, typeID, information.NumberOfLinks), nil
}

func processingLeasePathIdentity(path string) (string, error) {
	handle, err := openProcessingWindowsHandle(path, false)
	if err != nil {
		return "", err
	}
	defer windows.CloseHandle(handle)
	return processingHandleIdentity(handle)
}

func removeProcessingLeasePath(path string) error {
	pointer, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return err
	}
	return windows.DeleteFile(pointer)
}

func openProcessingWindowsExistingLeaseHandle(path string) (windows.Handle, error) {
	pointer, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return 0, err
	}
	return windows.CreateFile(pointer, windows.GENERIC_READ|windows.GENERIC_WRITE, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil, windows.OPEN_EXISTING, windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
}

func openAndLockProcessingWindowsFile(path string, create bool) (windows.Handle, *os.File, *windows.Overlapped, string, error) {
	for {
		var handle windows.Handle
		var err error
		if create {
			handle, err = openProcessingLeaseHandle(path)
		} else {
			handle, err = openProcessingWindowsExistingLeaseHandle(path)
		}
		if errors.Is(err, windows.ERROR_FILE_EXISTS) {
			continue
		}
		if err != nil {
			return 0, nil, nil, "", err
		}
		file := os.NewFile(uintptr(handle), path)
		if err := validateProcessingLeaseFile(handle, file); err != nil {
			_ = file.Close()
			return 0, nil, nil, "", err
		}
		overlapped := &windows.Overlapped{}
		if err := windows.LockFileEx(handle, windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, 1, 0, overlapped); err != nil {
			_ = file.Close()
			return 0, nil, nil, "", err
		}
		identity, err := processingHandleIdentity(handle)
		if err != nil {
			_ = windows.UnlockFileEx(handle, 0, 1, 0, overlapped)
			_ = file.Close()
			return 0, nil, nil, "", err
		}
		namedIdentity, err := processingLeasePathIdentity(path)
		if err != nil || namedIdentity != identity {
			_ = windows.UnlockFileEx(handle, 0, 1, 0, overlapped)
			_ = file.Close()
			return 0, nil, nil, "", fmt.Errorf("%w: processing path does not name the locked file", errProcessingLeaseIdentity)
		}
		return handle, file, overlapped, identity, nil
	}
}

func acquireProcessingLease(path string) (*processingLease, error) {
	guardLockPath := filepath.Dir(path) + processingGuardLockSuffix
	guardLock, guardLockFile, guardLockOverlapped, guardLockIdentity, err := openAndLockProcessingWindowsFile(guardLockPath, true)
	if err != nil {
		return nil, err
	}
	leaseHandle, leaseFile, leaseOverlapped, identity, err := openAndLockProcessingWindowsFile(path, true)
	if err != nil {
		_ = windows.UnlockFileEx(guardLock, 0, 1, 0, guardLockOverlapped)
		_ = guardLockFile.Close()
		return nil, err
	}
	unlinked := false
	guardLockUnlinked := false
	return &processingLease{
		identity:          identity,
		guardLockIdentity: guardLockIdentity,
		validateFn: func(leaseHeld, guardLockHeld bool) error {
			if leaseHeld {
				current, err := processingHandleIdentity(leaseHandle)
				if err != nil || !processingLeaseHandleIdentityMatches(identity, current, unlinked) {
					return errProcessingLeaseIdentity
				}
			}
			if guardLockHeld {
				current, err := processingHandleIdentity(guardLock)
				if err != nil || !processingLeaseHandleIdentityMatches(guardLockIdentity, current, guardLockUnlinked) {
					return errProcessingLeaseIdentity
				}
			}
			return nil
		},
		removeFn: func() error {
			err := removeProcessingLeasePath(path)
			if err == nil {
				unlinked = true
			}
			return err
		},
		removeGuardLockFn: func() error {
			err := removeProcessingLeasePath(guardLockPath)
			if err == nil {
				guardLockUnlinked = true
			}
			return err
		},
		markFn: func(workspace ProcessingWorkspace) error {
			if err := markProcessingLeaseComplete(leaseFile, workspace); err != nil {
				return err
			}
			return markProcessingLeaseComplete(guardLockFile, workspace)
		},
		releaseLeaseFn: func() error {
			unlockErr := windows.UnlockFileEx(leaseHandle, 0, 1, 0, leaseOverlapped)
			closeErr := leaseFile.Close()
			return errors.Join(unlockErr, closeErr)
		},
		releaseGuardLockFn: func() error {
			unlockErr := windows.UnlockFileEx(guardLock, 0, 1, 0, guardLockOverlapped)
			closeErr := guardLockFile.Close()
			return errors.Join(unlockErr, closeErr)
		},
	}, nil
}

func acquireProcessingGuardRecovery(path string) (*processingGuardRecovery, error) {
	handle, file, overlapped, identity, err := openAndLockProcessingWindowsFile(path, false)
	if err != nil {
		return nil, err
	}
	unlinked := false
	return &processingGuardRecovery{
		identity: identity,
		validateFn: func() error {
			current, err := processingHandleIdentity(handle)
			if err != nil || !processingLeaseHandleIdentityMatches(identity, current, unlinked) {
				return errProcessingLeaseIdentity
			}
			return nil
		},
		removeFn: func() error {
			err := removeProcessingLeasePath(path)
			if err == nil {
				unlinked = true
			}
			return err
		},
		releaseFn: func() error {
			unlockErr := windows.UnlockFileEx(handle, 0, 1, 0, overlapped)
			closeErr := file.Close()
			return errors.Join(unlockErr, closeErr)
		},
	}, nil
}

func validateProcessingLeaseFile(handle windows.Handle, file *os.File) error {
	info, err := file.Stat()
	if err != nil {
		return err
	}
	var information windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(handle, &information); err != nil {
		return err
	}
	if information.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 || !info.Mode().IsRegular() || information.NumberOfLinks != 1 {
		return fmt.Errorf("processing lease is not a private single-link regular file")
	}
	descriptor, err := windows.GetSecurityInfo(handle, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return err
	}
	if protected, err := processingSecurityDescriptorProtected(descriptor, false); err != nil || !protected {
		if err == nil {
			err = fmt.Errorf("processing lease ACL is not owner-authorized")
		}
		return err
	}
	return nil
}

func openProcessingLeaseHandle(path string) (windows.Handle, error) {
	pointer, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return 0, err
	}
	flags := uint32(windows.FILE_FLAG_OPEN_REPARSE_POINT)
	handle, err := windows.CreateFile(pointer, windows.GENERIC_READ|windows.GENERIC_WRITE, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil, windows.OPEN_EXISTING, flags, 0)
	if err == nil {
		return handle, nil
	}
	if !errors.Is(err, windows.ERROR_FILE_NOT_FOUND) {
		return 0, err
	}
	attributes, attrErr := protectedProcessingSecurityAttributes()
	if attrErr != nil {
		return 0, attrErr
	}
	handle, err = windows.CreateFile(pointer, windows.GENERIC_READ|windows.GENERIC_WRITE, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, &attributes, windows.CREATE_NEW, flags, 0)
	if err != nil {
		return 0, err
	}
	return handle, nil
}

func syncProcessingDirectory(string) error { return nil }

func processingPublishNoClobber(source, destination string) error {
	file, err := os.OpenFile(source, os.O_WRONLY, 0)
	if err != nil {
		return &processingPublicationError{operation: "open source", cause: err}
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return &processingPublicationError{operation: "sync source", cause: err}
	}
	if err := file.Close(); err != nil {
		return &processingPublicationError{operation: "close source", cause: err}
	}
	sourcePointer, err := windows.UTF16PtrFromString(source)
	if err != nil {
		return err
	}
	destinationPointer, err := windows.UTF16PtrFromString(destination)
	if err != nil {
		return err
	}
	moveErr := windows.MoveFileEx(sourcePointer, destinationPointer, windows.MOVEFILE_WRITE_THROUGH)
	if moveErr == nil {
		return nil
	}
	sourceExists, sourceErr := processingPathExists(source)
	destinationExists, destinationErr := processingPathExists(destination)
	if sourceErr != nil || destinationErr != nil {
		return &processingPublicationError{operation: "inspect failed move", cause: errors.Join(moveErr, sourceErr, destinationErr), indeterminate: true}
	}
	switch {
	case !sourceExists && destinationExists:
		return &processingPublicationError{operation: "move reported failure after commit", cause: moveErr, committed: true}
	case sourceExists:
		return &processingPublicationError{operation: "move destination", cause: moveErr}
	default:
		return &processingPublicationError{operation: "locate failed move", cause: moveErr, indeterminate: true}
	}
}

func processingPathExists(path string) (bool, error) {
	_, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	return err == nil, err
}
