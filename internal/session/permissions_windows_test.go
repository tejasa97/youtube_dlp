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
	if !secureWindowsACL(directory, true) {
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
		if secureWindowsACL(directory, true) {
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
		if secureWindowsACL(directory, true) {
			t.Fatal("unauthorized inherit-only ACE was accepted")
		}
	})
}

func TestWindowsOwnerOnlyACLRequiresObjectSpecificInheritance(t *testing.T) {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil || user == nil || user.User.Sid == nil {
		t.Fatalf("current user SID: %v", err)
	}
	sid := user.User.Sid.String()
	for _, test := range []struct {
		name      string
		directory bool
		sddl      string
	}{
		{
			name:      "directory missing inheritance",
			directory: true,
			sddl:      "O:" + sid + "G:" + sid + "D:P(A;;FA;;;" + sid + ")(A;;FA;;;SY)(A;;FA;;;BA)",
		},
		{
			name:      "file has directory inheritance",
			directory: false,
			sddl:      "O:" + sid + "G:" + sid + "D:P(A;OICI;FA;;;" + sid + ")(A;OICI;FA;;;SY)(A;OICI;FA;;;BA)",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			path := filepath.Join(root, "object")
			if test.directory {
				if err := os.Mkdir(path, 0o700); err != nil {
					t.Fatal(err)
				}
			} else {
				file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR|os.O_EXCL, 0o600)
				if err != nil {
					t.Fatal(err)
				}
				if err := file.Close(); err != nil {
					t.Fatal(err)
				}
			}
			descriptor, err := windows.SecurityDescriptorFromString(test.sddl)
			if err != nil {
				t.Fatal(err)
			}
			dacl, _, err := descriptor.DACL()
			if err != nil || dacl == nil {
				t.Fatalf("descriptor DACL: %v", err)
			}
			if err := windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT,
				windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION,
				user.User.Sid, nil, dacl, nil); err != nil {
				t.Fatal(err)
			}
			info, err := os.Lstat(path)
			if err != nil {
				t.Fatal(err)
			}
			if test.directory {
				if ownerOnlyDirectoryAt(path, info) {
					t.Fatal("directory without OI|CI inheritance was accepted")
				}
			} else if ownerOnlyFileAt(path, info) {
				t.Fatal("file with OI|CI inheritance was accepted")
			}
		})
	}
}

func TestWindowsOwnerOnlyACLRejectsInheritedChildUntilExplicitlyHardened(t *testing.T) {
	directory := t.TempDir()
	if err := secureDirectoryPath(directory); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, "inherited-child")
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR|os.O_EXCL, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	if ownerOnlyFileAt(path, info) {
		t.Fatal("inherited child ACL was accepted before explicit hardening")
	}
	if err := secureFilePath(path); err != nil {
		t.Fatal(err)
	}
	if !ownerOnlyFileAt(path, info) {
		t.Fatal("explicitly hardened child ACL was rejected")
	}
}

func TestWindowsWorkspaceCreateOpenAndDiscardUseOwnerOnlyACL(t *testing.T) {
	root := t.TempDir()
	workspace, err := Create(testCreateOptions(root))
	if err != nil {
		t.Fatalf("Create() = %v", err)
	}
	ref := workspace.Ref()
	for _, path := range []string{filepath.Join(workspace.Path(), LeaseFileName), filepath.Join(workspace.Path(), ManifestFileName)} {
		info, statErr := os.Lstat(path)
		if statErr != nil {
			t.Fatal(statErr)
		}
		if !ownerOnlyFileAt(path, info) {
			t.Fatalf("newly-created file %q does not have the protected owner-only ACL", path)
		}
	}
	if err := workspace.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(ref)
	if err != nil {
		t.Fatalf("Open() = %v", err)
	}
	manifestInfo, err := os.Lstat(filepath.Join(reopened.Path(), ManifestFileName))
	if err != nil || !ownerOnlyFileAt(filepath.Join(reopened.Path(), ManifestFileName), manifestInfo) {
		t.Fatalf("reopened manifest ACL is not protected: %v", err)
	}
	if err := reopened.Close(); err != nil {
		t.Fatal(err)
	}
	rootRef, err := ValidateOutputRoot(root)
	if err != nil {
		t.Fatal(err)
	}
	strictRef, err := NewWorkspaceRefWithIdentity(rootRef.CanonicalPath, rootRef.Identity, ref.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	handle, err := PrepareDiscard(strictRef)
	if err != nil {
		t.Fatalf("PrepareDiscard() = %v", err)
	}
	if disposition, err := handle.Discard(); disposition != Discarded || err != nil {
		t.Fatalf("Discard() = %v, %v", disposition, err)
	}
}
