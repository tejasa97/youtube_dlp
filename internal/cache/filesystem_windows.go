//go:build windows

package cache

import (
	"errors"
	"fmt"
	"os"
	"unsafe"

	"golang.org/x/sys/windows"
)

const windowsFileAllAccess windows.ACCESS_MASK = 0x1f01ff

// secureDirectory establishes a protected owner-only ACL rather than relying
// on os.Chmod, whose mode bits do not control Windows access checks.
func secureDirectory(path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		if err := os.MkdirAll(path, 0o700); err != nil {
			return fmt.Errorf("%w: create directory", ErrIO)
		}
		info, err = os.Lstat(path)
	}
	if err != nil {
		return fmt.Errorf("%w: inspect directory", ErrIO)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || windowsReparsePoint(path) {
		return ErrUnsafePath
	}
	if !windowsCurrentOwner(path) || !setProtectedWindowsACL(path, true) || !secureWindowsACL(path, true) {
		return ErrUnsafePath
	}
	return nil
}

func secureExistingDirectory(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || windowsReparsePoint(path) || !secureWindowsACL(path, true) {
		return ErrUnsafePath
	}
	return nil
}

func secureFile(path string) error {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || windowsReparsePoint(path) {
		return ErrUnsafePath
	}
	if !windowsCurrentOwner(path) || !setProtectedWindowsACL(path, false) || !secureWindowsACL(path, false) {
		return ErrUnsafePath
	}
	return nil
}

func secureExistingFile(path string) bool {
	info, err := os.Lstat(path)
	return err == nil && info.Mode().IsRegular() && info.Mode()&os.ModeSymlink == 0 && !windowsReparsePoint(path) && secureWindowsACL(path, false)
}

func rejectExistingNonRegular(path string) error {
	_, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("%w: inspect entry", ErrIO)
	}
	if !secureExistingFile(path) {
		return ErrUnsafePath
	}
	return nil
}

func removeRegular(path string) error {
	if err := rejectExistingNonRegular(path); err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("%w: remove entry", ErrIO)
	}
	return nil
}

func windowsReparsePoint(path string) bool {
	pointer, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return true
	}
	attributes, err := windows.GetFileAttributes(pointer)
	return err != nil || attributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0
}

func windowsCurrentOwner(path string) bool {
	descriptor, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION)
	if err != nil || descriptor == nil || !descriptor.IsValid() {
		return false
	}
	owner, _, err := descriptor.Owner()
	user, userErr := windows.GetCurrentProcessToken().GetTokenUser()
	return err == nil && userErr == nil && owner != nil && user != nil && user.User.Sid != nil && owner.Equals(user.User.Sid)
}

func setProtectedWindowsACL(path string, directory bool) bool {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil || user == nil || user.User.Sid == nil {
		return false
	}
	inheritance := ""
	if directory {
		inheritance = "OICI"
	}
	sid := user.User.Sid.String()
	ace := func(principal string) string { return "(A;" + inheritance + ";FA;;;" + principal + ")" }
	descriptor, err := windows.SecurityDescriptorFromString("O:" + sid + "G:" + sid + "D:P" + ace(sid) + ace("SY") + ace("BA"))
	if err != nil || descriptor == nil {
		return false
	}
	acl, _, err := descriptor.DACL()
	if err != nil || acl == nil {
		return false
	}
	return windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT,
		windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION,
		user.User.Sid, nil, acl, nil) == nil
}

func secureWindowsACL(path string, directory bool) bool {
	descriptor, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil || descriptor == nil || !descriptor.IsValid() {
		return false
	}
	control, _, err := descriptor.Control()
	if err != nil || control&windows.SE_DACL_PROTECTED == 0 {
		return false
	}
	owner, _, err := descriptor.Owner()
	user, userErr := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil || userErr != nil || owner == nil || user == nil || user.User.Sid == nil || !owner.Equals(user.User.Sid) {
		return false
	}
	dacl, _, err := descriptor.DACL()
	if err != nil || dacl == nil || dacl.AceCount != 3 {
		return false
	}
	system, err := windows.CreateWellKnownSid(windows.WinLocalSystemSid)
	if err != nil {
		return false
	}
	administrators, err := windows.CreateWellKnownSid(windows.WinBuiltinAdministratorsSid)
	if err != nil {
		return false
	}
	wantFlags := byte(0)
	if directory {
		wantFlags = windows.OBJECT_INHERIT_ACE | windows.CONTAINER_INHERIT_ACE
	}
	seen := make(map[string]bool, 3)
	for index := uint32(0); index < uint32(dacl.AceCount); index++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(dacl, index, &ace); err != nil || ace == nil || ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE ||
			ace.Header.AceFlags != wantFlags || ace.Header.AceFlags&windows.INHERITED_ACE != 0 || ace.Mask != windowsFileAllAccess {
			return false
		}
		sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
		if !sid.IsValid() || seen[sid.String()] || (!sid.Equals(owner) && !sid.Equals(system) && !sid.Equals(administrators)) {
			return false
		}
		seen[sid.String()] = true
	}
	return seen[owner.String()] && seen[system.String()] && seen[administrators.String()]
}
