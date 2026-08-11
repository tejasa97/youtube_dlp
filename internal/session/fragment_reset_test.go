package session

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

const fragmentResetCheckpoint = CheckpointDirectoryName + "/fragments/state.json"

func createFragmentResetWorkspace(t *testing.T) *Workspace {
	t.Helper()
	options := testCreateOptions(t.TempDir())
	options.Components = []Component{{
		ID: "primary", Kind: "fragmented-hls", ObservedBytes: 11, CommittedBytes: 11,
		Checkpoint: CheckpointMetadata{RelativePath: fragmentResetCheckpoint, Digest: strings64("a"), PlanHash: strings64("b"), Sequence: 1, Total: 11},
	}}
	workspace, err := Create(options)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = workspace.Close() })
	return workspace
}

func strings64(value string) string {
	result := make([]byte, 64)
	for index := range result {
		result[index] = value[0]
	}
	return string(result)
}

func writeFragmentResetEvidence(t *testing.T, workspace *Workspace) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(workspace.Path(), "payload"), []byte("payload"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace.Path(), "payload.part"), []byte("payload-part"), 0o600); err != nil {
		t.Fatal(err)
	}
	directory := filepath.Join(workspace.Path(), filepath.FromSlash(CheckpointDirectoryName), "fragments")
	if err := os.MkdirAll(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"state.json", "00000000.frag"} {
		if err := os.WriteFile(filepath.Join(directory, name), []byte(name), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func fragmentResetState(t *testing.T, workspace *Workspace) Manifest {
	t.Helper()
	manifest, err := workspace.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	return manifest
}

func assertFragmentAuthorityZero(t *testing.T, workspace *Workspace) Manifest {
	t.Helper()
	manifest := fragmentResetState(t, workspace)
	if len(manifest.Components) != 1 || manifest.Components[0].ObservedBytes != 0 || manifest.Components[0].CommittedBytes != 0 ||
		manifest.Components[0].Checkpoint.RelativePath != fragmentResetCheckpoint || manifest.Components[0].Checkpoint.Digest != "" ||
		manifest.Components[0].Checkpoint.PlanHash != "" || manifest.Components[0].Checkpoint.Sequence != 0 || manifest.Components[0].Checkpoint.Total != 0 {
		t.Fatalf("fragment authority was not durably revoked: %#v", manifest.Components)
	}
	return manifest
}

func TestResetFragmentComponentFixedPathAndStaleRevision(t *testing.T) {
	t.Run("stale revision preserves evidence", func(t *testing.T) {
		workspace := createFragmentResetWorkspace(t)
		writeFragmentResetEvidence(t, workspace)
		manifest := fragmentResetState(t, workspace)
		err := workspace.ResetFragmentComponent(manifest.Revision+1, manifest.RunGeneration, "primary", time.Now())
		if !errors.Is(err, ErrStaleMutation) {
			t.Fatalf("error=%v, want stale mutation", err)
		}
		if _, err := os.Stat(filepath.Join(workspace.Path(), "payload")); err != nil {
			t.Fatalf("stale reset removed payload: %v", err)
		}
	})

	t.Run("fixed checkpoint path required", func(t *testing.T) {
		options := testCreateOptions(t.TempDir())
		options.Components = []Component{{ID: "primary", Kind: "fragmented-hls", ObservedBytes: 1, CommittedBytes: 1, Checkpoint: CheckpointMetadata{RelativePath: CheckpointDirectoryName + "/other/state.json"}}}
		workspace, err := Create(options)
		if err != nil {
			t.Fatal(err)
		}
		defer workspace.Close()
		manifest := fragmentResetState(t, workspace)
		if err := workspace.ResetFragmentComponent(manifest.Revision, manifest.RunGeneration, "primary", time.Now()); !errors.Is(err, ErrUnsafePath) {
			t.Fatalf("error=%v, want unsafe path", err)
		}
	})
}

func TestResetFragmentComponentRemovesOwnedEvidenceAndRevokesAuthority(t *testing.T) {
	workspace := createFragmentResetWorkspace(t)
	writeFragmentResetEvidence(t, workspace)
	manifest := fragmentResetState(t, workspace)
	if err := workspace.ResetFragmentComponent(manifest.Revision, manifest.RunGeneration, "primary", time.Now()); err != nil {
		t.Fatal(err)
	}
	assertFragmentAuthorityZero(t, workspace)
	for _, path := range []string{
		filepath.Join(workspace.Path(), "payload"), filepath.Join(workspace.Path(), "payload.part"),
		filepath.Join(workspace.Path(), filepath.FromSlash(CheckpointDirectoryName), "fragments"),
	} {
		if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("retained reset evidence %q: %v", path, err)
		}
	}
}

func TestResetFragmentComponentDeletionFailureIsFailClosedAndRetryable(t *testing.T) {
	workspace := createFragmentResetWorkspace(t)
	writeFragmentResetEvidence(t, workspace)
	previous := discardFault
	fired := false
	discardFault = func(point discardFaultPoint) error {
		if point == discardFaultChildRemoval && !fired {
			fired = true
			return errors.New("injected reset deletion failure")
		}
		return nil
	}
	t.Cleanup(func() { discardFault = previous })
	manifest := fragmentResetState(t, workspace)
	if err := workspace.ResetFragmentComponent(manifest.Revision, manifest.RunGeneration, "primary", time.Now()); err == nil {
		t.Fatal("reset succeeded despite injected deletion failure")
	}
	if !fired {
		t.Fatal("deletion fault did not fire")
	}
	updated := assertFragmentAuthorityZero(t, workspace)
	if _, err := os.Stat(filepath.Join(workspace.Path(), "payload")); err != nil {
		t.Fatalf("failed deletion unexpectedly removed payload: %v", err)
	}
	if err := workspace.ResetFragmentComponent(updated.Revision, updated.RunGeneration, "primary", time.Now()); err != nil {
		t.Fatalf("retry reset: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(workspace.Path(), "payload")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("retry retained payload: %v", err)
	}
}

func TestResetFragmentComponentWorkspaceReplacementBeforeOpenFailsClosed(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("directory symlink setup can require elevated Windows privileges")
	}
	for _, replacement := range []string{"directory", "symlink"} {
		t.Run(replacement, func(t *testing.T) {
			workspace := createFragmentResetWorkspace(t)
			writeFragmentResetEvidence(t, workspace)
			outside := t.TempDir()
			sentinel := filepath.Join(outside, "sentinel")
			if err := os.WriteFile(sentinel, []byte("keep"), 0o600); err != nil {
				t.Fatal(err)
			}
			previous := atomicManifestWrite
			swapped := false
			atomicManifestWrite = func(path string, mode os.FileMode, encode func(io.Writer) error) error {
				err := previous(path, mode, encode)
				if err != nil || swapped || filepath.Base(path) != ManifestFileName {
					return err
				}
				swapped = true
				backup := workspace.Path() + ".original"
				if renameErr := os.Rename(workspace.Path(), backup); renameErr != nil {
					return renameErr
				}
				if replacement == "directory" {
					return os.Mkdir(workspace.Path(), 0o700)
				}
				return os.Symlink(outside, workspace.Path())
			}
			t.Cleanup(func() { atomicManifestWrite = previous })
			manifest := fragmentResetState(t, workspace)
			err := workspace.ResetFragmentComponent(manifest.Revision, manifest.RunGeneration, "primary", time.Now())
			if err == nil || !swapped {
				t.Fatalf("replacement reset error=%v swapped=%t, want fail-closed", err, swapped)
			}
			if _, err := os.Stat(sentinel); err != nil {
				t.Fatalf("replacement traversal touched outside sentinel: %v", err)
			}
			assertFragmentAuthorityZero(t, workspace)
		})
	}
}

func TestResetFragmentComponentTraversalSwapAndCrashOrderRemainFailClosed(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("directory symlink setup can require elevated Windows privileges")
	}
	workspace := createFragmentResetWorkspace(t)
	writeFragmentResetEvidence(t, workspace)
	outside := t.TempDir()
	sentinel := filepath.Join(outside, "sentinel")
	if err := os.WriteFile(sentinel, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	previous := discardBeforeChildOpen
	swapped := false
	discardBeforeChildOpen = func(path string) {
		if filepath.Base(path) != "state.json" || swapped {
			return
		}
		swapped = true
		fragments := filepath.Dir(path)
		if err := os.Rename(fragments, fragments+".original"); err != nil {
			t.Fatalf("swap original fragments: %v", err)
		}
		if err := os.Symlink(outside, fragments); err != nil {
			t.Fatalf("swap fragments to symlink: %v", err)
		}
	}
	t.Cleanup(func() { discardBeforeChildOpen = previous })
	manifest := fragmentResetState(t, workspace)
	err := workspace.ResetFragmentComponent(manifest.Revision, manifest.RunGeneration, "primary", time.Now())
	if err == nil || !swapped {
		t.Fatalf("traversal swap error=%v swapped=%t, want fail-closed", err, swapped)
	}
	updated := assertFragmentAuthorityZero(t, workspace)
	if _, err := os.Stat(sentinel); err != nil {
		t.Fatalf("traversal swap touched outside sentinel: %v", err)
	}
	// This is the crash-order equivalent: authority was already committed to
	// zero while an old ledger remains. Retrying against the new revision must
	// never authorize those bytes; after restoring the path it cleans them.
	fragments := filepath.Join(workspace.Path(), filepath.FromSlash(CheckpointDirectoryName), "fragments")
	if err := os.Remove(fragments); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(fragments+".original", fragments); err != nil {
		t.Fatal(err)
	}
	if err := workspace.ResetFragmentComponent(updated.Revision, updated.RunGeneration, "primary", time.Now()); err != nil {
		t.Fatalf("recover stale authority/ledger mismatch: %v", err)
	}
	if _, err := os.Lstat(fragments); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("recovery retained stale fragment ledger: %v", err)
	}
}
