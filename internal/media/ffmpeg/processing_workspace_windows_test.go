//go:build windows

package ffmpeg

import (
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/windows"
)

func TestWindowsProcessingWorkspaceUsesProtectedOwnerACL(t *testing.T) {
	root := t.TempDir()
	directory := filepath.Join(root, "private", "component")
	if err := ensureProcessingWorkspaceDirectory(root, directory); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{filepath.Join(root, "private"), directory} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		protected, err := processingDirectoryProtected(path, info)
		if err != nil || !protected {
			t.Fatalf("processing directory %s is not protected: protected=%v error=%v", path, protected, err)
		}
	}
}

func TestWindowsProcessingWorkspaceRejectsUnprotectedDACL(t *testing.T) {
	path := filepath.Join(t.TempDir(), "unprotected")
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	setWindowsProcessingTestDACL(t, path, "D:(A;;FA;;;"+user.User.Sid.String()+")", false)
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if protected, err := processingDirectoryProtected(path, info); err != nil || protected {
		t.Fatalf("unprotected DACL accepted: protected=%v error=%v", protected, err)
	}
}

func TestWindowsProcessingWorkspaceRejectsUnauthorizedInheritOnlyACE(t *testing.T) {
	path := filepath.Join(t.TempDir(), "unauthorized")
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	sid := user.User.Sid.String()
	setWindowsProcessingTestDACL(t, path, "D:P(A;;FA;;;"+sid+")(A;OICIIO;GA;;;WD)", true)
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if protected, err := processingDirectoryProtected(path, info); err != nil || protected {
		t.Fatalf("unauthorized inherit-only ACE accepted: protected=%v error=%v", protected, err)
	}
}

func setWindowsProcessingTestDACL(t *testing.T, path, sddl string, protected bool) {
	t.Helper()
	descriptor, err := windows.SecurityDescriptorFromString(sddl)
	if err != nil {
		t.Fatal(err)
	}
	dacl, _, err := descriptor.DACL()
	if err != nil {
		t.Fatal(err)
	}
	information := windows.SECURITY_INFORMATION(windows.DACL_SECURITY_INFORMATION | windows.UNPROTECTED_DACL_SECURITY_INFORMATION)
	if protected {
		information = windows.SECURITY_INFORMATION(windows.DACL_SECURITY_INFORMATION | windows.PROTECTED_DACL_SECURITY_INFORMATION)
	}
	if err := windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, information, nil, nil, dacl, nil); err != nil {
		t.Fatal(err)
	}
}
