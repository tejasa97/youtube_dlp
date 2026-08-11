//go:build !windows

package ffmpeg

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/tejasa97/youtube_dlp/internal/atomicfile"
	"github.com/tejasa97/youtube_dlp/internal/events"
)

func TestUnixProcessingWorkspaceRejectsOutputRootReplacementBeforeFFmpeg(t *testing.T) {
	root := t.TempDir()
	outputRoot := filepath.Join(root, "output-root")
	if err := os.Mkdir(outputRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(outputRoot, "missing-parent", "output.mka")
	workspace := testProcessingWorkspace(outputRoot, destination)
	workspace.Directory = filepath.Join(outputRoot, "processing")
	tools := &Toolset{ffmpeg: filepath.Join(root, "must-not-run")}
	ops := productionProcessingWorkspaceOps
	productionWrite := ops.writeAtomic
	replaced := false
	ops.writeAtomic = func(path string, mode os.FileMode, encode func(io.Writer) error) error {
		var buffer bytes.Buffer
		if err := encode(&buffer); err != nil {
			return err
		}
		var state processingState
		if json.Unmarshal(buffer.Bytes(), &state) == nil && state.Phase == processingPhaseRunning && !replaced {
			if err := productionWrite(path, mode, func(writer io.Writer) error { _, err := writer.Write(buffer.Bytes()); return err }); err != nil {
				return err
			}
			realRoot := outputRoot + ".real"
			if err := os.Rename(outputRoot, realRoot); err != nil {
				return err
			}
			if err := os.Symlink(realRoot, outputRoot); err != nil {
				return err
			}
			replaced = true
			return nil
		}
		return productionWrite(path, mode, func(writer io.Writer) error { _, err := writer.Write(buffer.Bytes()); return err })
	}
	err := tools.runAtomicWorkspaceWithOps(context.Background(), destination, false, nil, workspace, nil, processingOperation(filepath.Join(root, "input"), "complete"), ops)
	if !errors.Is(err, ErrProcessingReconciliation) {
		t.Fatalf("root replacement error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(outputRoot, "missing-parent")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("destination parent was created after root replacement: %v", err)
	}
	if _, err := os.Stat(filepath.Join(outputRoot, "processing", "output.mka")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("FFmpeg stage was touched after root replacement: %v", err)
	}
	if replaced {
		_ = os.Remove(outputRoot)
		_ = os.Rename(outputRoot+".real", outputRoot)
	}
}

func TestUnixProcessingWorkspaceRejectsRootReplacementBeforeInitialMutation(t *testing.T) {
	root := t.TempDir()
	outputRoot := filepath.Join(root, "output-root")
	replacementRoot := filepath.Join(root, "replacement-root")
	if err := os.Mkdir(outputRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(replacementRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(outputRoot, "new-parent", "output.mka")
	workspace := testProcessingWorkspace(outputRoot, destination)
	workspace.Directory = filepath.Join(outputRoot, "processing")
	ops := productionProcessingWorkspaceOps
	ops.beforeInitialWorkspaceMutation = func(string) error {
		if err := os.Rename(outputRoot, outputRoot+".original"); err != nil {
			return err
		}
		return os.Rename(replacementRoot, outputRoot)
	}
	err := (&Toolset{ffmpeg: filepath.Join(root, "must-not-run")}).runAtomicWorkspaceWithOps(
		context.Background(), destination, false, nil, workspace, nil, nil, ops,
	)
	if !errors.Is(err, ErrProcessingReconciliation) {
		t.Fatalf("root replacement before ensure error = %v", err)
	}
	for _, path := range []string{
		filepath.Join(outputRoot, "processing"),
		processingGuardPath(workspace.Directory),
		filepath.Dir(destination),
	} {
		if _, statErr := os.Stat(path); !errors.Is(statErr, os.ErrNotExist) {
			t.Fatalf("replacement root was mutated at %s: %v", path, statErr)
		}
	}
}

func TestUnixProcessingWorkspaceRejectsGuardSwapBeforeCleanupEvidenceRead(t *testing.T) {
	root := t.TempDir()
	outputRoot := filepath.Join(root, "output-root")
	if err := os.Mkdir(outputRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(outputRoot, "output.mka")
	workspace := testProcessingWorkspace(outputRoot, destination)
	if err := ensureProcessingWorkspaceDirectory(outputRoot, workspace.Directory); err != nil {
		t.Fatal(err)
	}
	if err := ensureProcessingGuardDirectory(outputRoot, workspace.Directory); err != nil {
		t.Fatal(err)
	}
	cleanupPath := processingCleanupMarkerPath(workspace.Directory)
	if err := writeProcessingCleanupMarker(cleanupPath, workspace, atomicfile.Write); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(cleanupPath)
	if err != nil {
		t.Fatal(err)
	}
	outsideGuard := filepath.Join(root, "outside-guard")
	ops := productionProcessingWorkspaceOps
	ops.beforeProcessingEvidenceRead = func(path string) error {
		if err := os.Rename(path, outsideGuard); err != nil {
			return err
		}
		return ensureProcessingGuardDirectory(outputRoot, workspace.Directory)
	}
	err = (&Toolset{ffmpeg: filepath.Join(root, "must-not-run")}).runAtomicWorkspaceWithOps(
		context.Background(), destination, false, nil, workspace, nil, nil, ops,
	)
	if !errors.Is(err, ErrProcessingReconciliation) {
		t.Fatalf("guard replacement error = %v", err)
	}
	after, err := os.ReadFile(filepath.Join(outsideGuard, processingCleanupName))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatalf("outside cleanup evidence changed: before=%q after=%q", before, after)
	}
}

func TestUnixProcessingWorkspaceRejectsLeasePathSwapAfterAcquisition(t *testing.T) {
	root := t.TempDir()
	outputRoot := filepath.Join(root, "output-root")
	if err := os.Mkdir(outputRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(outputRoot, "output.mka")
	workspace := testProcessingWorkspace(outputRoot, destination)
	if err := ensureProcessingWorkspaceDirectory(outputRoot, workspace.Directory); err != nil {
		t.Fatal(err)
	}
	if err := ensureProcessingGuardDirectory(outputRoot, workspace.Directory); err != nil {
		t.Fatal(err)
	}
	outsideLease := filepath.Join(root, "outside-lease")
	ops := productionProcessingWorkspaceOps
	ops.afterProcessingLeaseAcquisition = func(path string) error {
		if err := os.Rename(path, outsideLease); err != nil {
			return err
		}
		return os.WriteFile(path, []byte("replacement-lease"), 0o600)
	}
	err := (&Toolset{ffmpeg: filepath.Join(root, "must-not-run")}).runAtomicWorkspaceWithOps(
		context.Background(), destination, false, nil, workspace, nil, nil, ops,
	)
	if !errors.Is(err, ErrProcessingReconciliation) {
		t.Fatalf("lease replacement error = %v", err)
	}
	outside, err := os.ReadFile(outsideLease)
	if err != nil {
		t.Fatal(err)
	}
	if len(outside) != 0 {
		t.Fatalf("locked outside lease evidence changed: %q", outside)
	}
}

func TestUnixProcessingWorkspaceRejectsRootReplacementAfterDestinationParentCreation(t *testing.T) {
	root := t.TempDir()
	outputRoot := filepath.Join(root, "output-root")
	replacementRoot := filepath.Join(root, "replacement-root")
	if err := os.Mkdir(outputRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(replacementRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	input := filepath.Join(root, "input")
	if err := os.WriteFile(input, []byte("candidate"), 0o600); err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(outputRoot, "nested", "output.mka")
	workspace := testProcessingWorkspace(outputRoot, destination)
	tools := &Toolset{ffmpeg: writeProcessingHelper(t, root), maxOutput: 1 << 20}
	ops := productionProcessingWorkspaceOps
	ops.afterDestinationParentCreation = func(string) error {
		if err := os.Rename(outputRoot, outputRoot+".original"); err != nil {
			return err
		}
		return os.Rename(replacementRoot, outputRoot)
	}
	err := tools.runAtomicWorkspaceWithOps(
		context.Background(), destination, false, nil, workspace, nil, processingOperation(input, "complete"), ops,
	)
	if !errors.Is(err, ErrProcessingReconciliation) {
		t.Fatalf("root replacement after destination parent creation error = %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(outputRoot, "nested")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("replacement root destination parent was created: %v", statErr)
	}
	if _, statErr := os.Stat(destination); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("destination was published after root replacement: %v", statErr)
	}
}

func TestUnixProcessingWorkspaceCleanupRejectsRootSwapBeforeStateRemoval(t *testing.T) {
	root := t.TempDir()
	outputRoot := filepath.Join(root, "output-root")
	replacementRoot := filepath.Join(root, "replacement-root")
	if err := os.Mkdir(outputRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(replacementRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	input := filepath.Join(root, "input")
	if err := os.WriteFile(input, []byte("candidate"), 0o600); err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(outputRoot, "output.mka")
	workspace := testProcessingWorkspace(outputRoot, destination)
	statePath := filepath.Join(workspace.Directory, "state.json")
	tools := &Toolset{ffmpeg: writeProcessingHelper(t, root), maxOutput: 1 << 20}
	ops := productionProcessingWorkspaceOps
	ops.beforeCleanupMutation = func(path string) error {
		if path != statePath {
			return nil
		}
		if err := os.Rename(outputRoot, outputRoot+".original"); err != nil {
			return err
		}
		return os.Rename(replacementRoot, outputRoot)
	}
	err := tools.runAtomicWorkspaceWithOps(
		context.Background(), destination, false, nil, workspace, nil, processingOperation(input, "complete"), ops,
	)
	var authority atomicfile.CommitError
	if !errors.Is(err, ErrProcessingReconciliation) || !errors.As(err, &authority) || !authority.Committed() {
		t.Fatalf("root cleanup swap authority = %#v, %v", authority, err)
	}
	originalWorkspace := filepath.Join(outputRoot+".original", filepath.Base(workspace.Directory))
	if _, statErr := os.Stat(filepath.Join(originalWorkspace, "state.json")); statErr != nil {
		t.Fatalf("original workspace evidence was deleted: %v", statErr)
	}
	if _, statErr := os.Stat(filepath.Join(outputRoot, filepath.Base(workspace.Directory))); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("replacement root was mutated: %v", statErr)
	}
}

func TestUnixProcessingWorkspaceCleanupRejectsWorkspaceSwapBeforeRemoval(t *testing.T) {
	root := t.TempDir()
	outputRoot := filepath.Join(root, "output-root")
	if err := os.Mkdir(outputRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	input := filepath.Join(root, "input")
	if err := os.WriteFile(input, []byte("candidate"), 0o600); err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(outputRoot, "output.mka")
	workspace := testProcessingWorkspace(outputRoot, destination)
	replacementWorkspace := filepath.Join(root, "replacement-workspace")
	tools := &Toolset{ffmpeg: writeProcessingHelper(t, root), maxOutput: 1 << 20}
	ops := productionProcessingWorkspaceOps
	ops.beforeCleanupMutation = func(path string) error {
		if path != workspace.Directory {
			return nil
		}
		if err := os.Rename(workspace.Directory, replacementWorkspace); err != nil {
			return err
		}
		if err := os.Mkdir(workspace.Directory, 0o700); err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(workspace.Directory, "replacement-evidence"), []byte("retain"), 0o600)
	}
	err := tools.runAtomicWorkspaceWithOps(
		context.Background(), destination, false, nil, workspace, nil, processingOperation(input, "complete"), ops,
	)
	var authority atomicfile.CommitError
	if !errors.Is(err, ErrProcessingReconciliation) || !errors.As(err, &authority) || !authority.Committed() {
		t.Fatalf("workspace cleanup swap authority = %#v, %v", authority, err)
	}
	if contents, readErr := os.ReadFile(filepath.Join(workspace.Directory, "replacement-evidence")); readErr != nil || string(contents) != "retain" {
		t.Fatalf("replacement workspace evidence changed: %q, %v", contents, readErr)
	}
	if _, statErr := os.Stat(replacementWorkspace); statErr != nil {
		t.Fatalf("original workspace evidence was deleted: %v", statErr)
	}
}

func TestUnixProcessingWorkspaceCleanupRejectsGuardSwapBeforeMarkerRemoval(t *testing.T) {
	root := t.TempDir()
	outputRoot := filepath.Join(root, "output-root")
	if err := os.Mkdir(outputRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	input := filepath.Join(root, "input")
	if err := os.WriteFile(input, []byte("candidate"), 0o600); err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(outputRoot, "output.mka")
	workspace := testProcessingWorkspace(outputRoot, destination)
	cleanupPath := processingCleanupMarkerPath(workspace.Directory)
	guardPath := processingGuardPath(workspace.Directory)
	outsideGuard := filepath.Join(root, "outside-cleanup-guard")
	tools := &Toolset{ffmpeg: writeProcessingHelper(t, root), maxOutput: 1 << 20}
	ops := productionProcessingWorkspaceOps
	ops.beforeCleanupMutation = func(path string) error {
		if path != cleanupPath {
			return nil
		}
		if err := os.Rename(guardPath, outsideGuard); err != nil {
			return err
		}
		return ensureProcessingGuardDirectory(outputRoot, workspace.Directory)
	}
	err := tools.runAtomicWorkspaceWithOps(
		context.Background(), destination, false, nil, workspace, nil, processingOperation(input, "complete"), ops,
	)
	var authority atomicfile.CommitError
	if !errors.Is(err, ErrProcessingReconciliation) || !errors.As(err, &authority) || !authority.Committed() {
		t.Fatalf("guard cleanup swap authority = %#v, %v", authority, err)
	}
	if _, statErr := os.Stat(filepath.Join(outsideGuard, processingCleanupName)); statErr != nil {
		t.Fatalf("outside cleanup marker was deleted: %v", statErr)
	}
	if _, statErr := os.Stat(filepath.Join(outsideGuard, processingLeaseName)); statErr != nil {
		t.Fatalf("outside lease evidence was deleted: %v", statErr)
	}
}

func TestUnixProcessingWorkspaceCleanupKeepsLeaseLockedUntilUnlink(t *testing.T) {
	root := t.TempDir()
	input := filepath.Join(root, "input")
	destination := filepath.Join(root, "output.mka")
	if err := os.WriteFile(input, []byte("candidate"), 0o600); err != nil {
		t.Fatal(err)
	}
	workspace := testProcessingWorkspace(root, destination)
	leasePath := processingLeasePath(workspace.Directory)
	tools := &Toolset{ffmpeg: writeProcessingHelper(t, root), maxOutput: 1 << 20}
	ops := productionProcessingWorkspaceOps
	ops.beforeCleanupMutation = func(path string) error {
		if path != leasePath {
			return nil
		}
		other, err := acquireProcessingLease(leasePath)
		if err == nil {
			_ = other.release()
			return errors.New("processing lease was reacquired before unlink")
		}
		return nil
	}
	if err := tools.runAtomicWorkspaceWithOps(
		context.Background(), destination, false, nil, workspace, nil, processingOperation(input, "complete"), ops,
	); err != nil {
		t.Fatalf("cleanup with held lease = %v", err)
	}
	for _, path := range []string{workspace.Directory, processingGuardPath(workspace.Directory), leasePath} {
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("cleanup authority remains at %s: %v", path, err)
		}
	}
}

func TestUnixProcessingWorkspaceGuardLockBlocksReacquisitionAfterLeaseUnlink(t *testing.T) {
	root := t.TempDir()
	input := filepath.Join(root, "input")
	destination := filepath.Join(root, "output.mka")
	if err := os.WriteFile(input, []byte("candidate"), 0o600); err != nil {
		t.Fatal(err)
	}
	workspace := testProcessingWorkspace(root, destination)
	leasePath := processingLeasePath(workspace.Directory)
	guardPath := processingGuardPath(workspace.Directory)
	tools := &Toolset{ffmpeg: writeProcessingHelper(t, root), maxOutput: 1 << 20}
	ops := productionProcessingWorkspaceOps
	ops.beforeCleanupMutation = func(path string) error {
		if path != guardPath {
			return nil
		}
		if _, err := os.Stat(leasePath); !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("lease path was not unlinked before guard removal: %v", err)
		}
		other, err := acquireProcessingLease(leasePath)
		if err == nil {
			_ = other.release()
			return errors.New("processing lease was reacquired after unlink and before guard removal")
		}
		return nil
	}
	if err := tools.runAtomicWorkspaceWithOps(
		context.Background(), destination, false, nil, workspace, nil, processingOperation(input, "complete"), ops,
	); err != nil {
		t.Fatalf("cleanup with guard lock = %v", err)
	}
}

func TestUnixProcessingWorkspaceFinalCleanupRecoveryRejectsEvidenceSwap(t *testing.T) {
	for _, test := range []struct {
		name string
		swap func(ProcessingWorkspace, string, string) (string, error)
	}{
		{
			name: "guard lock",
			swap: func(workspace ProcessingWorkspace, lockPath, _ string) (string, error) {
				outside := lockPath + ".outside"
				return outside, os.Rename(lockPath, outside)
			},
		},
		{
			name: "final marker",
			swap: func(_ ProcessingWorkspace, _, finalPath string) (string, error) {
				outside := finalPath + ".outside"
				return outside, os.Rename(finalPath, outside)
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			input := filepath.Join(root, "input")
			destination := filepath.Join(root, "output.mka")
			if err := os.WriteFile(input, []byte("candidate"), 0o600); err != nil {
				t.Fatal(err)
			}
			workspace := testProcessingWorkspace(root, destination)
			tools := &Toolset{ffmpeg: writeProcessingHelper(t, root), maxOutput: 1 << 20}
			ops := productionProcessingWorkspaceOps
			productionRemove := ops.remove
			injected := errors.New("retain final cleanup evidence")
			ops.remove = func(path string) error {
				if path == processingFinalCleanupMarkerPath(workspace.Directory) {
					return injected
				}
				return productionRemove(path)
			}
			if err := tools.runAtomicWorkspaceWithOps(context.Background(), destination, false, nil, workspace, nil, processingOperation(input, "complete"), ops); err == nil {
				t.Fatal("initial cleanup unexpectedly completed")
			}
			lockPath := processingGuardLockPath(workspace.Directory)
			finalPath := processingFinalCleanupMarkerPath(workspace.Directory)
			var outside string
			recoveryOps := productionProcessingWorkspaceOps
			recoveryOps.beforeFinalCleanupRecovery = func(string) error {
				var err error
				outside, err = test.swap(workspace, lockPath, finalPath)
				return err
			}
			err := (&Toolset{ffmpeg: filepath.Join(root, "must-not-run")}).runAtomicWorkspaceWithOps(
				context.Background(), destination, true, nil, workspace, nil, nil, recoveryOps,
			)
			if !errors.Is(err, ErrProcessingCommitted) {
				t.Fatalf("recovery swap error = %v", err)
			}
			if outside == "" {
				t.Fatal("recovery swap hook did not run")
			}
			if _, statErr := os.Stat(outside); statErr != nil {
				t.Fatalf("replacement evidence was deleted: %v", statErr)
			}
		})
	}
}

func TestUnixProcessingWorkspaceFinalCleanupMarkerRecoveryAfterGuardRemoval(t *testing.T) {
	root := t.TempDir()
	input := filepath.Join(root, "input")
	destination := filepath.Join(root, "output.mka")
	if err := os.WriteFile(input, []byte("candidate"), 0o600); err != nil {
		t.Fatal(err)
	}
	workspace := testProcessingWorkspace(root, destination)
	tools := &Toolset{ffmpeg: writeProcessingHelper(t, root), maxOutput: 1 << 20}
	ops := productionProcessingWorkspaceOps
	productionRemove := ops.remove
	injected := errors.New("final cleanup marker removal")
	ops.remove = func(path string) error {
		if path == processingFinalCleanupMarkerPath(workspace.Directory) {
			return injected
		}
		return productionRemove(path)
	}
	err := tools.runAtomicWorkspaceWithOps(context.Background(), destination, false, nil, workspace, nil, processingOperation(input, "complete"), ops)
	var authority atomicfile.CommitError
	if !errors.Is(err, injected) || !errors.As(err, &authority) || !authority.Committed() {
		t.Fatalf("final cleanup marker authority = %#v, %v", authority, err)
	}
	if _, err := os.Stat(processingFinalCleanupMarkerPath(workspace.Directory)); err != nil {
		t.Fatalf("final cleanup marker was lost: %v", err)
	}
	for _, path := range []string{workspace.Directory, processingGuardPath(workspace.Directory), processingLeasePath(workspace.Directory)} {
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("authority path remains at %s: %v", path, err)
		}
	}
	if _, err := os.Stat(processingGuardLockPath(workspace.Directory)); err != nil {
		t.Fatalf("guard-lock recovery authority was lost: %v", err)
	}
	second := &Toolset{ffmpeg: filepath.Join(root, "must-not-run")}
	err = second.runAtomicWorkspace(context.Background(), destination, true, nil, workspace, nil, func(string) []string {
		t.Fatal("final-cleanup retry restarted FFmpeg")
		return nil
	})
	if !errors.Is(err, ErrProcessingCommitted) {
		t.Fatalf("final cleanup evidence was not retained as committed authority: %v", err)
	}
}

func TestUnixProcessingWorkspaceCancellationAfterDestinationPreparationIsRetryable(t *testing.T) {
	root := t.TempDir()
	input := filepath.Join(root, "input")
	outputRoot := filepath.Join(root, "output-root")
	destination := filepath.Join(outputRoot, "publish", "output.mka")
	if err := os.Mkdir(outputRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(input, []byte("candidate"), 0o600); err != nil {
		t.Fatal(err)
	}
	workspace := testProcessingWorkspace(outputRoot, destination)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ops := productionProcessingWorkspaceOps
	ops.afterDestinationParentCreation = func(string) error {
		cancel()
		return nil
	}
	ops.publishNoClobber = func(string, string) error {
		t.Fatal("publication ran after cancellation")
		return nil
	}
	tools := &Toolset{ffmpeg: writeProcessingHelper(t, root), maxOutput: 1 << 20}
	err := tools.runAtomicWorkspaceWithOps(ctx, destination, false, nil, workspace, nil, processingOperation(input, "complete"), ops)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation error = %v", err)
	}
	if _, err := os.Stat(destination); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("destination was published after cancellation: %v", err)
	}
	state := readProcessingStateForTest(t, workspace.Directory)
	if state.Phase != processingPhaseOutputComplete {
		t.Fatalf("state phase after cancellation = %q, want %q", state.Phase, processingPhaseOutputComplete)
	}
	if err := (&Toolset{ffmpeg: filepath.Join(root, "must-not-run")}).runAtomicWorkspace(context.Background(), destination, false, nil, workspace, nil, func(string) []string {
		t.Fatal("retry restarted processing instead of reusing verified output")
		return nil
	}); err != nil {
		t.Fatalf("retry failed to reuse verified output: %v", err)
	}
	if _, err := os.Stat(destination); err != nil {
		t.Fatalf("retry did not publish verified output: %v", err)
	}
}

func TestUnixProcessingWorkspaceRejectsSymlinkedSiblingParentBeforeGuardAccess(t *testing.T) {
	root := t.TempDir()
	outputRoot := filepath.Join(root, "output-root")
	outside := filepath.Join(root, "outside")
	if err := os.MkdirAll(outputRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(outside, 0o755); err != nil {
		t.Fatal(err)
	}
	parent := filepath.Join(outputRoot, "parent")
	if err := os.Symlink(outside, parent); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	destination := filepath.Join(outputRoot, "output.mka")
	workspace := testProcessingWorkspace(outputRoot, destination)
	workspace.Directory = filepath.Join(parent, "processing")
	err := (&Toolset{ffmpeg: filepath.Join(root, "must-not-run")}).runAtomicWorkspace(context.Background(), destination, false, nil, workspace, nil, nil)
	if !errors.Is(err, ErrInvalidProcessingWorkspace) {
		t.Fatalf("symlinked parent error = %v", err)
	}
	if _, err := os.Stat(processingGuardPath(workspace.Directory)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("guard was accessed or created through symlinked parent: %v", err)
	}
	if _, err := os.Stat(processingLeasePath(workspace.Directory)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("lease was accessed or created through symlinked parent: %v", err)
	}
}

func TestUnixProcessingLeaseRejectsExistingHardlinkBeforeMutation(t *testing.T) {
	root := t.TempDir()
	workspace := ProcessingWorkspace{OutputRoot: root, Directory: filepath.Join(root, "processing"), OperationIdentity: "op", InputFingerprint: "input"}
	if err := ensureProcessingWorkspaceDirectory(root, workspace.Directory); err != nil {
		t.Fatal(err)
	}
	if err := ensureProcessingGuardDirectory(root, workspace.Directory); err != nil {
		t.Fatal(err)
	}
	path := processingLeasePath(workspace.Directory)
	outside := filepath.Join(root, "lease-hardlink")
	if err := os.WriteFile(path, []byte("lease"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(path, outside); err != nil {
		t.Skipf("hardlinks unavailable: %v", err)
	}
	if _, err := acquireProcessingLease(path); err == nil {
		t.Fatal("hardlinked lease unexpectedly acquired")
	}
	info, err := os.Stat(outside)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("hardlinked lease permissions changed: %o", info.Mode().Perm())
	}
}

func TestProcessingWorkspaceCancellationWaitsForDescendantProcessTree(t *testing.T) {
	root := t.TempDir()
	input := filepath.Join(root, "input.m4a")
	destination := filepath.Join(root, "output.mka")
	childPIDPath := filepath.Join(root, "child.pid")
	markerPath := filepath.Join(root, "child.started")
	if err := os.WriteFile(input, []byte("complete input"), 0o600); err != nil {
		t.Fatal(err)
	}
	helper := filepath.Join(root, "ffmpeg-descendant-helper.sh")
	script := `#!/bin/sh
for argument do
  case "$argument" in
    --output=*) output=${argument#--output=} ;;
  esac
done
printf partial > "$output"
exec "$PROCESS_TEST_BINARY" -test.run=TestUnixProcessHelper \
  -ytdlp-ffmpeg-unix-process-mode=parent \
  -ytdlp-ffmpeg-unix-process-child-pid="$PROCESS_CHILD_PID" \
  -ytdlp-ffmpeg-unix-process-marker="$PROCESS_CHILD_MARKER"
`
	if err := os.WriteFile(helper, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PROCESS_TEST_BINARY", os.Args[0])
	t.Setenv("PROCESS_CHILD_PID", childPIDPath)
	t.Setenv("PROCESS_CHILD_MARKER", markerPath)
	workspace := testProcessingWorkspace(root, destination)
	tools := &Toolset{ffmpeg: helper, maxOutput: 1 << 20}
	ctx, cancel := context.WithCancel(context.Background())
	sink := events.SinkFunc(func(_ context.Context, event events.Event) error {
		if event.Kind == events.KindPostprocessProgress {
			cancel()
		}
		return nil
	})
	err := tools.runAtomicWorkspace(ctx, destination, false, sink, workspace, nil, func(stage string) []string {
		return []string{"--output=" + stage}
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("processing cancellation = %v", err)
	}
	encodedPID, err := os.ReadFile(childPIDPath)
	if err != nil {
		t.Fatalf("descendant did not start: %v", err)
	}
	childPID, err := strconv.Atoi(string(encodedPID))
	if err != nil {
		t.Fatal(err)
	}
	assertProcessGone(t, childPID)
	if _, err := os.Stat(filepath.Join(workspace.Directory, "output.mka")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("incomplete output remains after process-tree settlement: %v", err)
	}
	if contents, err := os.ReadFile(input); err != nil || string(contents) != "complete input" {
		t.Fatalf("input changed: %q, %v", contents, err)
	}
}

func TestUnixProcessingWorkspaceRejectsUnprotectedAndSymlinkEvidence(t *testing.T) {
	t.Run("state permissions", func(t *testing.T) {
		root := t.TempDir()
		destination := filepath.Join(root, "output.mka")
		workspace := testProcessingWorkspace(root, destination)
		writeProcessingFixture(t, workspace, processingState{Version: processingStateVersion, OperationIdentity: workspace.OperationIdentity, InputFingerprint: workspace.InputFingerprint, Phase: processingPhaseRunning}, "", destination)
		if err := os.Chmod(filepath.Join(workspace.Directory, "state.json"), 0o644); err != nil {
			t.Fatal(err)
		}
		err := (&Toolset{}).runAtomicWorkspace(context.Background(), destination, false, nil, workspace, nil, nil)
		if !errors.Is(err, ErrInvalidProcessingWorkspace) {
			t.Fatalf("permissive state accepted: %v", err)
		}
	})
	t.Run("state symlink", func(t *testing.T) {
		root := t.TempDir()
		destination := filepath.Join(root, "output.mka")
		workspace := testProcessingWorkspace(root, destination)
		ensureProcessingTestDirectory(t, workspace)
		outside := filepath.Join(root, "outside-state")
		if err := os.WriteFile(outside, []byte("state"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(outside, filepath.Join(workspace.Directory, "state.json")); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}
		err := (&Toolset{}).runAtomicWorkspace(context.Background(), destination, false, nil, workspace, nil, nil)
		if !errors.Is(err, ErrInvalidProcessingWorkspace) {
			t.Fatalf("state symlink accepted: %v", err)
		}
		if contents, readErr := os.ReadFile(outside); readErr != nil || string(contents) != "state" {
			t.Fatalf("outside state changed: %q, %v", contents, readErr)
		}
	})
}
