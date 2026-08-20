//go:build windows

package fragment

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/tejasa97/ytdlp-go/internal/network"
	"golang.org/x/sys/windows"
)

func TestWindowsCheckpointDirectoryUsesProtectedOwnerACL(t *testing.T) {
	root := t.TempDir()
	directory := filepath.Join(root, "private", "component")
	if _, err := ensureProtectedCheckpointDirectory(root, directory); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{filepath.Join(root, "private"), directory} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		protected, err := checkpointDirectoryProtected(path, info)
		if err != nil {
			t.Fatal(err)
		}
		if !protected {
			t.Fatalf("checkpoint directory %s does not have a protected owner ACL", path)
		}
	}
}

func TestWindowsCheckpointDirectoryRejectsUnprotectedDACL(t *testing.T) {
	path := filepath.Join(t.TempDir(), "unprotected")
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	setWindowsCheckpointTestDACL(t, path, "D:(A;;FA;;;"+user.User.Sid.String()+")", false)
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if protected, err := checkpointDirectoryProtected(path, info); err != nil || protected {
		t.Fatalf("unprotected DACL accepted: protected=%v error=%v", protected, err)
	}
}

func TestWindowsCheckpointDirectoryRejectsUnauthorizedInheritOnlyACE(t *testing.T) {
	path := filepath.Join(t.TempDir(), "unauthorized")
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	sid := user.User.Sid.String()
	setWindowsCheckpointTestDACL(t, path, "D:P(A;;FA;;;"+sid+")(A;OICIIO;GA;;;WD)", true)
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if protected, err := checkpointDirectoryProtected(path, info); err != nil || protected {
		t.Fatalf("unauthorized inherit-only ACE accepted: protected=%v error=%v", protected, err)
	}
}

func TestWindowsDurableCheckpointFilesHaveExplicitACL(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = writer.Write([]byte("fragment"))
	}))
	defer server.Close()
	transport, err := network.New(network.Config{})
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	destination := filepath.Join(root, "output.bin")
	job := checkpointTestJob(root, destination, "windows-checkpoint-acl", []Segment{{URL: server.URL}})
	job.Checkpoint.OnCommit = func(_ context.Context, snapshot CommitSnapshot) error {
		if snapshot.Sequence != 1 {
			return fmt.Errorf("checkpoint sequence = %d, want 1", snapshot.Sequence)
		}
		for _, path := range []string{
			filepath.Join(job.Checkpoint.Directory, "state.json"),
			fragmentPath(job.Checkpoint.Directory, 0),
		} {
			info, statErr := os.Lstat(path)
			if statErr != nil {
				return fmt.Errorf("stat checkpoint file %s: %w", path, statErr)
			}
			if !checkpointFileProtected(path, info) {
				return fmt.Errorf("checkpoint file %s does not have the explicit protected file ACL", path)
			}
		}
		return nil
	}
	if _, err := New(transport).Download(context.Background(), job, nil); err != nil {
		t.Fatal(err)
	}
}

func setWindowsCheckpointTestDACL(t *testing.T, path, sddl string, protected bool) {
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
