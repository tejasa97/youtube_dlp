//go:build windows

package session

import (
	"os"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Windows ACLs are the owner-only primitive. The ACL is protected from
// inheritance and grants access only to the current user, LocalSystem, and
// Builtin Administrators. Directory ACEs inherit only to descendants in this
// workspace tree.
func secureDirectoryPath(path string) error { return setProtectedWindowsACL(path, true) }

func secureFilePath(path string) error { return setProtectedWindowsACL(path, false) }

func ownerOnlyDirectory(info os.FileInfo) bool { return info.IsDir() }

func ownerOnlyDirectoryAt(path string, info os.FileInfo) bool {
	return info.IsDir() && secureWindowsACL(path, true)
}

func ownerOnlyDirectoryFromPath(path string) bool {
	info, err := os.Lstat(path)
	return err == nil && ownerOnlyDirectoryAt(path, info)
}

func ownerOnlyFile(info os.FileInfo) bool { return info.Mode().IsRegular() }

func ownerOnlyFileAt(path string, info os.FileInfo) bool {
	return info.Mode().IsRegular() && secureWindowsACL(path, false)
}

// FILE_ALL_ACCESS is the object-specific expansion of SDDL FA for file-system
// objects. Generic-all ACEs are mapped by Windows when applied to a file and
// therefore are not stable evidence to compare after a round trip.
const windowsFileAllAccess windows.ACCESS_MASK = 0x1f01ff

func setProtectedWindowsACL(path string, directory bool) error {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil || user == nil || user.User.Sid == nil {
		return ErrPermissionUnavailable
	}
	inheritance := ""
	if directory {
		inheritance = "OICI"
	}
	// Build the exact protected allow-only DACL. SetEntriesInAcl can normalize
	// explicit entries differently across Windows versions; SDDL keeps the
	// intended owner, group, principals, and inheritance flags unambiguous.
	sid := user.User.Sid.String()
	ace := func(principal string) string { return "(A;" + inheritance + ";FA;;;" + principal + ")" }
	sddl := "O:" + sid + "G:" + sid + "D:P" + ace(sid) + ace("SY") + ace("BA")
	descriptor, err := windows.SecurityDescriptorFromString(sddl)
	if err != nil || descriptor == nil {
		return ErrPermissionUnavailable
	}
	acl, _, err := descriptor.DACL()
	if err != nil || acl == nil {
		return ErrPermissionUnavailable
	}
	if err := windows.SetNamedSecurityInfo(
		path,
		windows.SE_FILE_OBJECT,
		windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION,
		user.User.Sid,
		nil,
		acl,
		nil,
	); err != nil {
		return ErrPermissionUnavailable
	}
	return nil
}

func secureWindowsACL(path string, directory bool) bool {
	descriptor, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	return err == nil && secureWindowsACLDescriptor(descriptor, directory)
}

func secureWindowsACLHandle(handle windows.Handle, directory bool) bool {
	descriptor, err := windows.GetSecurityInfo(handle, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	return err == nil && secureWindowsACLDescriptor(descriptor, directory)
}

func secureWindowsACLDescriptor(descriptor *windows.SECURITY_DESCRIPTOR, directory bool) bool {
	if descriptor == nil || !descriptor.IsValid() {
		return false
	}
	control, _, err := descriptor.Control()
	if err != nil || control&windows.SE_DACL_PROTECTED == 0 {
		return false
	}
	owner, _, err := descriptor.Owner()
	if err != nil || owner == nil {
		return false
	}
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil || user == nil || user.User.Sid == nil || !owner.Equals(user.User.Sid) {
		return false
	}
	dacl, _, err := descriptor.DACL()
	if err != nil || dacl == nil {
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
	if dacl.AceCount != 3 {
		return false
	}
	seen := make(map[string]bool, 3)
	wantFlags := byte(0)
	if directory {
		wantFlags = windows.OBJECT_INHERIT_ACE | windows.CONTAINER_INHERIT_ACE
	}
	for index := uint32(0); index < uint32(dacl.AceCount); index++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(dacl, index, &ace); err != nil || ace == nil {
			return false
		}
		if ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE ||
			ace.Header.AceFlags != wantFlags ||
			ace.Mask != windowsFileAllAccess || ace.Header.AceFlags&windows.INHERITED_ACE != 0 {
			return false
		}
		sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
		if !sid.IsValid() {
			return false
		}
		key := sid.String()
		if seen[key] || (!sid.Equals(owner) && !sid.Equals(system) && !sid.Equals(administrators)) {
			return false
		}
		seen[key] = true
	}
	return len(seen) == 3 && seen[owner.String()] && seen[system.String()] && seen[administrators.String()]
}
