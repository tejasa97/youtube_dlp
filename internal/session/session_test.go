package session

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/tejasa97/youtube_dlp/internal/atomicfile"
)

var testNow = time.Date(2026, time.August, 11, 12, 0, 0, 0, time.UTC)

func testCreateOptions(root string) CreateOptions {
	return CreateOptions{
		OutputRoot: root,
		Source: SourceIntent{
			Provider:     "fixture",
			ID:           "video-001",
			Kind:         "video",
			CanonicalURL: "https://Example.invalid/watch?v=secret-source&sig=secret-signature",
		},
		Output: OutputIntent{
			Container:    "mp4",
			Extension:    "mp4",
			PlanIdentity: "bestvideo+bestaudio:mp4",
		},
		RelativeDestination: "downloads/video.mp4",
		Components: []Component{
			{ID: "video", Kind: "video", ObservedBytes: 128, CommittedBytes: 96, Checkpoint: CheckpointMetadata{RelativePath: "checkpoints/video.json"}},
			{ID: "audio", Kind: "audio", ObservedBytes: 64, CommittedBytes: 64, Checkpoint: CheckpointMetadata{RelativePath: "checkpoints/audio.json"}},
		},
		Now: func() time.Time { return testNow },
	}
}

func createTestWorkspace(t *testing.T) (*Workspace, WorkspaceRef) {
	t.Helper()
	root := t.TempDir()
	workspace, err := Create(testCreateOptions(root))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = workspace.Close() })
	return workspace, workspace.Ref()
}

func requireClasses(t *testing.T, inspection Inspection, expected ...InspectionClass) {
	t.Helper()
	for _, want := range expected {
		found := false
		for _, actual := range inspection.Classifications {
			if actual == want {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("inspection classes = %v, missing %q", inspection.Classifications, want)
		}
	}
}

func TestCreateOpenInspectAndOwnerOnlyLayout(t *testing.T) {
	workspace, ref := createTestWorkspace(t)
	path := workspace.Path()
	if filepath.Base(path) != ref.SessionID || filepath.Base(filepath.Dir(path)) != SessionsDirectoryName {
		t.Fatalf("workspace path = %q, ref = %+v", path, ref)
	}
	for _, directory := range []string{filepath.Join(ref.OutputRoot, SessionsDirectoryName), path} {
		info, err := os.Stat(directory)
		if err != nil {
			t.Fatal(err)
		}
		if runtime.GOOS != "windows" && info.Mode().Perm() != 0o700 {
			t.Fatalf("directory %q mode = %o, want 700", directory, info.Mode().Perm())
		}
	}
	manifestPath := filepath.Join(path, ManifestFileName)
	manifestInfo, err := os.Stat(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && manifestInfo.Mode().Perm() != 0o600 {
		t.Fatalf("manifest mode = %o, want 600", manifestInfo.Mode().Perm())
	}
	manifest := workspace.Manifest()
	if manifest.SessionID != ref.SessionID {
		t.Fatalf("manifest session id = %q, ref = %q", manifest.SessionID, ref.SessionID)
	}
	if manifest.Output.PlanFingerprint != OutputPlanFingerprint(manifest.Output.PlanIdentity) {
		t.Fatalf("output fingerprint is not bound to plan identity: %+v", manifest.Output)
	}

	raw, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"secret-source", "secret-signature", "sig=", "cookie", "authorization", "password", "token"} {
		if strings.Contains(string(raw), secret) {
			t.Fatalf("manifest contains forbidden material %q: %s", secret, raw)
		}
	}
	if !strings.Contains(string(raw), "https://example.invalid/watch") {
		t.Fatalf("manifest lost canonical safe source identity: %s", raw)
	}

	inspection, err := Inspect(ref)
	if err != nil {
		t.Fatal(err)
	}
	if !inspection.HasManifest || inspection.Manifest.SessionID != ref.SessionID {
		t.Fatalf("inspection = %+v", inspection)
	}
	requireClasses(t, inspection, InspectionLeaseContended)
	if !inspection.LeaseContended {
		t.Fatal("same-process lease was not contended")
	}

	if err := workspace.Close(); err != nil {
		t.Fatal(err)
	}
	inspection, err = Inspect(ref)
	if err != nil {
		t.Fatal(err)
	}
	if inspection.Classification != InspectionAvailable {
		t.Fatalf("released inspection classification = %q, classes = %v", inspection.Classification, inspection.Classifications)
	}
	reopened, err := Open(ref)
	if err != nil {
		t.Fatal(err)
	}
	if got := reopened.Manifest().SessionID; got != ref.SessionID {
		t.Fatalf("reopened session id = %q", got)
	}
	if err := reopened.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestManifestAllowsExtractionWithoutDestinationButRequiresItBeforeDownload(t *testing.T) {
	root := t.TempDir()
	options := testCreateOptions(root)
	options.RelativeDestination = ""
	workspace, err := Create(options)
	if err != nil {
		t.Fatal(err)
	}
	defer workspace.Close()
	manifest := workspace.Manifest()
	if manifest.Phase != PhasePrepared || manifest.RelativeDestination != "" {
		t.Fatalf("initial manifest = %+v", manifest)
	}
	if err := workspace.Transition(manifest.Revision, manifest.RunGeneration, PhaseExtracting, StatusActive, DesiredRunning, testNow.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	manifest = workspace.Manifest()
	if err := workspace.Transition(manifest.Revision, manifest.RunGeneration, PhaseDownloading, StatusActive, DesiredRunning, testNow.Add(2*time.Minute)); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("empty destination transition error = %v, want invalid transition", err)
	}
	if err := workspace.Update(manifest.Revision, manifest.RunGeneration, func(candidate *Manifest) error {
		candidate.RelativeDestination = "downloads/video.mp4"
		candidate.UpdatedAt = testNow.Add(3 * time.Minute)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	manifest = workspace.Manifest()
	if err := workspace.Transition(manifest.Revision, manifest.RunGeneration, PhaseDownloading, StatusActive, DesiredRunning, testNow.Add(4*time.Minute)); err != nil {
		t.Fatal(err)
	}
}

func TestTransitionTableKeepsPhaseAcrossDispositionAndAllowsDirectPublish(t *testing.T) {
	cases := []struct {
		name string
		from LifecycleState
		to   LifecycleState
		want bool
	}{
		{"progress", LifecycleState{Phase: PhasePrepared, Status: StatusActive}, LifecycleState{Phase: PhaseExtracting, Status: StatusActive}, true},
		{"pause retains processing", LifecycleState{Phase: PhaseProcessing, Status: StatusActive}, LifecycleState{Phase: PhaseProcessing, Status: StatusPaused}, true},
		{"resume retains processing", LifecycleState{Phase: PhaseProcessing, Status: StatusPaused}, LifecycleState{Phase: PhaseProcessing, Status: StatusActive}, true},
		{"single track direct publish", LifecycleState{Phase: PhaseDownloading, Status: StatusActive}, LifecycleState{Phase: PhaseReadyToPublish, Status: StatusActive}, true},
		{"processing publish", LifecycleState{Phase: PhaseProcessing, Status: StatusActive}, LifecycleState{Phase: PhaseReadyToPublish, Status: StatusActive}, true},
		{"reconciliation cannot resume directly", LifecycleState{Phase: PhaseDownloading, Status: StatusNeedsReconciliation}, LifecycleState{Phase: PhaseDownloading, Status: StatusActive}, false},
		{"canceled cannot resume", LifecycleState{Phase: PhaseDownloading, Status: StatusCanceled}, LifecycleState{Phase: PhaseDownloading, Status: StatusActive}, false},
		{"completed requires terminal phase", LifecycleState{Phase: PhaseReadyToPublish, Status: StatusActive}, LifecycleState{Phase: PhaseCompleted, Status: StatusCompleted}, true},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			if got := CanTransition(test.from, test.to); got != test.want {
				t.Fatalf("CanTransition(%+v, %+v) = %v, want %v", test.from, test.to, got, test.want)
			}
		})
	}
}

func TestPausedProcessingResumeNormalizesToCommittedBytesAndNewGeneration(t *testing.T) {
	workspace, _ := createTestWorkspace(t)
	defer workspace.Close()
	manifest := workspace.Manifest()
	if err := workspace.Transition(manifest.Revision, manifest.RunGeneration, PhaseExtracting, StatusActive, DesiredRunning, testNow.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	manifest = workspace.Manifest()
	if err := workspace.Transition(manifest.Revision, manifest.RunGeneration, PhaseDownloading, StatusActive, DesiredRunning, testNow.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	manifest = workspace.Manifest()
	if err := workspace.Transition(manifest.Revision, manifest.RunGeneration, PhaseProcessing, StatusActive, DesiredRunning, testNow.Add(3*time.Minute)); err != nil {
		t.Fatal(err)
	}
	manifest = workspace.Manifest()
	if err := workspace.Transition(manifest.Revision, manifest.RunGeneration, PhaseProcessing, StatusPaused, DesiredPaused, testNow.Add(4*time.Minute)); err != nil {
		t.Fatal(err)
	}
	paused := workspace.Manifest()
	if paused.Phase != PhaseProcessing || paused.Status != StatusPaused {
		t.Fatalf("paused state = %+v", paused)
	}
	if err := workspace.Resume(paused.Revision, paused.RunGeneration, testNow.Add(5*time.Minute)); err != nil {
		t.Fatal(err)
	}
	resumed := workspace.Manifest()
	if resumed.Phase != PhaseProcessing || resumed.Status != StatusActive || resumed.Desired != DesiredRunning {
		t.Fatalf("resumed state = %+v", resumed)
	}
	if resumed.RunGeneration != paused.RunGeneration+1 || resumed.Revision != paused.Revision+1 {
		t.Fatalf("resumed revision/generation = %d/%d, paused = %d/%d", resumed.Revision, resumed.RunGeneration, paused.Revision, paused.RunGeneration)
	}
	for _, component := range resumed.Components {
		if component.ObservedBytes != component.CommittedBytes {
			t.Fatalf("component %q retained observed bytes beyond durable checkpoint: %+v", component.ID, component)
		}
	}
	boundaries := paused.ResumeBoundaries()
	if len(boundaries) != 2 || boundaries[0].ComponentID != "audio" || boundaries[1].ComponentID != "video" || boundaries[1].DurableBytes != 96 || boundaries[1].DiscardBytes != 32 {
		t.Fatalf("resume boundaries = %+v", boundaries)
	}
}

func TestReconciliationRequiresDedicatedResolution(t *testing.T) {
	workspace, _ := createTestWorkspace(t)
	defer workspace.Close()
	manifest := workspace.Manifest()
	if err := workspace.Transition(manifest.Revision, manifest.RunGeneration, PhaseExtracting, StatusActive, DesiredRunning, testNow.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	manifest = workspace.Manifest()
	if err := workspace.Transition(manifest.Revision, manifest.RunGeneration, PhaseExtracting, StatusNeedsReconciliation, DesiredPaused, testNow.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	needs := workspace.Manifest()
	if err := workspace.Resume(needs.Revision, needs.RunGeneration, testNow.Add(3*time.Minute)); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("resume from reconciliation error = %v", err)
	}
	if err := workspace.Transition(needs.Revision, needs.RunGeneration, PhaseExtracting, StatusActive, DesiredRunning, testNow.Add(3*time.Minute)); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("generic reconciliation transition error = %v", err)
	}
	if err := workspace.ResolveReconciliation(needs.Revision, needs.RunGeneration, StatusPaused, DesiredPaused, testNow.Add(4*time.Minute)); err != nil {
		t.Fatal(err)
	}
	resolved := workspace.Manifest()
	if resolved.Status != StatusPaused || resolved.Phase != PhaseExtracting {
		t.Fatalf("resolved state = %+v", resolved)
	}
}

func TestCompletedRequiresCommittedPublication(t *testing.T) {
	workspace, _ := createTestWorkspace(t)
	defer workspace.Close()
	manifest := workspace.Manifest()
	for _, phase := range []Phase{PhaseExtracting, PhaseDownloading, PhaseReadyToPublish} {
		manifest = workspace.Manifest()
		if err := workspace.Transition(manifest.Revision, manifest.RunGeneration, phase, StatusActive, DesiredRunning, testNow.Add(time.Duration(manifest.Revision)*time.Minute)); err != nil {
			t.Fatal(err)
		}
	}
	manifest = workspace.Manifest()
	if err := workspace.Transition(manifest.Revision, manifest.RunGeneration, PhaseCompleted, StatusCompleted, DesiredRunning, testNow.Add(10*time.Minute)); !errors.Is(err, ErrInvalidManifest) && !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("completion without publication error = %v", err)
	}
	if err := workspace.SetPublicationState(manifest.Revision, manifest.RunGeneration, PublicationPending, testNow.Add(11*time.Minute)); err != nil {
		t.Fatal(err)
	}
	manifest = workspace.Manifest()
	if err := workspace.SetPublicationState(manifest.Revision, manifest.RunGeneration, PublicationCommitted, testNow.Add(12*time.Minute)); err != nil {
		t.Fatal(err)
	}
	manifest = workspace.Manifest()
	if err := workspace.Transition(manifest.Revision, manifest.RunGeneration, PhaseCompleted, StatusCompleted, DesiredRunning, testNow.Add(13*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if got := workspace.Manifest().Status; got != StatusCompleted {
		t.Fatalf("completed status = %q", got)
	}
}

func TestManifestCrossFieldAndIdentityValidation(t *testing.T) {
	workspace, _ := createTestWorkspace(t)
	defer workspace.Close()
	base := workspace.Manifest()
	cases := []struct {
		name   string
		mutate func(*Manifest)
	}{
		{name: "completed without committed publication", mutate: func(manifest *Manifest) {
			manifest.Phase = PhaseCompleted
			manifest.Status = StatusCompleted
			manifest.Publication = PublicationPending
		}},
		{name: "pending publication before ready", mutate: func(manifest *Manifest) {
			manifest.Publication = PublicationPending
		}},
		{name: "indeterminate publication without reconciliation", mutate: func(manifest *Manifest) {
			manifest.Publication = PublicationIndeterminate
		}},
		{name: "missing provider", mutate: func(manifest *Manifest) {
			manifest.Source.Provider = ""
		}},
		{name: "missing component id", mutate: func(manifest *Manifest) {
			manifest.Components[0].ID = ""
		}},
		{name: "transition target mismatch", mutate: func(manifest *Manifest) {
			manifest.LastTransition = TransitionRecord{FromPhase: PhasePrepared, FromStatus: StatusActive, ToPhase: PhaseExtracting, ToStatus: StatusActive, At: testNow}
		}},
		{name: "transition before creation", mutate: func(manifest *Manifest) {
			manifest.LastTransition = TransitionRecord{FromPhase: PhasePrepared, FromStatus: StatusActive, ToPhase: PhasePrepared, ToStatus: StatusActive, At: testNow.Add(-time.Minute)}
		}},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			candidate := base.Clone()
			test.mutate(&candidate)
			if err := candidate.Validate(); err == nil {
				t.Fatal("invalid cross-field manifest was accepted")
			}
		})
	}
}

func TestPublicationAndCleanupStateAreMonotonic(t *testing.T) {
	workspace, _ := createTestWorkspace(t)
	defer workspace.Close()
	manifest := workspace.Manifest()
	if err := workspace.SetPublicationState(manifest.Revision, manifest.RunGeneration, PublicationPending, testNow.Add(time.Minute)); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("pending during prepared error = %v", err)
	}
	if err := workspace.SetPublicationState(manifest.Revision, manifest.RunGeneration, PublicationCommitted, testNow.Add(time.Minute)); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("committed during prepared error = %v", err)
	}
	if err := workspace.SetCleanupState(manifest.Revision, manifest.RunGeneration, CleanupComplete, testNow.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	manifest = workspace.Manifest()
	if err := workspace.SetCleanupState(manifest.Revision, manifest.RunGeneration, CleanupPending, testNow.Add(2*time.Minute)); !errors.Is(err, ErrInvalidManifest) && !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("cleanup rollback error = %v", err)
	}

	for _, phase := range []Phase{PhaseExtracting, PhaseDownloading, PhaseReadyToPublish} {
		manifest = workspace.Manifest()
		if err := workspace.Transition(manifest.Revision, manifest.RunGeneration, phase, StatusActive, DesiredRunning, testNow.Add(time.Duration(manifest.Revision+1)*time.Minute)); err != nil {
			t.Fatal(err)
		}
	}
	manifest = workspace.Manifest()
	if err := workspace.SetPublicationState(manifest.Revision, manifest.RunGeneration, PublicationPending, testNow.Add(10*time.Minute)); err != nil {
		t.Fatal(err)
	}
	manifest = workspace.Manifest()
	if err := workspace.SetPublicationState(manifest.Revision, manifest.RunGeneration, PublicationCommitted, testNow.Add(11*time.Minute)); err != nil {
		t.Fatal(err)
	}
	manifest = workspace.Manifest()
	if err := workspace.SetPublicationState(manifest.Revision, manifest.RunGeneration, PublicationNotStarted, testNow.Add(12*time.Minute)); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("publication rollback error = %v", err)
	}
	if err := workspace.SetPublicationState(manifest.Revision, manifest.RunGeneration, PublicationIndeterminate, testNow.Add(12*time.Minute)); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("publication fast-forward error = %v", err)
	}

	// A pending publication can become indeterminate, but only the dedicated
	// resolution API can establish committed authority afterward.
	workspace2, _ := createTestWorkspace(t)
	defer workspace2.Close()
	manifest = workspace2.Manifest()
	for _, phase := range []Phase{PhaseExtracting, PhaseDownloading, PhaseReadyToPublish} {
		manifest = workspace2.Manifest()
		if err := workspace2.Transition(manifest.Revision, manifest.RunGeneration, phase, StatusActive, DesiredRunning, testNow.Add(time.Duration(manifest.Revision+20)*time.Minute)); err != nil {
			t.Fatal(err)
		}
	}
	manifest = workspace2.Manifest()
	if err := workspace2.SetPublicationState(manifest.Revision, manifest.RunGeneration, PublicationPending, testNow.Add(30*time.Minute)); err != nil {
		t.Fatal(err)
	}
	manifest = workspace2.Manifest()
	if err := workspace2.SetPublicationState(manifest.Revision, manifest.RunGeneration, PublicationIndeterminate, testNow.Add(31*time.Minute)); err != nil {
		t.Fatal(err)
	}
	needs := workspace2.Manifest()
	if err := workspace2.SetPublicationState(needs.Revision, needs.RunGeneration, PublicationCommitted, testNow.Add(32*time.Minute)); !errors.Is(err, ErrNeedsReconciliation) && !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("generic indeterminate resolution error = %v", err)
	}
	if err := workspace2.ResolvePublication(needs.Revision, needs.RunGeneration, PublicationCommitted, testNow.Add(32*time.Minute)); err != nil {
		t.Fatal(err)
	}
	resolved := workspace2.Manifest()
	if resolved.Publication != PublicationCommitted || resolved.Status != StatusNeedsReconciliation {
		t.Fatalf("resolved publication = %+v", resolved)
	}
	if err := workspace2.ResolveReconciliation(resolved.Revision, resolved.RunGeneration, StatusCompleted, DesiredRunning, testNow.Add(33*time.Minute)); err != nil {
		t.Fatal(err)
	}
}

func TestStaleRevisionAndGenerationMutationsAreRejected(t *testing.T) {
	workspace, _ := createTestWorkspace(t)
	defer workspace.Close()
	initial := workspace.Manifest()
	if err := workspace.Transition(initial.Revision, initial.RunGeneration, PhaseExtracting, StatusActive, DesiredRunning, testNow.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	current := workspace.Manifest()
	if err := workspace.Update(initial.Revision, initial.RunGeneration, func(candidate *Manifest) error {
		candidate.Desired = DesiredPaused
		return nil
	}); !errors.Is(err, ErrStaleMutation) {
		t.Fatalf("stale revision error = %v", err)
	}
	if err := workspace.Transition(current.Revision, current.RunGeneration, PhaseExtracting, StatusPaused, DesiredPaused, testNow.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	paused := workspace.Manifest()
	if err := workspace.Resume(paused.Revision, paused.RunGeneration-1, testNow.Add(3*time.Minute)); !errors.Is(err, ErrStaleMutation) {
		t.Fatalf("stale generation error = %v", err)
	}
	if err := workspace.Resume(paused.Revision, paused.RunGeneration, testNow.Add(4*time.Minute)); err != nil {
		t.Fatal(err)
	}
	resumed := workspace.Manifest()
	if err := workspace.Update(paused.Revision, paused.RunGeneration, func(candidate *Manifest) error {
		candidate.Desired = DesiredCanceled
		return nil
	}); !errors.Is(err, ErrStaleMutation) {
		t.Fatalf("stale pre-resume worker error = %v", err)
	}
	if resumed.RunGeneration != paused.RunGeneration+1 {
		t.Fatalf("run generation did not advance: %+v", resumed)
	}
}

func TestConcurrentExpectedRevisionAllowsOnlyOneMutation(t *testing.T) {
	workspace, _ := createTestWorkspace(t)
	defer workspace.Close()
	manifest := workspace.Manifest()
	const workers = 16
	results := make(chan error, workers)
	var wait sync.WaitGroup
	for index := 0; index < workers; index++ {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			results <- workspace.Update(manifest.Revision, manifest.RunGeneration, func(candidate *Manifest) error {
				candidate.Desired = DesiredRunning
				candidate.UpdatedAt = testNow.Add(time.Duration(index+1) * time.Second)
				return nil
			})
		}(index)
	}
	wait.Wait()
	close(results)
	successes := 0
	stale := 0
	for err := range results {
		if err == nil {
			successes++
		} else if errors.Is(err, ErrStaleMutation) {
			stale++
		}
	}
	if successes != 1 || stale != workers-1 {
		t.Fatalf("successes = %d, stale = %d", successes, stale)
	}
}

type injectedCommitError struct {
	committed     bool
	indeterminate bool
	err           error
}

func (err injectedCommitError) Error() string { return err.err.Error() }

func (err injectedCommitError) Unwrap() error { return err.err }

func (err injectedCommitError) Committed() bool { return err.committed }

func (err injectedCommitError) Indeterminate() bool { return err.indeterminate }

func encodeManifestForTest(t *testing.T, encode func(io.Writer) error) []byte {
	t.Helper()
	var buffer bytes.Buffer
	if err := encode(&buffer); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}

func TestManifestAtomicCommitOutcomes(t *testing.T) {
	tests := []struct {
		name          string
		committed     bool
		indeterminate bool
	}{
		{name: "confirmed precommit", committed: false},
		{name: "committed durability error", committed: true},
		{name: "indeterminate", indeterminate: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			workspace, _ := createTestWorkspace(t)
			defer workspace.Close()
			before := workspace.Manifest()
			original := atomicManifestWrite
			t.Cleanup(func() { atomicManifestWrite = original })
			secret := errors.New("injected-secret-token")
			atomicManifestWrite = func(path string, mode fs.FileMode, encode func(io.Writer) error) error {
				encoded := encodeManifestForTest(t, encode)
				if test.committed {
					if err := os.WriteFile(path, encoded, mode); err != nil {
						t.Fatal(err)
					}
				}
				if test.indeterminate {
					if err := os.WriteFile(filepath.Join(filepath.Dir(path), ".atomic-injected"), encoded, 0o600); err != nil {
						t.Fatal(err)
					}
				}
				return injectedCommitError{committed: test.committed, indeterminate: test.indeterminate, err: secret}
			}
			err := workspace.Transition(before.Revision, before.RunGeneration, PhaseExtracting, StatusActive, DesiredRunning, testNow.Add(time.Minute))
			if err == nil {
				t.Fatal("injected commit error was lost")
			}
			var outcome atomicfile.CommitError
			if !errors.As(err, &outcome) || outcome.Committed() != test.committed || outcome.Indeterminate() != test.indeterminate {
				t.Fatalf("error = %T %v, outcome = %v/%v", err, err, outcome.Committed(), outcome.Indeterminate())
			}
			if strings.Contains(err.Error(), "injected-secret-token") {
				t.Fatalf("secret leaked through commit error: %v", err)
			}
			if test.committed {
				if got := workspace.Manifest().Phase; got != PhaseExtracting {
					t.Fatalf("committed in-memory phase = %q", got)
				}
			} else if got := workspace.Manifest().Phase; got != before.Phase {
				t.Fatalf("precommit/indeterminate in-memory phase = %q, want %q", got, before.Phase)
			}
			if test.indeterminate {
				if err := workspace.Update(before.Revision, before.RunGeneration, func(candidate *Manifest) error { return nil }); !errors.Is(err, ErrNeedsReconciliation) {
					t.Fatalf("post-indeterminate mutation error = %v", err)
				}
				if inspection, inspectErr := Inspect(workspace.Ref()); inspectErr != nil {
					t.Fatal(inspectErr)
				} else {
					requireClasses(t, inspection, InspectionManifestIndeterminate)
				}
			}
		})
	}
}

func TestCreatePreservesRecoverableRefForCommittedAndIndeterminateOutcomes(t *testing.T) {
	for _, test := range []struct {
		name          string
		committed     bool
		indeterminate bool
	}{
		{name: "committed", committed: true},
		{name: "indeterminate", indeterminate: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			original := atomicManifestWrite
			t.Cleanup(func() { atomicManifestWrite = original })
			atomicManifestWrite = func(path string, mode fs.FileMode, encode func(io.Writer) error) error {
				encoded := encodeManifestForTest(t, encode)
				if test.committed {
					if err := os.WriteFile(path, encoded, mode); err != nil {
						t.Fatal(err)
					}
				}
				if test.indeterminate {
					if err := os.WriteFile(filepath.Join(filepath.Dir(path), ".atomic-create-evidence"), encoded, 0o600); err != nil {
						t.Fatal(err)
					}
				}
				return injectedCommitError{committed: test.committed, indeterminate: test.indeterminate, err: errors.New("create-secret")}
			}
			workspace, err := Create(testCreateOptions(t.TempDir()))
			if err == nil {
				t.Fatal("injected create outcome was lost")
			}
			if workspace == nil {
				t.Fatal("create dropped recoverable workspace handle")
			}
			if workspace.Ref().SessionID == "" || workspace.Path() == "" {
				t.Fatalf("recoverable workspace = %+v", workspace.Ref())
			}
			var outcome atomicfile.CommitError
			if !errors.As(err, &outcome) || outcome.Committed() != test.committed || outcome.Indeterminate() != test.indeterminate {
				t.Fatalf("create error = %v, outcome = %v/%v", err, outcome.Committed(), outcome.Indeterminate())
			}
			if test.indeterminate {
				if err := workspace.Update(1, 1, func(candidate *Manifest) error { return nil }); !errors.Is(err, ErrNeedsReconciliation) {
					t.Fatalf("indeterminate create allowed mutation: %v", err)
				}
			}
			_ = workspace.Close()
		})
	}
}

func TestConfirmedPrecommitCleansNewWorkspace(t *testing.T) {
	original := atomicManifestWrite
	t.Cleanup(func() { atomicManifestWrite = original })
	atomicManifestWrite = func(string, fs.FileMode, func(io.Writer) error) error {
		return injectedCommitError{err: errors.New("precommit")}
	}
	root := t.TempDir()
	if workspace, err := Create(testCreateOptions(root)); workspace != nil || err == nil {
		t.Fatalf("precommit create result = workspace %v, err %v", workspace, err)
	}
	entries, err := os.ReadDir(filepath.Join(root, SessionsDirectoryName))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("confirmed precommit left session directories: %v", entries)
	}
}

func TestPrecommitCleanupFailurePreservesRecoverableRefAndEvidence(t *testing.T) {
	original := atomicManifestWrite
	t.Cleanup(func() { atomicManifestWrite = original })
	atomicManifestWrite = func(path string, mode fs.FileMode, encode func(io.Writer) error) error {
		encoded := encodeManifestForTest(t, encode)
		if err := os.WriteFile(filepath.Join(filepath.Dir(path), ".atomic-retained"), encoded, 0o600); err != nil {
			t.Fatal(err)
		}
		return injectedCommitError{err: errors.New("precommit-cleanup-evidence")}
	}
	workspace, err := Create(testCreateOptions(t.TempDir()))
	if err == nil || workspace == nil {
		t.Fatalf("cleanup failure result = workspace %v, err %v", workspace, err)
	}
	if workspace.Ref().SessionID == "" {
		t.Fatal("cleanup failure lost recoverable reference")
	}
	inspection, inspectErr := Inspect(workspace.Ref())
	if inspectErr != nil {
		t.Fatal(inspectErr)
	}
	requireClasses(t, inspection, InspectionManifestIndeterminate)
	if _, statErr := os.Stat(filepath.Join(workspace.Path(), ".atomic-retained")); statErr != nil {
		t.Fatalf("retained evidence was removed: %v", statErr)
	}
}

func TestOpenAndInspectFailClosedForMissingLeaseAndAtomicEvidence(t *testing.T) {
	workspace, ref := createTestWorkspace(t)
	if err := workspace.Close(); err != nil {
		t.Fatal(err)
	}
	leasePath := filepath.Join(workspace.Path(), LeaseFileName)
	if err := os.Remove(leasePath); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(ref); !errors.Is(err, ErrMissingLease) {
		t.Fatalf("Open missing lease error = %v", err)
	}
	if _, err := os.Lstat(leasePath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Open recreated missing lease: %v", err)
	}
	inspection, err := Inspect(ref)
	if err != nil {
		t.Fatal(err)
	}
	requireClasses(t, inspection, InspectionMissingLease, InspectionNeedsReconciliation)

	workspace, ref = createTestWorkspace(t)
	if err := workspace.Close(); err != nil {
		t.Fatal(err)
	}
	evidence := filepath.Join(workspace.Path(), ".atomic-backup-evidence")
	if err := os.WriteFile(evidence, []byte("candidate"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(ref); !errors.Is(err, ErrNeedsReconciliation) {
		t.Fatalf("Open atomic evidence error = %v", err)
	}
	inspection, err = Inspect(ref)
	if err != nil {
		t.Fatal(err)
	}
	requireClasses(t, inspection, InspectionManifestIndeterminate)
}

func TestCorruptTruncatedUnknownVersionAndNonRegularManifests(t *testing.T) {
	tests := []struct {
		name      string
		configure func(*testing.T, string)
		wantClass InspectionClass
	}{
		{name: "truncated", configure: func(t *testing.T, path string) {
			if err := os.WriteFile(path, []byte(`{"version":1`), 0o600); err != nil {
				t.Fatal(err)
			}
		}, wantClass: InspectionCorruptManifest},
		{name: "unknown version", configure: func(t *testing.T, path string) {
			var value map[string]any
			body, _ := os.ReadFile(path)
			if err := json.Unmarshal(body, &value); err != nil {
				t.Fatal(err)
			}
			value["version"] = float64(99)
			encoded, _ := json.Marshal(value)
			if err := os.WriteFile(path, encoded, 0o600); err != nil {
				t.Fatal(err)
			}
		}, wantClass: InspectionUnknownManifestVersion},
		{name: "directory", configure: func(t *testing.T, path string) {
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(path, 0o700); err != nil {
				t.Fatal(err)
			}
		}, wantClass: InspectionUnsafePath},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			workspace, ref := createTestWorkspace(t)
			path := filepath.Join(workspace.Path(), ManifestFileName)
			if err := workspace.Close(); err != nil {
				t.Fatal(err)
			}
			test.configure(t, path)
			inspection, err := Inspect(ref)
			if err != nil {
				t.Fatal(err)
			}
			requireClasses(t, inspection, test.wantClass)
			if _, err := Open(ref); err == nil {
				t.Fatal("Open accepted corrupt manifest")
			}
		})
	}
}

func TestManifestIdentityAndDerivedPathValidation(t *testing.T) {
	workspace, ref := createTestWorkspace(t)
	path := filepath.Join(workspace.Path(), ManifestFileName)
	if err := workspace.Close(); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var value map[string]any
	if err := json.Unmarshal(body, &value); err != nil {
		t.Fatal(err)
	}
	value["session_id"] = "00000000000000000000000000000000"
	encoded, _ := json.Marshal(value)
	if err := os.WriteFile(path, encoded, 0o600); err != nil {
		t.Fatal(err)
	}
	inspection, err := Inspect(ref)
	if err != nil {
		t.Fatal(err)
	}
	requireClasses(t, inspection, InspectionCorruptManifest)

	// Recreate a valid session to exercise output-root and checkpoint checks.
	workspace, ref = createTestWorkspace(t)
	if err := workspace.Close(); err != nil {
		t.Fatal(err)
	}
	path = filepath.Join(workspace.Path(), ManifestFileName)
	body, _ = os.ReadFile(path)
	if err := json.Unmarshal(body, &value); err != nil {
		t.Fatal(err)
	}
	value["relative_destination"] = "../outside.mp4"
	encoded, _ = json.Marshal(value)
	if err := os.WriteFile(path, encoded, 0o600); err != nil {
		t.Fatal(err)
	}
	inspection, err = Inspect(ref)
	if err != nil {
		t.Fatal(err)
	}
	requireClasses(t, inspection, InspectionCorruptManifest)
}

func TestPathAndSymlinkRejection(t *testing.T) {
	for _, relative := range []string{"../escape.mp4", "/absolute.mp4", "a/../escape.mp4", "C:/absolute.mp4"} {
		t.Run(relative, func(t *testing.T) {
			options := testCreateOptions(t.TempDir())
			options.RelativeDestination = relative
			if _, err := Create(options); err == nil {
				t.Fatalf("destination %q was accepted", relative)
			}
		})
	}
	root := t.TempDir()
	target := t.TempDir()
	symlinkRoot := filepath.Join(root, "root-link")
	if err := os.Symlink(target, symlinkRoot); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	options := testCreateOptions(symlinkRoot)
	if _, err := Create(options); !errors.Is(err, ErrUnsafePath) {
		t.Fatalf("symlinked output root error = %v", err)
	}

	root = t.TempDir()
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, SessionsDirectoryName)); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if _, err := Create(testCreateOptions(root)); !errors.Is(err, ErrUnsafePath) {
		t.Fatalf("symlinked sessions root error = %v", err)
	}

	workspace, ref := createTestWorkspace(t)
	if err := workspace.Close(); err != nil {
		t.Fatal(err)
	}
	workspacePath := workspace.Path()
	if err := os.RemoveAll(workspacePath); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, workspacePath); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if _, err := Open(ref); !errors.Is(err, ErrUnsafePath) {
		t.Fatalf("symlinked workspace error = %v", err)
	}
	if inspection, err := Inspect(ref); err != nil {
		t.Fatal(err)
	} else {
		requireClasses(t, inspection, InspectionUnsafePath)
	}
}

func TestCheckpointSymlinkRejection(t *testing.T) {
	workspace, ref := createTestWorkspace(t)
	if err := workspace.Close(); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "outside-checkpoint")
	if err := os.WriteFile(outside, []byte("safe"), 0o600); err != nil {
		t.Fatal(err)
	}
	checkpoint := filepath.Join(workspace.Path(), "checkpoints", "video.json")
	if err := os.MkdirAll(filepath.Dir(checkpoint), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, checkpoint); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	inspection, err := Inspect(ref)
	if err != nil {
		t.Fatal(err)
	}
	requireClasses(t, inspection, InspectionUnsafePath)
	if _, err := Open(ref); err == nil {
		t.Fatal("Open accepted symlinked checkpoint")
	}
	data, err := os.ReadFile(outside)
	if err != nil || string(data) != "safe" {
		t.Fatalf("outside checkpoint changed: %q, %v", data, err)
	}
}

func TestOutputIntentRejectsSecretBearingPlanIdentity(t *testing.T) {
	options := testCreateOptions(t.TempDir())
	options.Output.PlanIdentity = "https://cdn.invalid/file?token=secret-output-token"
	if _, err := Create(options); err == nil {
		t.Fatal("secret-bearing output plan was accepted")
	} else if strings.Contains(err.Error(), "secret-output-token") {
		t.Fatalf("secret leaked in output validation error: %v", err)
	}
}

func TestHelperProcessLeaseContentionAndRelease(t *testing.T) {
	if os.Getenv("YTDLP_SESSION_LEASE_HELPER") == "1" {
		var ref WorkspaceRef
		if err := json.Unmarshal([]byte(os.Getenv("YTDLP_SESSION_LEASE_REF")), &ref); err != nil {
			os.Exit(2)
		}
		workspace, err := Open(ref)
		if err != nil {
			os.Exit(3)
		}
		fmt.Fprintln(os.Stdout, "ready")
		_, _ = bufio.NewReader(os.Stdin).ReadString('\n')
		_ = workspace.Close()
		os.Exit(0)
	}
	workspace, ref := createTestWorkspace(t)
	if err := workspace.Close(); err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(ref)
	command := exec.Command(os.Args[0], "-test.run=TestHelperProcessLeaseContentionAndRelease", "--")
	command.Env = append(os.Environ(), "YTDLP_SESSION_LEASE_HELPER=1", "YTDLP_SESSION_LEASE_REF="+string(encoded))
	stdout, err := command.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdin, err := command.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	scanner := bufio.NewScanner(stdout)
	if !scanner.Scan() || scanner.Text() != "ready" {
		_ = command.Process.Kill()
		t.Fatalf("helper did not become ready: %q", scanner.Text())
	}
	if _, err := Open(ref); !errors.Is(err, ErrLeaseContended) {
		t.Fatalf("helper contention error = %v", err)
	}
	if _, err := stdin.Write([]byte("release\n")); err != nil {
		t.Fatal(err)
	}
	if err := stdin.Close(); err != nil {
		t.Fatal(err)
	}
	if err := command.Wait(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(ref)
	if err != nil {
		t.Fatal(err)
	}
	if err := reopened.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestNoSecretInErrorsForInvalidSource(t *testing.T) {
	options := testCreateOptions(t.TempDir())
	options.Source.CanonicalURL = "https://cdn.invalid/file?authorization=secret-auth-header"
	options.Source.Provider = "fixture"
	options.Source.ID = "video-001"
	// Query material is stripped, not rejected, and therefore cannot reach the
	// manifest or an error string.
	workspace, err := Create(options)
	if err != nil {
		t.Fatal(err)
	}
	defer workspace.Close()
	raw, err := os.ReadFile(filepath.Join(workspace.Path(), ManifestFileName))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte("secret-auth-header")) {
		t.Fatalf("secret persisted: %s", raw)
	}
}
