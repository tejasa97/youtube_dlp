package ffmpeg

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tejasa97/ytdlp-go/internal/atomicfile"
	"github.com/tejasa97/ytdlp-go/internal/events"
)

func TestProcessingWorkspaceCancellationPreservesInputsAndRemovesIncompleteOutput(t *testing.T) {
	root := t.TempDir()
	input := filepath.Join(root, "input-track-secret-name.m4a")
	destination := filepath.Join(root, "output.mka")
	if err := os.WriteFile(input, []byte("complete-input"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(destination, []byte("existing-destination"), 0o600); err != nil {
		t.Fatal(err)
	}
	workspace := testProcessingWorkspace(root, destination)
	tools := &Toolset{ffmpeg: writeProcessingHelper(t, root), maxOutput: 1 << 20}
	ctx, cancel := context.WithCancel(context.Background())
	sink := events.SinkFunc(func(_ context.Context, event events.Event) error {
		if event.Kind == events.KindPostprocessProgress {
			cancel()
		}
		return nil
	})
	err := tools.runAtomicWorkspace(ctx, destination, true, sink, workspace, nil, processingOperation(input, "interrupt"))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation error = %v", err)
	}
	contents, readErr := os.ReadFile(input)
	if readErr != nil || string(contents) != "complete-input" {
		t.Fatalf("input changed: %q, %v", contents, readErr)
	}
	if _, err := os.Stat(filepath.Join(workspace.Directory, "output.mka")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("incomplete output remains authoritative: %v", err)
	}
	if contents, err := os.ReadFile(destination); err != nil || string(contents) != "existing-destination" {
		t.Fatalf("existing destination changed: %q, %v", contents, err)
	}
	state := readProcessingStateForTest(t, workspace.Directory)
	if state.Phase != processingPhaseRunning || state.Output != nil {
		t.Fatalf("state = %#v", state)
	}
}

func TestProcessingWorkspaceRestartDiscardsOwnedIncompleteOutput(t *testing.T) {
	root := t.TempDir()
	input := filepath.Join(root, "input.m4a")
	destination := filepath.Join(root, "output.mka")
	if err := os.WriteFile(input, []byte("complete-input"), 0o600); err != nil {
		t.Fatal(err)
	}
	workspace := testProcessingWorkspace(root, destination)
	writeProcessingFixture(t, workspace, processingState{Version: processingStateVersion, OperationIdentity: workspace.OperationIdentity, InputFingerprint: workspace.InputFingerprint, Phase: processingPhaseRunning}, "partial-crash-output", destination)
	tools := &Toolset{ffmpeg: writeProcessingHelper(t, root), maxOutput: 1 << 20}
	if err := tools.runAtomicWorkspace(context.Background(), destination, false, nil, workspace, nil, processingOperation(input, "complete")); err != nil {
		t.Fatal(err)
	}
	contents, err := os.ReadFile(destination)
	if err != nil || string(contents) != "complete-input" {
		t.Fatalf("destination = %q, %v", contents, err)
	}
	if contents, err := os.ReadFile(input); err != nil || string(contents) != "complete-input" {
		t.Fatalf("input = %q, %v", contents, err)
	}
	if _, err := os.Stat(workspace.Directory); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("committed workspace remains: %v", err)
	}
}

func TestProcessingWorkspaceReusesOnlyVerifiedCompleteOutput(t *testing.T) {
	root := t.TempDir()
	destination := filepath.Join(root, "output.mka")
	workspace := testProcessingWorkspace(root, destination)
	writeProcessingFixture(t, workspace, processingState{
		Version: processingStateVersion, OperationIdentity: workspace.OperationIdentity,
		InputFingerprint: workspace.InputFingerprint, Phase: processingPhaseOutputComplete,
	}, "verified-complete-output", destination)
	tools := &Toolset{ffmpeg: filepath.Join(root, "must-not-run")}
	err := tools.runAtomicWorkspace(context.Background(), destination, false, nil, workspace, nil, func(string) []string {
		t.Fatal("verified output restarted FFmpeg")
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	contents, err := os.ReadFile(destination)
	if err != nil || string(contents) != "verified-complete-output" {
		t.Fatalf("destination = %q, %v", contents, err)
	}
}

func TestProcessingWorkspaceReopenPhaseAuthority(t *testing.T) {
	for _, test := range []struct {
		phase         string
		committed     bool
		indeterminate bool
	}{
		{phase: processingPhasePublishing, indeterminate: true},
		{phase: processingPhaseIndeterminate, indeterminate: true},
		{phase: processingPhaseCommitted, committed: true},
	} {
		t.Run(test.phase, func(t *testing.T) {
			root := t.TempDir()
			destination := filepath.Join(root, "output.mka")
			workspace := testProcessingWorkspace(root, destination)
			writeProcessingFixture(t, workspace, processingState{Version: processingStateVersion, OperationIdentity: workspace.OperationIdentity, InputFingerprint: workspace.InputFingerprint, Phase: test.phase}, "complete", destination)
			err := (&Toolset{}).runAtomicWorkspace(context.Background(), destination, true, nil, workspace, nil, func(string) []string {
				t.Fatal("uncertain phase restarted ffmpeg")
				return nil
			})
			var authority atomicfile.CommitError
			if !errors.As(err, &authority) || authority.Committed() != test.committed || authority.Indeterminate() != test.indeterminate {
				t.Fatalf("authority = %#v, %v", authority, err)
			}
		})
	}
}

func TestProcessingWorkspaceRejectsIdentityCorruptionUnknownAndHardlinks(t *testing.T) {
	t.Run("identity mismatch", func(t *testing.T) {
		root := t.TempDir()
		destination := filepath.Join(root, "output.mka")
		workspace := testProcessingWorkspace(root, destination)
		writeProcessingFixture(t, workspace, processingState{Version: processingStateVersion, OperationIdentity: workspace.OperationIdentity, InputFingerprint: workspace.InputFingerprint, Phase: processingPhaseRunning}, "partial", destination)
		workspace.InputFingerprint = "different-input-set"
		before := snapshotProcessingWorkspace(t, workspace.Directory)
		err := (&Toolset{}).runAtomicWorkspace(context.Background(), destination, false, nil, workspace, nil, nil)
		if !errors.Is(err, ErrProcessingReconciliation) {
			t.Fatalf("mismatch error = %v", err)
		}
		if after := snapshotProcessingWorkspace(t, workspace.Directory); !mapsEqual(before, after) {
			t.Fatalf("mismatch changed evidence: before=%v after=%v", before, after)
		}
	})
	t.Run("corrupt", func(t *testing.T) {
		for name, encoded := range map[string][]byte{
			"malformed":        []byte("{ trailing"),
			"trailing":         []byte(`{"version":1} trailing`),
			"unknown-field":    []byte(`{"version":1,"operation_identity":"op","input_fingerprint":"in","phase":"running","command":"secret"}`),
			"oversized":        bytes.Repeat([]byte("x"), maxProcessingState+1),
			"unknown-phase":    []byte(`{"version":1,"operation_identity":"op","input_fingerprint":"in","phase":"finished"}`),
			"running-output":   []byte(`{"version":1,"operation_identity":"op","input_fingerprint":"in","phase":"running","output":{"bytes":1,"sha256":"0000000000000000000000000000000000000000000000000000000000000000"}}`),
			"bad-artifact":     []byte(`{"version":1,"operation_identity":"op","input_fingerprint":"in","phase":"output_complete","output":{"bytes":0,"sha256":"bad"}}`),
			"control-identity": []byte("{\"version\":1,\"operation_identity\":\"op\\u0007\",\"input_fingerprint\":\"in\",\"phase\":\"running\"}"),
		} {
			t.Run(name, func(t *testing.T) {
				root := t.TempDir()
				destination := filepath.Join(root, "output.mka")
				workspace := testProcessingWorkspace(root, destination)
				ensureProcessingTestDirectory(t, workspace)
				if err := os.WriteFile(filepath.Join(workspace.Directory, "state.json"), encoded, 0o600); err != nil {
					t.Fatal(err)
				}
				before := snapshotProcessingWorkspace(t, workspace.Directory)
				err := (&Toolset{}).runAtomicWorkspace(context.Background(), destination, false, nil, workspace, nil, nil)
				if !errors.Is(err, ErrInvalidProcessingWorkspace) {
					t.Fatalf("corrupt error = %v", err)
				}
				if after := snapshotProcessingWorkspace(t, workspace.Directory); !mapsEqual(before, after) {
					t.Fatalf("invalid state changed evidence")
				}
			})
		}
	})
	t.Run("unknown evidence", func(t *testing.T) {
		root := t.TempDir()
		destination := filepath.Join(root, "output.mka")
		workspace := testProcessingWorkspace(root, destination)
		writeProcessingFixture(t, workspace, processingState{Version: processingStateVersion, OperationIdentity: workspace.OperationIdentity, InputFingerprint: workspace.InputFingerprint, Phase: processingPhaseRunning}, "", destination)
		if err := os.WriteFile(filepath.Join(workspace.Directory, "command.args"), []byte("secret"), 0o600); err != nil {
			t.Fatal(err)
		}
		err := (&Toolset{}).runAtomicWorkspace(context.Background(), destination, false, nil, workspace, nil, nil)
		if !errors.Is(err, ErrProcessingReconciliation) {
			t.Fatalf("unknown evidence error = %v", err)
		}
	})
	t.Run("hardlink", func(t *testing.T) {
		root := t.TempDir()
		destination := filepath.Join(root, "output.mka")
		workspace := testProcessingWorkspace(root, destination)
		writeProcessingFixture(t, workspace, processingState{Version: processingStateVersion, OperationIdentity: workspace.OperationIdentity, InputFingerprint: workspace.InputFingerprint, Phase: processingPhaseRunning}, "partial", destination)
		stage := filepath.Join(workspace.Directory, "output.mka")
		if err := os.Link(stage, filepath.Join(root, "outside-link")); err != nil {
			t.Skipf("hardlinks unavailable: %v", err)
		}
		err := (&Toolset{}).runAtomicWorkspace(context.Background(), destination, false, nil, workspace, nil, nil)
		if !errors.Is(err, ErrInvalidProcessingWorkspace) {
			t.Fatalf("hardlink error = %v", err)
		}
	})
}

func TestProcessingWorkspaceRejectsUnsafeConfiguration(t *testing.T) {
	root := t.TempDir()
	destination := filepath.Join(root, "output.mka")
	for _, invalid := range []string{"", "identity\x00tail", "identity\rbreak", "identity\nbreak", strings.Repeat("x", maxProcessingIdentity+1)} {
		workspace := testProcessingWorkspace(root, destination)
		workspace.OperationIdentity = invalid
		if err := validateProcessingWorkspace(workspace, destination); !errors.Is(err, ErrInvalidProcessingWorkspace) {
			t.Fatalf("identity %q accepted: %v", invalid, err)
		}
	}
	workspace := testProcessingWorkspace(root, destination)
	outside := filepath.Join(filepath.Dir(root), "outside.mka")
	if err := validateProcessingWorkspace(workspace, outside); !errors.Is(err, ErrInvalidProcessingWorkspace) {
		t.Fatalf("destination outside root accepted: %v", err)
	}
	t.Run("destination symlink chain", func(t *testing.T) {
		target := t.TempDir()
		link := filepath.Join(root, "linked")
		if err := os.Symlink(target, link); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}
		if _, err := validateProcessingDestination(root, filepath.Join(link, "output.mka")); !errors.Is(err, ErrInvalidProcessingWorkspace) {
			t.Fatalf("symlinked destination chain accepted: %v", err)
		}
	})
	t.Run("nested destination symlink chain", func(t *testing.T) {
		first := filepath.Join(root, "first")
		if err := os.Mkdir(first, 0o755); err != nil {
			t.Fatal(err)
		}
		target := t.TempDir()
		link := filepath.Join(first, "linked")
		if err := os.Symlink(target, link); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}
		if _, err := validateProcessingDestination(root, filepath.Join(link, "output.mka")); !errors.Is(err, ErrInvalidProcessingWorkspace) {
			t.Fatalf("nested symlinked destination chain accepted: %v", err)
		}
	})
	t.Run("precreated protected workspace", func(t *testing.T) {
		workspace := testProcessingWorkspace(root, destination)
		if err := os.Mkdir(workspace.Directory, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := ensureProcessingWorkspaceDirectory(root, workspace.Directory); err != nil {
			t.Fatal(err)
		}
	})
}

func TestProcessingWorkspacePreflightIsReadOnlyAndPrecedesFFprobe(t *testing.T) {
	root := t.TempDir()
	destination := filepath.Join(root, "new-parent", "output.mka")
	workspace := testProcessingWorkspace(root, destination)
	workspace.Directory = filepath.Join(root, "processing-workspace")
	writeProcessingFixture(t, workspace, processingState{
		Version: processingStateVersion, OperationIdentity: workspace.OperationIdentity,
		InputFingerprint: workspace.InputFingerprint, Phase: processingPhaseRunning,
	}, "", destination)
	if err := os.WriteFile(filepath.Join(workspace.Directory, "state.json"), []byte("{corrupt"), 0o600); err != nil {
		t.Fatal(err)
	}
	input := filepath.Join(root, "input.m4a")
	if err := os.WriteFile(input, []byte("input"), 0o600); err != nil {
		t.Fatal(err)
	}
	probeMarker := filepath.Join(root, "ffprobe-ran")
	probeHelper := filepath.Join(root, "ffprobe-helper.sh")
	if err := os.WriteFile(probeHelper, []byte("#!/bin/sh\nprintf ran > \"$PROBE_MARKER\"\nexit 1\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PROBE_MARKER", probeMarker)
	tools := &Toolset{ffprobe: probeHelper, ffmpeg: filepath.Join(root, "must-not-run")}
	err := tools.MergeTracksWithWorkspace(context.Background(), []MergeInput{{Path: input, HasAudio: true, Protocol: "m3u8"}}, destination, false, nil, workspace)
	if !errors.Is(err, ErrInvalidProcessingWorkspace) {
		t.Fatalf("preflight error = %v", err)
	}
	if _, err := os.Stat(filepath.Dir(destination)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("destination parent was created before publication: %v", err)
	}
	if _, err := os.Stat(probeMarker); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("ffprobe ran before workspace validation: %v", err)
	}
}

func TestProcessingWorkspaceExclusiveLease(t *testing.T) {
	root := t.TempDir()
	destination := filepath.Join(root, "output.mka")
	workspace := testProcessingWorkspace(root, destination)
	ensureProcessingTestDirectory(t, workspace)
	if err := ensureProcessingGuardDirectory(root, workspace.Directory); err != nil {
		t.Fatal(err)
	}
	path := processingLeasePath(workspace.Directory)
	first, err := acquireProcessingLease(path)
	if err != nil {
		t.Fatal(err)
	}
	defer first.release()
	if second, err := acquireProcessingLease(path); err == nil {
		_ = second.release()
		t.Fatal("concurrent processing lease unexpectedly acquired")
	}
	if err := first.release(); err != nil {
		t.Fatal(err)
	}
	third, err := acquireProcessingLease(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := third.release(); err != nil {
		t.Fatal(err)
	}
}

func TestProcessingWorkspaceRetriesCommittedCleanupMarker(t *testing.T) {
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
	injected := errors.New("retain cleanup marker")
	ops.remove = func(path string) error {
		if path == workspace.Directory {
			return injected
		}
		return productionRemove(path)
	}
	err := tools.runAtomicWorkspaceWithOps(context.Background(), destination, false, nil, workspace, nil, processingOperation(input, "complete"), ops)
	var authority atomicfile.CommitError
	if !errors.Is(err, injected) || !errors.As(err, &authority) || !authority.Committed() {
		t.Fatalf("initial cleanup authority = %#v, %v", authority, err)
	}
	if _, err := os.Stat(processingCleanupMarkerPath(workspace.Directory)); err != nil {
		t.Fatalf("cleanup marker missing: %v", err)
	}
	second := &Toolset{ffmpeg: filepath.Join(root, "must-not-run")}
	err = second.runAtomicWorkspace(context.Background(), destination, true, nil, workspace, nil, func(string) []string {
		t.Fatal("cleanup retry restarted FFmpeg")
		return nil
	})
	if !errors.As(err, &authority) || !authority.Committed() {
		t.Fatalf("cleanup retry authority = %v", err)
	}
	for _, path := range []string{workspace.Directory, processingCleanupMarkerPath(workspace.Directory), processingLeasePath(workspace.Directory)} {
		if _, statErr := os.Stat(path); !errors.Is(statErr, os.ErrNotExist) {
			t.Fatalf("cleanup evidence remains at %s: %v (retry error=%v)", path, statErr, err)
		}
	}
}

func TestProcessingWorkspaceRecoversAfterCleanupMarkerCommitBoundary(t *testing.T) {
	root := t.TempDir()
	input := filepath.Join(root, "input")
	destination := filepath.Join(root, "output.mka")
	if err := os.WriteFile(input, []byte("candidate"), 0o600); err != nil {
		t.Fatal(err)
	}
	workspace := testProcessingWorkspace(root, destination)
	tools := &Toolset{ffmpeg: writeProcessingHelper(t, root), maxOutput: 1 << 20}
	ops := productionProcessingWorkspaceOps
	productionSync := ops.syncDirectory
	injected := errors.New("cleanup marker sync indeterminate")
	calls := 0
	ops.syncDirectory = func(path string) error {
		calls++
		if calls == 5 {
			return injected
		}
		return productionSync(path)
	}
	err := tools.runAtomicWorkspaceWithOps(context.Background(), destination, false, nil, workspace, nil, processingOperation(input, "complete"), ops)
	var authority atomicfile.CommitError
	if !errors.Is(err, injected) || !errors.As(err, &authority) || !authority.Committed() {
		t.Fatalf("cleanup boundary authority = %#v, %v", authority, err)
	}
	if _, err := os.Stat(workspace.Directory); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("workspace unexpectedly remains after marker boundary: %v", err)
	}
	second := &Toolset{ffmpeg: filepath.Join(root, "must-not-run")}
	err = second.runAtomicWorkspace(context.Background(), destination, true, nil, workspace, nil, func(string) []string {
		t.Fatal("cleanup boundary retry restarted FFmpeg")
		return nil
	})
	if !errors.As(err, &authority) || !authority.Committed() {
		t.Fatalf("cleanup boundary retry authority = %v", err)
	}
	for _, path := range []string{processingLeasePath(workspace.Directory), processingCleanupMarkerPath(workspace.Directory)} {
		if _, statErr := os.Stat(path); !errors.Is(statErr, os.ErrNotExist) {
			t.Fatalf("cleanup boundary evidence remains at %s: %v", path, statErr)
		}
	}
}

func TestProcessingWorkspaceGuardRemovalFailureFailsClosed(t *testing.T) {
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
	injected := errors.New("guard removal")
	ops.remove = func(path string) error {
		if path == processingGuardPath(workspace.Directory) {
			return injected
		}
		return productionRemove(path)
	}
	err := tools.runAtomicWorkspaceWithOps(context.Background(), destination, false, nil, workspace, nil, processingOperation(input, "complete"), ops)
	var authority atomicfile.CommitError
	if !errors.Is(err, injected) || !errors.As(err, &authority) || !authority.Committed() {
		t.Fatalf("guard removal authority = %#v, %v", authority, err)
	}
	if _, err := os.Stat(workspace.Directory); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("workspace remains after guard removal fault: %v", err)
	}
	if _, err := os.Stat(processingGuardPath(workspace.Directory)); err != nil {
		t.Fatalf("guard evidence lost after removal fault: %v", err)
	}
	second := &Toolset{ffmpeg: filepath.Join(root, "must-not-run")}
	err = second.runAtomicWorkspace(context.Background(), destination, true, nil, workspace, nil, func(string) []string {
		t.Fatal("guard-removal retry restarted FFmpeg")
		return nil
	})
	if !errors.Is(err, ErrProcessingReconciliation) {
		t.Fatalf("guard-removal retry was not fail-closed: %v", err)
	}
}

func TestProcessingWorkspaceStateDoesNotPersistSecretsOrPaths(t *testing.T) {
	root := t.TempDir()
	input := filepath.Join(root, "input-token-secret.m4a")
	destination := filepath.Join(root, "output.mka")
	if err := os.WriteFile(input, []byte("input"), 0o600); err != nil {
		t.Fatal(err)
	}
	workspace := testProcessingWorkspace(root, destination)
	tools := &Toolset{ffmpeg: writeProcessingHelper(t, root), maxOutput: 1 << 20}
	ctx, cancel := context.WithCancel(context.Background())
	sink := events.SinkFunc(func(_ context.Context, event events.Event) error {
		if event.Kind == events.KindPostprocessProgress {
			cancel()
		}
		return nil
	})
	secretArg := "https://example.test/media?token=credential-secret"
	err := tools.runAtomicWorkspace(ctx, destination, false, sink, workspace, nil, func(stage string) []string {
		return []string{"--source=" + input, "--mode=interrupt", "--credential=" + secretArg, "--output=" + stage}
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	encoded, err := os.ReadFile(filepath.Join(workspace.Directory, "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{input, destination, secretArg, "credential-secret", "--source", "--credential"} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("state persisted forbidden value %q: %s", forbidden, encoded)
		}
	}
}

func TestProcessingWorkspacePublicationRaceAndOutcomes(t *testing.T) {
	t.Run("late destination no clobber", func(t *testing.T) {
		root := t.TempDir()
		input := filepath.Join(root, "input")
		destination := filepath.Join(root, "output.mka")
		_ = os.WriteFile(input, []byte("candidate"), 0o600)
		workspace := testProcessingWorkspace(root, destination)
		tools := &Toolset{ffmpeg: writeProcessingHelper(t, root), maxOutput: 1 << 20}
		ops := productionProcessingWorkspaceOps
		productionPublish := ops.publishNoClobber
		ops.publishNoClobber = func(source, destination string) error {
			if err := os.WriteFile(destination, []byte("late-existing-media"), 0o600); err != nil {
				return err
			}
			return productionPublish(source, destination)
		}
		err := tools.runAtomicWorkspaceWithOps(context.Background(), destination, false, nil, workspace, nil, processingOperation(input, "complete"), ops)
		if err == nil {
			t.Fatal("late destination unexpectedly published")
		}
		contents, _ := os.ReadFile(destination)
		if string(contents) != "late-existing-media" {
			t.Fatalf("late destination = %q", contents)
		}
	})
	for _, test := range []struct {
		name          string
		committed     bool
		indeterminate bool
	}{
		{name: "committed", committed: true},
		{name: "indeterminate", indeterminate: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			input := filepath.Join(root, "input")
			destination := filepath.Join(root, "output.mka")
			_ = os.WriteFile(input, []byte("candidate"), 0o600)
			workspace := testProcessingWorkspace(root, destination)
			tools := &Toolset{ffmpeg: writeProcessingHelper(t, root), maxOutput: 1 << 20}
			ops := productionProcessingWorkspaceOps
			injected := &processingTestCommitError{name: test.name, committed: test.committed, indeterminate: test.indeterminate}
			ops.publishNoClobber = func(string, string) error { return injected }
			err := tools.runAtomicWorkspaceWithOps(context.Background(), destination, false, nil, workspace, nil, processingOperation(input, "complete"), ops)
			var authority atomicfile.CommitError
			if !errors.As(err, &authority) || authority.Committed() != test.committed || authority.Indeterminate() != test.indeterminate {
				t.Fatalf("authority = %#v, %v", authority, err)
			}
			state := readProcessingStateForTest(t, workspace.Directory)
			wantPhase := processingPhaseCommitted
			if test.indeterminate {
				wantPhase = processingPhaseIndeterminate
			}
			if state.Phase != wantPhase {
				t.Fatalf("phase = %s, want %s", state.Phase, wantPhase)
			}
		})
	}
}

func TestProcessingWorkspaceStateCommitAndCleanupFaults(t *testing.T) {
	t.Run("output complete state uncertain", func(t *testing.T) {
		runProcessingStateWriteFault(t, processingPhaseOutputComplete, &processingTestCommitError{name: "output complete", committed: true}, ErrProcessingReconciliation)
	})
	t.Run("publication boundary uncertain", func(t *testing.T) {
		runProcessingStateWriteFault(t, processingPhasePublishing, &processingTestCommitError{name: "publishing", indeterminate: true}, ErrProcessingReconciliation)
	})
	t.Run("committed state precommit failure", func(t *testing.T) {
		runProcessingStateWriteFault(t, processingPhaseCommitted, &processingTestCommitError{name: "committed state"}, ErrProcessingCommitted)
	})
	t.Run("cleanup state removal", func(t *testing.T) {
		injected := errors.New("cleanup state removal")
		runProcessingCleanupFault(t, func(ops *processingWorkspaceOps, workspace ProcessingWorkspace) {
			productionRemove := ops.remove
			ops.remove = func(path string) error {
				if filepath.Base(path) == "state.json" {
					return injected
				}
				return productionRemove(path)
			}
		}, injected, true)
	})
	t.Run("cleanup precommit sync", func(t *testing.T) {
		injected := errors.New("cleanup precommit sync")
		runProcessingCleanupFault(t, func(ops *processingWorkspaceOps, _ ProcessingWorkspace) {
			ops.syncDirectory = func(string) error { return injected }
		}, injected, true)
	})
	t.Run("cleanup postcommit sync", func(t *testing.T) {
		injected := errors.New("cleanup postcommit sync")
		runProcessingCleanupFault(t, func(ops *processingWorkspaceOps, _ ProcessingWorkspace) {
			productionSync := ops.syncDirectory
			calls := 0
			ops.syncDirectory = func(path string) error {
				calls++
				if calls == 6 {
					return injected
				}
				return productionSync(path)
			}
		}, injected, false)
	})
	t.Run("cleanup directory removal", func(t *testing.T) {
		injected := errors.New("cleanup directory removal")
		runProcessingCleanupFault(t, func(ops *processingWorkspaceOps, workspace ProcessingWorkspace) {
			productionRemove := ops.remove
			ops.remove = func(path string) error {
				if path == workspace.Directory {
					return injected
				}
				return productionRemove(path)
			}
		}, injected, false)
	})
}

func TestMergeTracksWithWorkspace(t *testing.T) {
	tools := requireToolset(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	root := t.TempDir()
	video := filepath.Join(root, "video.mp4")
	audio := filepath.Join(root, "audio.m4a")
	if _, err := tools.execute(ctx, tools.ffmpeg, []string{
		"-nostdin", "-y", "-f", "lavfi", "-i", "color=c=black:s=16x16:d=0.3",
		"-an", "-c:v", "mpeg4", video,
	}, nil); err != nil {
		t.Fatalf("generate video: %v", err)
	}
	generateAudio(t, ctx, tools, audio)
	destination := filepath.Join(root, "merged.mkv")
	workspace := testProcessingWorkspace(root, destination)
	err := tools.MergeTracksWithWorkspace(ctx, []MergeInput{{Path: video, HasVideo: true}, {Path: audio, HasAudio: true}}, destination, false, nil, workspace)
	if err != nil {
		t.Fatal(err)
	}
	for _, input := range []string{video, audio} {
		if _, err := os.Stat(input); err != nil {
			t.Fatalf("input removed: %v", err)
		}
	}
	if _, err := os.Stat(destination); err != nil {
		t.Fatal(err)
	}
}

type processingTestCommitError struct {
	name          string
	committed     bool
	indeterminate bool
}

func (failure *processingTestCommitError) Error() string       { return "injected " + failure.name }
func (failure *processingTestCommitError) Committed() bool     { return failure.committed }
func (failure *processingTestCommitError) Indeterminate() bool { return failure.indeterminate }

func testProcessingWorkspace(root, destination string) ProcessingWorkspace {
	return ProcessingWorkspace{
		OutputRoot: root, Directory: destination + ".processing",
		OperationIdentity: "merge:v1:container=mka", InputFingerprint: "inputs:sha256:stable-caller-fingerprint",
	}
}

func processingOperation(source, mode string) func(string) []string {
	return func(stage string) []string {
		return []string{"--source=" + source, "--mode=" + mode, "--output=" + stage}
	}
}

func writeProcessingHelper(t *testing.T, root string) string {
	t.Helper()
	path := filepath.Join(root, "ffmpeg-processing-helper.sh")
	const script = `#!/bin/sh
for argument do
  case "$argument" in
    --source=*) source=${argument#--source=} ;;
    --output=*) output=${argument#--output=} ;;
    --mode=*) mode=${argument#--mode=} ;;
  esac
done
case "$mode" in
  complete)
    cp "$source" "$output"
    printf 'total_size=%s\nprogress=end\n' "$(wc -c < "$output")"
    ;;
  interrupt)
    printf partial > "$output"
    printf 'total_size=7\nprogress=continue\n'
    while :; do sleep 1; done
    ;;
  *) exit 91 ;;
esac
`
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}

func ensureProcessingTestDirectory(t *testing.T, workspace ProcessingWorkspace) {
	t.Helper()
	if err := ensureProcessingWorkspaceDirectory(workspace.OutputRoot, workspace.Directory); err != nil {
		t.Fatal(err)
	}
}

func writeProcessingFixture(t *testing.T, workspace ProcessingWorkspace, state processingState, stageContents, destination string) {
	t.Helper()
	ensureProcessingTestDirectory(t, workspace)
	if stageContents != "" {
		stage := filepath.Join(workspace.Directory, "output"+filepath.Ext(destination))
		if err := os.WriteFile(stage, []byte(stageContents), 0o600); err != nil {
			t.Fatal(err)
		}
		if state.Phase != processingPhaseRunning {
			artifact, err := inspectProcessingArtifact(stage)
			if err != nil {
				t.Fatal(err)
			}
			state.Output = &artifact
		}
	}
	if err := writeProcessingState(filepath.Join(workspace.Directory, "state.json"), state, atomicfile.Write); err != nil {
		t.Fatal(err)
	}
}

func readProcessingStateForTest(t *testing.T, directory string) processingState {
	t.Helper()
	encoded, err := os.ReadFile(filepath.Join(directory, "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	var state processingState
	if err := json.Unmarshal(encoded, &state); err != nil {
		t.Fatal(err)
	}
	return state
}

func snapshotProcessingWorkspace(t *testing.T, directory string) map[string]string {
	t.Helper()
	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatal(err)
	}
	result := make(map[string]string, len(entries))
	for _, entry := range entries {
		contents, err := os.ReadFile(filepath.Join(directory, entry.Name()))
		if err != nil {
			t.Fatal(err)
		}
		result[entry.Name()] = string(contents)
	}
	return result
}

func mapsEqual(left, right map[string]string) bool {
	if len(left) != len(right) {
		return false
	}
	for key, value := range left {
		if right[key] != value {
			return false
		}
	}
	return true
}

func runProcessingStateWriteFault(t *testing.T, phase string, injected error, want error) {
	t.Helper()
	root := t.TempDir()
	input := filepath.Join(root, "input")
	destination := filepath.Join(root, "output.mka")
	_ = os.WriteFile(input, []byte("candidate"), 0o600)
	workspace := testProcessingWorkspace(root, destination)
	tools := &Toolset{ffmpeg: writeProcessingHelper(t, root), maxOutput: 1 << 20}
	ops := productionProcessingWorkspaceOps
	productionWrite := ops.writeAtomic
	ops.writeAtomic = func(path string, mode os.FileMode, encode func(io.Writer) error) error {
		var buffer bytes.Buffer
		if err := encode(&buffer); err != nil {
			return err
		}
		var state processingState
		_ = json.Unmarshal(buffer.Bytes(), &state)
		if filepath.Base(path) == "state.json" && state.Phase == phase {
			var commitErr atomicfile.CommitError
			if errors.As(injected, &commitErr) && commitErr.Committed() {
				if err := productionWrite(path, mode, func(writer io.Writer) error { _, err := writer.Write(buffer.Bytes()); return err }); err != nil {
					return err
				}
			}
			return injected
		}
		return productionWrite(path, mode, func(writer io.Writer) error { _, err := writer.Write(buffer.Bytes()); return err })
	}
	err := tools.runAtomicWorkspaceWithOps(context.Background(), destination, false, nil, workspace, nil, processingOperation(input, "complete"), ops)
	if !errors.Is(err, want) {
		t.Fatalf("phase %s error = %v, want %v", phase, err, want)
	}
}

func runProcessingCleanupFault(t *testing.T, inject func(*processingWorkspaceOps, ProcessingWorkspace), injected error, markerRetained bool) {
	t.Helper()
	root := t.TempDir()
	input := filepath.Join(root, "input")
	destination := filepath.Join(root, "output.mka")
	if err := os.WriteFile(input, []byte("candidate"), 0o600); err != nil {
		t.Fatal(err)
	}
	workspace := testProcessingWorkspace(root, destination)
	tools := &Toolset{ffmpeg: writeProcessingHelper(t, root), maxOutput: 1 << 20}
	ops := productionProcessingWorkspaceOps
	inject(&ops, workspace)
	err := tools.runAtomicWorkspaceWithOps(context.Background(), destination, false, nil, workspace, nil, processingOperation(input, "complete"), ops)
	var authority atomicfile.CommitError
	if !errors.Is(err, injected) || !errors.As(err, &authority) || !authority.Committed() {
		t.Fatalf("cleanup authority = %#v, %v", authority, err)
	}
	_, stateErr := os.Stat(filepath.Join(workspace.Directory, "state.json"))
	if markerRetained && stateErr != nil {
		t.Fatalf("cleanup authority marker lost: %v", stateErr)
	}
	if !markerRetained && !errors.Is(stateErr, os.ErrNotExist) {
		t.Fatalf("cleanup commit marker unexpectedly remains: %v", stateErr)
	}
	if contents, err := os.ReadFile(destination); err != nil || string(contents) != "candidate" {
		t.Fatalf("committed destination = %q, %v", contents, err)
	}
}

var _ atomicfile.CommitError = (*processingTestCommitError)(nil)
