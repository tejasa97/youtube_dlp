//go:build windows

package session

import (
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/windows"
)

func TestWindowsOwnerOnlyACLIsAppliedAndValidated(t *testing.T) {
	directory := t.TempDir()
	if err := secureDirectoryPath(directory); err != nil {
		t.Fatal(err)
	}
	if !secureWindowsACL(directory) {
		t.Fatal("owner-only directory ACL was not validated")
	}
	filePath := filepath.Join(directory, "state")
	file, err := os.OpenFile(filePath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if err := secureFilePath(filePath); err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(filePath)
	if err != nil {
		t.Fatal(err)
	}
	if !ownerOnlyFileAt(filePath, info) {
		t.Fatal("owner-only file ACL was not validated")
	}
}

func TestWindowsOwnerOnlyACLRejectsUnprotectedAndUnauthorizedInheritedAccess(t *testing.T) {
	t.Run("unprotected dacl", func(t *testing.T) {
		directory := t.TempDir()
		if err := secureDirectoryPath(directory); err != nil {
			t.Fatal(err)
		}
		descriptor, err := windows.GetNamedSecurityInfo(directory, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
		if err != nil {
			t.Fatal(err)
		}
		dacl, _, err := descriptor.DACL()
		if err != nil || dacl == nil {
			t.Fatalf("read dacl: %v", err)
		}
		if err := windows.SetNamedSecurityInfo(
			directory,
			windows.SE_FILE_OBJECT,
			windows.DACL_SECURITY_INFORMATION|windows.UNPROTECTED_DACL_SECURITY_INFORMATION,
			nil,
			nil,
			dacl,
			nil,
		); err != nil {
			t.Fatal(err)
		}
		if secureWindowsACL(directory) {
			t.Fatal("unprotected DACL was accepted")
		}
	})

	t.Run("unauthorized inherit-only ace", func(t *testing.T) {
		directory := t.TempDir()
		user, err := windows.GetCurrentProcessToken().GetTokenUser()
		if err != nil {
			t.Fatal(err)
		}
		everyone, err := windows.CreateWellKnownSid(windows.WinWorldSid)
		if err != nil {
			t.Fatal(err)
		}
		entries := []windows.EXPLICIT_ACCESS{
			{
				AccessPermissions: windows.GENERIC_ALL,
				AccessMode:        windows.SET_ACCESS,
				Inheritance:       windows.NO_INHERITANCE,
				Trustee: windows.TRUSTEE{
					TrusteeForm: windows.TRUSTEE_IS_SID, TrusteeType: windows.TRUSTEE_IS_USER,
					TrusteeValue: windows.TrusteeValueFromSID(user.User.Sid),
				},
			},
			{
				AccessPermissions: windows.GENERIC_READ,
				AccessMode:        windows.SET_ACCESS,
				Inheritance:       windows.SUB_CONTAINERS_AND_OBJECTS_INHERIT | windows.INHERIT_ONLY_ACE,
				Trustee: windows.TRUSTEE{
					TrusteeForm: windows.TRUSTEE_IS_SID, TrusteeType: windows.TRUSTEE_IS_WELL_KNOWN_GROUP,
					TrusteeValue: windows.TrusteeValueFromSID(everyone),
				},
			},
		}
		acl, err := windows.ACLFromEntries(entries, nil)
		if err != nil {
			t.Fatal(err)
		}
		if err := windows.SetNamedSecurityInfo(
			directory,
			windows.SE_FILE_OBJECT,
			windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION,
			nil,
			nil,
			acl,
			nil,
		); err != nil {
			t.Fatal(err)
		}
		if secureWindowsACL(directory) {
			t.Fatal("unauthorized inherit-only ACE was accepted")
		}
	})
}
