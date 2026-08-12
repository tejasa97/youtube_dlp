package session

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"time"

	"github.com/tejasa97/youtube_dlp/internal/atomicfile"
)

// atomicManifestWrite is a narrow seam for commit-outcome tests. Production
// persistence always goes through PR #240's atomicfile primitive.
var atomicManifestWrite = atomicfile.Write

// Workspace owns an exclusive cross-process lease for the lifetime of the
// handle. It is intentionally not part of WorkspaceRef and cannot be encoded
// into the portable reference.
type Workspace struct {
	mu                   sync.Mutex
	ref                  WorkspaceRef
	path                 string
	workspaceIdentity    string
	lease                *workspaceLease
	manifest             Manifest
	closed               bool
	reconciliationNeeded bool
}

// Create creates an owner-only hidden workspace and its initial manifest. The
// returned handle owns the workspace lease and must be closed by the caller.
func Create(options CreateOptions) (*Workspace, error) {
	return create(options, "")
}

// CreateWithID creates a workspace using a caller-owned session ID. The ID is
// never generated or persisted by the engine on behalf of the caller. If the
// workspace already exists, callers should open it instead.
func CreateWithID(options CreateOptions, sessionID string) (*Workspace, error) {
	if !validSessionID(sessionID) {
		return nil, ErrInvalidReference
	}
	return create(options, sessionID)
}

func create(options CreateOptions, requestedSessionID string) (*Workspace, error) {
	root, err := canonicalOutputRoot(options.OutputRoot)
	if err != nil {
		return nil, ErrUnsafePath
	}
	if err := ensureOutputRoot(root); err != nil {
		return nil, err
	}
	sessionsRoot := filepath.Join(root, SessionsDirectoryName)
	if err := ensureDirectory(sessionsRoot, 0o700, true); err != nil {
		return nil, err
	}
	source, err := normalizeSource(options.Source)
	if err != nil {
		return nil, err
	}
	output, err := normalizeOutputIntent(options.Output)
	if err != nil {
		return nil, err
	}
	components, err := normalizeComponents(options.Components)
	if err != nil {
		return nil, err
	}
	if options.RelativeDestination != "" {
		if err := validateDestination(root, options.RelativeDestination); err != nil {
			return nil, err
		}
	}
	now := time.Now().UTC()
	if options.Now != nil {
		now = options.Now().UTC()
	}
	if now.IsZero() {
		return nil, ErrInvalidManifest
	}

	for attempt := 0; attempt < 16; attempt++ {
		sessionID := requestedSessionID
		if sessionID == "" {
			var randomErr error
			sessionID, randomErr = randomSessionID()
			if randomErr != nil {
				return nil, ErrWorkspaceUnavailable
			}
		}
		ref := WorkspaceRef{OutputRoot: root, OutputRootIdentity: options.OutputRootIdentity, SessionID: sessionID}
		if options.OutputRootIdentity != "" {
			if err := validateWorkspaceRootIdentity(ref); err != nil {
				return nil, err
			}
		}
		workspacePath, pathErr := ref.Path()
		if pathErr != nil {
			return nil, ErrInvalidReference
		}
		if mkdirErr := os.Mkdir(workspacePath, 0o700); mkdirErr != nil {
			if errors.Is(mkdirErr, os.ErrExist) {
				if requestedSessionID != "" {
					return nil, ErrWorkspaceUnavailable
				}
				continue
			}
			return nil, ErrWorkspaceUnavailable
		}
		if err := secureDirectoryPath(workspacePath); err != nil {
			return recoverCreateFailure(ref, workspacePath, nil, Manifest{}, err)
		}
		if err := validateDirectory(workspacePath, true); err != nil {
			return recoverCreateFailure(ref, workspacePath, nil, Manifest{}, err)
		}
		workspaceIdentity, identityErr := directoryIdentity(workspacePath)
		if identityErr != nil {
			return recoverCreateFailure(ref, workspacePath, nil, Manifest{}, identityErr)
		}
		leasePath, _ := ref.leasePath()
		lease, leaseErr := acquireWorkspaceLease(leasePath, true, true)
		if leaseErr != nil {
			if errors.Is(leaseErr, ErrLeaseContended) {
				return recoverableCreateHandle(ref, workspacePath, Manifest{}), leaseErr
			}
			return recoverCreateFailure(ref, workspacePath, nil, Manifest{}, leaseErr)
		}
		manifest := Manifest{
			Version:             ManifestSchemaVersion,
			SessionID:           sessionID,
			Revision:            1,
			RunGeneration:       1,
			Source:              source,
			Output:              output,
			RelativeDestination: options.RelativeDestination,
			Phase:               PhasePrepared,
			Status:              StatusActive,
			Desired:             DesiredRunning,
			Components:          components,
			CreatedAt:           now,
			UpdatedAt:           now,
			Publication:         PublicationNotStarted,
			Cleanup:             CleanupPending,
		}
		if err := manifest.Validate(); err != nil {
			return recoverCreateFailure(ref, workspacePath, lease, manifest, err)
		}
		if err := validateManifestDerivedPaths(root, workspacePath, manifest); err != nil {
			return recoverCreateFailure(ref, workspacePath, lease, manifest, err)
		}
		if err := persistManifest(filepath.Join(workspacePath, ManifestFileName), manifest); err != nil {
			var outcome atomicfile.CommitError
			if errors.As(err, &outcome) && (outcome.Committed() || outcome.Indeterminate()) {
				return &Workspace{
					ref:                  ref,
					path:                 workspacePath,
					workspaceIdentity:    workspaceIdentity,
					lease:                lease,
					manifest:             manifest,
					reconciliationNeeded: outcome.Indeterminate(),
				}, err
			}
			return recoverCreateFailure(ref, workspacePath, lease, manifest, err)
		}
		return &Workspace{ref: ref, path: workspacePath, workspaceIdentity: workspaceIdentity, lease: lease, manifest: manifest}, nil
	}
	return nil, ErrWorkspaceUnavailable
}

// Open validates an existing reference, acquires its lease, and loads its
// manifest. It never performs network work or destructive recovery.
func Open(ref WorkspaceRef) (*Workspace, error) {
	workspacePath, err := validateExistingWorkspace(ref)
	if err != nil {
		return nil, err
	}
	guardPath := filepath.Join(filepath.Dir(workspacePath), discardGuardPrefix+ref.SessionID)
	guardExists, guardErr := safeDirectoryExists(guardPath)
	if guardErr != nil {
		return nil, ErrNeedsReconciliation
	}
	if guardExists {
		return nil, ErrNeedsReconciliation
	}
	leasePath, err := ref.leasePath()
	if err != nil {
		return nil, ErrInvalidReference
	}
	lease, err := acquireWorkspaceLease(leasePath, false, true)
	if err != nil {
		return nil, err
	}
	if hasAtomicManifestEvidence(workspacePath) {
		_ = lease.Close()
		return nil, ErrNeedsReconciliation
	}
	_, marker, _ := readDiscardRecord(filepath.Join(workspacePath, discardMarkerName))
	if marker {
		_ = lease.Close()
		return nil, ErrNeedsReconciliation
	}
	manifest, err := readManifest(filepath.Join(workspacePath, ManifestFileName))
	if err != nil {
		_ = lease.Close()
		return nil, err
	}
	if manifest.SessionID != ref.SessionID {
		_ = lease.Close()
		return nil, ErrCorruptManifest
	}
	if err := validateManifestDerivedPaths(ref.OutputRoot, workspacePath, manifest); err != nil {
		_ = lease.Close()
		return nil, err
	}
	workspaceIdentity, identityErr := directoryIdentity(workspacePath)
	if identityErr != nil {
		_ = lease.Close()
		return nil, identityErr
	}
	return &Workspace{ref: ref, path: workspacePath, workspaceIdentity: workspaceIdentity, lease: lease, manifest: manifest}, nil
}

func recoverableCreateHandle(ref WorkspaceRef, workspacePath string, manifest Manifest) *Workspace {
	return &Workspace{
		ref:                  ref,
		path:                 workspacePath,
		manifest:             manifest,
		closed:               true,
		reconciliationNeeded: true,
	}
}

func recoverCreateFailure(ref WorkspaceRef, workspacePath string, lease *workspaceLease, manifest Manifest, cause error) (*Workspace, error) {
	if lease != nil {
		if err := lease.Close(); err != nil {
			return recoverableCreateHandle(ref, workspacePath, manifest), cause
		}
	}
	if cleanupCreatedWorkspace(workspacePath) {
		return nil, cause
	}
	return recoverableCreateHandle(ref, workspacePath, manifest), cause
}

// Manifest returns a copy of the last known manifest image.
func (workspace *Workspace) Manifest() Manifest {
	if workspace == nil {
		return Manifest{}
	}
	workspace.mu.Lock()
	defer workspace.mu.Unlock()
	return workspace.manifest.Clone()
}

// Snapshot is the error-reporting form of Manifest for callers that need to
// distinguish a closed handle.
func (workspace *Workspace) Snapshot() (Manifest, error) {
	if workspace == nil {
		return Manifest{}, ErrWorkspaceClosed
	}
	workspace.mu.Lock()
	defer workspace.mu.Unlock()
	if workspace.closed {
		return Manifest{}, ErrWorkspaceClosed
	}
	return workspace.manifest.Clone(), nil
}

// Ref returns the portable reference for this workspace.
func (workspace *Workspace) Ref() WorkspaceRef {
	if workspace == nil {
		return WorkspaceRef{}
	}
	return workspace.ref
}

// Path returns the validated on-disk workspace path.
func (workspace *Workspace) Path() string {
	if workspace == nil {
		return ""
	}
	return workspace.path
}

// Close releases the native lease. It does not remove the workspace or any
// evidence, including indeterminate atomic candidates.
func (workspace *Workspace) Close() error {
	if workspace == nil {
		return nil
	}
	workspace.mu.Lock()
	if workspace.closed {
		workspace.mu.Unlock()
		return nil
	}
	workspace.closed = true
	lease := workspace.lease
	workspace.mu.Unlock()
	if lease == nil {
		return nil
	}
	return lease.Close()
}

type mutationKind uint8

const (
	mutationTransition mutationKind = iota
	mutationResume
	mutationReconciliation
	mutationDesired
	mutationDestination
	mutationComponentProgress
	mutationComponentCheckpoint
	mutationComponentReset
	mutationStagedOutput
	mutationDestinationRebind
	mutationPublication
	mutationPublicationResolution
	mutationCleanup
	mutationCleanupResolution
)

type mutationAuthority struct {
	kind        mutationKind
	componentID string
}

// update is deliberately unexported. Every caller supplies a narrow authority
// whose exact field projection is checked after the callback; adding a new
// mutation API therefore cannot accidentally inherit unrestricted manifest
// write authority.
func (workspace *Workspace) update(expectedRevision, expectedGeneration uint64, authority mutationAuthority, mutate func(*Manifest) error) error {
	if workspace == nil {
		return ErrWorkspaceClosed
	}
	workspace.mu.Lock()
	defer workspace.mu.Unlock()
	if workspace.closed {
		return ErrWorkspaceClosed
	}
	if workspace.reconciliationNeeded {
		return ErrNeedsReconciliation
	}
	if expectedRevision == 0 || expectedGeneration == 0 || mutate == nil {
		return ErrStaleMutation
	}
	diskManifest, err := readManifest(filepath.Join(workspace.path, ManifestFileName))
	if err != nil {
		return err
	}
	if diskManifest.SessionID != workspace.ref.SessionID || diskManifest.Revision != expectedRevision || diskManifest.RunGeneration != expectedGeneration {
		return ErrStaleMutation
	}
	candidate := diskManifest.Clone()
	if err := mutate(&candidate); err != nil {
		return safeMutationError(err)
	}
	if err := validateMutationAuthority(diskManifest, candidate, authority); err != nil {
		return err
	}
	fromState := LifecycleState{Phase: diskManifest.Phase, Status: diskManifest.Status}
	toState := LifecycleState{Phase: candidate.Phase, Status: candidate.Status}
	if fromState != toState {
		if authority.kind != mutationReconciliation && diskManifest.Status == StatusNeedsReconciliation {
			return ErrInvalidTransition
		}
		if !CanTransition(fromState, toState) {
			return ErrInvalidTransition
		}
	}
	if candidate.Revision != diskManifest.Revision {
		return ErrMutationRejected
	}
	if authority.kind == mutationResume {
		if candidate.RunGeneration != diskManifest.RunGeneration+1 {
			return ErrMutationRejected
		}
	} else if candidate.RunGeneration != diskManifest.RunGeneration {
		return ErrMutationRejected
	}
	if candidate.Revision == ^uint64(0) {
		return ErrInvalidManifest
	}
	candidate.Revision++
	if candidate.UpdatedAt.Equal(diskManifest.UpdatedAt) {
		now := time.Now().UTC()
		if now.Before(diskManifest.UpdatedAt) {
			now = diskManifest.UpdatedAt
		}
		candidate.UpdatedAt = now
	}
	if err := candidate.Validate(); err != nil {
		return err
	}
	if err := validateManifestDerivedPaths(workspace.ref.OutputRoot, workspace.path, candidate); err != nil {
		return err
	}
	if err := persistManifest(filepath.Join(workspace.path, ManifestFileName), candidate); err != nil {
		var commitErr atomicfile.CommitError
		if errors.As(err, &commitErr) {
			if commitErr.Committed() {
				workspace.manifest = candidate
			}
			if commitErr.Indeterminate() {
				workspace.reconciliationNeeded = true
			}
		}
		return err
	}
	workspace.manifest = candidate
	return nil
}

func validateMutationAuthority(before, after Manifest, authority mutationAuthority) error {
	if after.Revision != before.Revision || after.UpdatedAt.Before(before.UpdatedAt) {
		return ErrMutationRejected
	}
	permitted := before.Clone()
	permitted.UpdatedAt = after.UpdatedAt
	switch authority.kind {
	case mutationTransition:
		if !validOrdinaryTransitionMutation(before, after) {
			return ErrMutationRejected
		}
		permitted.Phase = after.Phase
		permitted.Status = after.Status
		permitted.Desired = after.Desired
		permitted.LastTransition = after.LastTransition
	case mutationReconciliation:
		if before.Status != StatusNeedsReconciliation || after.Status == StatusActive || before.Publication == PublicationIndeterminate || before.Cleanup == CleanupIndeterminate || !validRecordedTransition(before, after) {
			return ErrMutationRejected
		}
		permitted.Phase = after.Phase
		permitted.Status = after.Status
		permitted.Desired = after.Desired
		permitted.LastTransition = after.LastTransition
	case mutationResume:
		if (before.Status != StatusPaused && before.Status != StatusFailed) || before.Cleanup != CleanupPending || after.Phase != before.Phase || after.Status != StatusActive || after.Desired != DesiredRunning || after.RunGeneration != before.RunGeneration+1 || !validRecordedTransition(before, after) {
			return ErrMutationRejected
		}
		permitted.Status = after.Status
		permitted.Desired = after.Desired
		permitted.LastTransition = after.LastTransition
		permitted.RunGeneration = after.RunGeneration
		if len(permitted.Components) != len(after.Components) {
			return ErrMutationRejected
		}
		for index := range permitted.Components {
			if after.Components[index].ObservedBytes != before.Components[index].CommittedBytes {
				return ErrMutationRejected
			}
			permitted.Components[index].ObservedBytes = after.Components[index].ObservedBytes
		}
	case mutationDesired:
		permitted.Desired = after.Desired
	case mutationDestination:
		if before.RelativeDestination != "" || !isSafeRelativePath(after.RelativeDestination) || phaseRequiresDestination(before.Phase) {
			return ErrMutationRejected
		}
		permitted.RelativeDestination = after.RelativeDestination
	case mutationComponentProgress:
		found := false
		if len(permitted.Components) != len(after.Components) {
			return ErrMutationRejected
		}
		for index := range permitted.Components {
			if permitted.Components[index].ID != authority.componentID {
				continue
			}
			found = true
			if after.Components[index].ObservedBytes < before.Components[index].ObservedBytes || after.Components[index].CommittedBytes < before.Components[index].CommittedBytes {
				return ErrMutationRejected
			}
			permitted.Components[index].ObservedBytes = after.Components[index].ObservedBytes
			permitted.Components[index].CommittedBytes = after.Components[index].CommittedBytes
		}
		if !found {
			return ErrMutationRejected
		}
	case mutationComponentCheckpoint:
		found := false
		if len(permitted.Components) != len(after.Components) {
			return ErrMutationRejected
		}
		for index := range permitted.Components {
			if permitted.Components[index].ID != authority.componentID {
				continue
			}
			found = true
			beforeComponent := before.Components[index]
			afterComponent := after.Components[index]
			if afterComponent.ObservedBytes < beforeComponent.ObservedBytes || afterComponent.CommittedBytes < beforeComponent.CommittedBytes ||
				afterComponent.Checkpoint.RelativePath != beforeComponent.Checkpoint.RelativePath {
				return ErrMutationRejected
			}
			permitted.Components[index].ObservedBytes = afterComponent.ObservedBytes
			permitted.Components[index].CommittedBytes = afterComponent.CommittedBytes
			permitted.Components[index].Checkpoint = afterComponent.Checkpoint
		}
		if !found {
			return ErrMutationRejected
		}
	case mutationComponentReset:
		found := false
		if len(permitted.Components) != len(after.Components) {
			return ErrMutationRejected
		}
		for index := range permitted.Components {
			if permitted.Components[index].ID != authority.componentID {
				continue
			}
			found = true
			beforeComponent := before.Components[index]
			afterComponent := after.Components[index]
			if afterComponent.ObservedBytes != 0 || afterComponent.CommittedBytes != 0 ||
				afterComponent.Checkpoint.RelativePath != beforeComponent.Checkpoint.RelativePath ||
				afterComponent.Checkpoint.Digest != "" || afterComponent.Checkpoint.Sequence != 0 ||
				afterComponent.Checkpoint.ETag != "" || afterComponent.Checkpoint.LastModified != "" || afterComponent.Checkpoint.Total != 0 {
				return ErrMutationRejected
			}
			permitted.Components[index].ObservedBytes = 0
			permitted.Components[index].CommittedBytes = 0
			permitted.Components[index].Checkpoint = afterComponent.Checkpoint
		}
		if !found {
			return ErrMutationRejected
		}
	case mutationStagedOutput:
		if before.Phase != PhaseProcessing && before.Phase != PhaseReadyToPublish || before.Status != StatusActive ||
			before.Publication == PublicationCommitted || before.Publication == PublicationIndeterminate ||
			after.StagedFingerprint == "" || after.StagedBytes < 0 ||
			after.Phase != before.Phase || after.Status != before.Status || after.Desired != before.Desired || after.Publication != before.Publication || after.RelativeDestination != before.RelativeDestination || after.LastTransition != before.LastTransition {
			return ErrMutationRejected
		}
		permitted.StagedFingerprint = after.StagedFingerprint
		permitted.StagedBytes = after.StagedBytes
	case mutationDestinationRebind:
		if before.Phase != PhaseReadyToPublish || before.Status == StatusActive || before.Publication == PublicationCommitted || before.Publication == PublicationIndeterminate ||
			!isSafeRelativePath(after.RelativeDestination) || after.Phase != before.Phase || after.Status != before.Status || after.Desired != before.Desired || after.Publication != before.Publication || after.StagedFingerprint != before.StagedFingerprint || after.StagedBytes != before.StagedBytes || after.LastTransition != before.LastTransition {
			return ErrMutationRejected
		}
		permitted.RelativeDestination = after.RelativeDestination
	case mutationPublication:
		if after.Phase != before.Phase || !validPublicationTransition(before.Publication, after.Publication, before.Phase) || (after.Publication != before.Publication && before.Status != StatusActive) {
			return ErrMutationRejected
		}
		if after.Publication == PublicationIndeterminate {
			if after.Status != StatusNeedsReconciliation || !validRecordedTransition(before, after) {
				return ErrMutationRejected
			}
		} else if before.Phase != after.Phase || before.Status != after.Status || before.Desired != after.Desired || before.LastTransition != after.LastTransition {
			return ErrMutationRejected
		}
		permitted.Publication = after.Publication
		permitted.Status = after.Status
		permitted.Desired = after.Desired
		permitted.LastTransition = after.LastTransition
	case mutationPublicationResolution:
		if before.Status != StatusNeedsReconciliation || before.Publication != PublicationIndeterminate || (after.Publication != PublicationCommitted && after.Publication != PublicationPending) {
			return ErrMutationRejected
		}
		permitted.Publication = after.Publication
	case mutationCleanup:
		if !terminalCleanupStatus(before.Status) || !validCleanupTransition(before.Cleanup, after.Cleanup) {
			return ErrMutationRejected
		}
		if after.Cleanup == CleanupIndeterminate {
			if after.Phase != before.Phase || after.Status != StatusNeedsReconciliation || !validRecordedTransition(before, after) {
				return ErrMutationRejected
			}
		} else if before.Phase != after.Phase || before.Status != after.Status || before.Desired != after.Desired || before.LastTransition != after.LastTransition {
			return ErrMutationRejected
		}
		permitted.Cleanup = after.Cleanup
		permitted.Phase = after.Phase
		permitted.Status = after.Status
		permitted.Desired = after.Desired
		permitted.LastTransition = after.LastTransition
	case mutationCleanupResolution:
		if before.Status != StatusNeedsReconciliation || before.Cleanup != CleanupIndeterminate || (after.Cleanup != CleanupComplete && after.Cleanup != CleanupPending) {
			return ErrMutationRejected
		}
		permitted.Cleanup = after.Cleanup
	default:
		return ErrMutationRejected
	}
	if !reflect.DeepEqual(permitted, after) {
		return ErrMutationRejected
	}
	return nil
}

func validRecordedTransition(before, after Manifest) bool {
	return after.LastTransition.FromPhase == before.Phase &&
		after.LastTransition.FromStatus == before.Status &&
		after.LastTransition.ToPhase == after.Phase &&
		after.LastTransition.ToStatus == after.Status &&
		after.LastTransition.At.Equal(after.UpdatedAt) &&
		!after.LastTransition.At.Before(before.UpdatedAt)
}

func validOrdinaryTransitionMutation(before, after Manifest) bool {
	if !validRecordedTransition(before, after) || !CanTransition(LifecycleState{Phase: before.Phase, Status: before.Status}, LifecycleState{Phase: after.Phase, Status: after.Status}) {
		return false
	}
	if before.Status != StatusActive && after.Status == StatusActive {
		return false
	}
	if before.Status == StatusActive && after.Status == StatusActive && (before.Desired != DesiredRunning || after.Desired != before.Desired) {
		return false
	}
	return before.Desired == DesiredRunning || after.Desired == before.Desired
}

// Transition applies a checked lifecycle transition.
func (workspace *Workspace) Transition(expectedRevision, expectedGeneration uint64, phase Phase, status Status, desired DesiredState, now time.Time) error {
	return workspace.update(expectedRevision, expectedGeneration, mutationAuthority{kind: mutationTransition}, func(manifest *Manifest) error {
		return manifest.Transition(phase, status, desired, now)
	})
}

// Resume retains the current Phase, normalizes component progress to durable
// checkpoints, and increments RunGeneration before committing the new image.
func (workspace *Workspace) Resume(expectedRevision, expectedGeneration uint64, now time.Time) error {
	return workspace.update(expectedRevision, expectedGeneration, mutationAuthority{kind: mutationResume}, func(manifest *Manifest) error {
		return manifest.Resume(now)
	})
}

// ResolveReconciliation applies a concrete reconciliation result. It is the
// only workspace mutation that can leave StatusNeedsReconciliation.
func (workspace *Workspace) ResolveReconciliation(expectedRevision, expectedGeneration uint64, status Status, desired DesiredState, now time.Time) error {
	return workspace.update(expectedRevision, expectedGeneration, mutationAuthority{kind: mutationReconciliation}, func(manifest *Manifest) error {
		return manifest.ResolveReconciliation(status, desired, now)
	})
}

// SetDesired records a new intent without pretending that the worker has
// already reached the corresponding disposition.
func (workspace *Workspace) SetDesired(expectedRevision, expectedGeneration uint64, desired DesiredState, now time.Time) error {
	return workspace.update(expectedRevision, expectedGeneration, mutationAuthority{kind: mutationDesired}, func(manifest *Manifest) error {
		if !validDesired(desired) || now.IsZero() || !validDesiredForStatus(manifest.Status, desired) {
			return ErrInvalidTransition
		}
		manifest.Desired = desired
		manifest.UpdatedAt = now.UTC()
		return nil
	})
}

// SetRelativeDestination records the metadata-derived output path once. The
// destination is immutable after it has been established.
func (workspace *Workspace) SetRelativeDestination(expectedRevision, expectedGeneration uint64, relative string, now time.Time) error {
	return workspace.update(expectedRevision, expectedGeneration, mutationAuthority{kind: mutationDestination}, func(manifest *Manifest) error {
		if manifest.RelativeDestination != "" || !isSafeRelativePath(relative) || now.IsZero() || phaseRequiresDestination(manifest.Phase) {
			return ErrInvalidTransition
		}
		manifest.RelativeDestination = relative
		manifest.UpdatedAt = now.UTC()
		return nil
	})
}

// SetComponentProgress records observed progress and a separately confirmed
// durable checkpoint boundary.
func (workspace *Workspace) SetComponentProgress(expectedRevision, expectedGeneration uint64, id string, observedBytes, committedBytes int64) error {
	return workspace.update(expectedRevision, expectedGeneration, mutationAuthority{kind: mutationComponentProgress, componentID: id}, func(manifest *Manifest) error {
		for _, component := range manifest.Components {
			if component.ID == id {
				if observedBytes < component.ObservedBytes || committedBytes < component.CommittedBytes || (committedBytes > 0 && component.Checkpoint.RelativePath == "") {
					return ErrInvalidManifest
				}
				break
			}
		}
		if err := manifest.SetComponentProgress(id, observedBytes, committedBytes); err != nil {
			return err
		}
		return nil
	})
}

// SetComponentCheckpoint records a durable direct-transfer checkpoint and its
// bounded validators after the downloader has flushed both payload and local
// checkpoint state. The checkpoint path is immutable for the component.
func (workspace *Workspace) SetComponentCheckpoint(expectedRevision, expectedGeneration uint64, id string, observedBytes, committedBytes int64, checkpoint CheckpointMetadata) error {
	return workspace.update(expectedRevision, expectedGeneration, mutationAuthority{kind: mutationComponentCheckpoint, componentID: id}, func(manifest *Manifest) error {
		for index := range manifest.Components {
			if manifest.Components[index].ID != id {
				continue
			}
			if checkpoint.RelativePath != manifest.Components[index].Checkpoint.RelativePath ||
				observedBytes < manifest.Components[index].ObservedBytes || committedBytes < manifest.Components[index].CommittedBytes {
				return ErrInvalidManifest
			}
			manifest.Components[index].ObservedBytes = observedBytes
			manifest.Components[index].CommittedBytes = committedBytes
			manifest.Components[index].Checkpoint = checkpoint
			return nil
		}
		return ErrInvalidManifest
	})
}

// ResetComponent discards a component's durable progress after the remote
// representation fails the strong-equivalence check. The caller owns the
// corresponding payload reset; this method only changes manifest authority.
func (workspace *Workspace) ResetComponent(expectedRevision, expectedGeneration uint64, id string, now time.Time) error {
	return workspace.update(expectedRevision, expectedGeneration, mutationAuthority{kind: mutationComponentReset, componentID: id}, func(manifest *Manifest) error {
		if now.IsZero() {
			return ErrInvalidManifest
		}
		for index := range manifest.Components {
			if manifest.Components[index].ID != id {
				continue
			}
			path := manifest.Components[index].Checkpoint.RelativePath
			manifest.Components[index].ObservedBytes = 0
			manifest.Components[index].CommittedBytes = 0
			manifest.Components[index].Checkpoint = CheckpointMetadata{RelativePath: path}
			manifest.UpdatedAt = now.UTC()
			return nil
		}
		return ErrInvalidManifest
	})
}

// ResetFragmentComponent first revokes the component's durable authority, then
// removes the fixed session fragment-ledger directory through the same
// handle-relative/no-follow traversal used by discard. It is intentionally
// limited to the engine-owned checkpoints/fragments layout; callers cannot
// supply an arbitrary path. This order is deliberate: a failure or a workspace
// replacement during deletion leaves a zero durable boundary, so stale local
// bytes cannot be reused. A later retry may safely remove the retained files.
func (workspace *Workspace) ResetFragmentComponent(expectedRevision, expectedGeneration uint64, id string, now time.Time) error {
	if workspace == nil || now.IsZero() {
		return ErrInvalidManifest
	}
	manifest, err := workspace.Snapshot()
	if err != nil {
		return err
	}
	if manifest.Revision != expectedRevision || manifest.RunGeneration != expectedGeneration {
		return ErrStaleMutation
	}
	var relative string
	found := false
	for _, component := range manifest.Components {
		if component.ID == id {
			relative = component.Checkpoint.RelativePath
			found = true
			break
		}
	}
	if !found || relative != CheckpointDirectoryName+"/fragments/state.json" {
		return ErrUnsafePath
	}
	if err := validateWorkspaceRootIdentity(workspace.ref); err != nil {
		return err
	}
	// Commit the authority reset before any destructive traversal. In
	// particular, do not call the ordinary pathname-based manifest writer after
	// an identity-bound deletion: a path replacement in that interval could
	// redirect a manifest mutation. If deletion fails, this committed zero
	// boundary is intentionally fail-closed and a later retry owns cleanup.
	if err := workspace.ResetComponent(expectedRevision, expectedGeneration, id, now); err != nil {
		return err
	}
	root, openErr := openDiscardRoot(workspace.path, workspace.workspaceIdentity)
	if openErr == nil {
		// Fixed payload candidates and checkpoint children are opened from
		// this identity-bound workspace handle; no descendant pathname is
		// trusted after the root is opened.
		for _, name := range []string{"payload", "payload.part"} {
			if removeErr := removeDiscardNamedFile(root, name); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
				_ = root.close()
				return removeErr
			}
		}
		parent, parentErr := openDiscardEntry(root, CheckpointDirectoryName, nil, true)
		if parentErr == nil {
			checkpointDirectory := &discardDirectory{file: parent.file, path: parent.path, identity: parent.identity}
			child, childErr := openDiscardEntry(checkpointDirectory, "fragments", nil, true)
			if childErr == nil {
				directory := &discardDirectory{file: child.file, path: child.path, identity: child.identity}
				removeErr := removeDiscardChildrenFromHandle(directory, &discardTreeBudget{}, true)
				if removeErr == nil {
					removeErr = syncDiscardDirectoryHandle(directory)
				}
				if removeErr == nil {
					removeErr = child.remove()
				}
				closeErr := child.close()
				parentErr := parent.close()
				rootErr := root.close()
				if removeErr != nil {
					return removeErr
				}
				if closeErr != nil {
					return closeErr
				}
				if parentErr != nil {
					return parentErr
				}
				if rootErr != nil {
					return rootErr
				}
			} else {
				_ = parent.close()
				_ = root.close()
				if !errors.Is(childErr, os.ErrNotExist) {
					return childErr
				}
			}
		} else {
			_ = root.close()
			if !errors.Is(parentErr, os.ErrNotExist) {
				return parentErr
			}
		}
	} else if !errors.Is(openErr, os.ErrNotExist) {
		return openErr
	}
	return nil
}

// SetStagedOutput records the fingerprint of a fully fsynced staged output.
// It is separate from publication state so a crash can distinguish a staged
// artifact from a destination commit.
func (workspace *Workspace) SetStagedOutput(expectedRevision, expectedGeneration uint64, fingerprint string, bytes int64) error {
	return workspace.update(expectedRevision, expectedGeneration, mutationAuthority{kind: mutationStagedOutput}, func(manifest *Manifest) error {
		if fingerprint == "" || bytes < 0 {
			return ErrInvalidManifest
		}
		manifest.StagedFingerprint = fingerprint
		manifest.StagedBytes = bytes
		return nil
	})
}

// RebindRelativeDestination changes the reserved basename after a durable
// destination collision. It is legal only for a failed/paused ready-to-publish
// session; completed publication and indeterminate evidence are immutable.
func (workspace *Workspace) RebindRelativeDestination(expectedRevision, expectedGeneration uint64, relative string, now time.Time) error {
	return workspace.update(expectedRevision, expectedGeneration, mutationAuthority{kind: mutationDestinationRebind}, func(manifest *Manifest) error {
		if !isSafeRelativePath(relative) || now.IsZero() || manifest.Phase != PhaseReadyToPublish || manifest.Status == StatusActive || manifest.Publication == PublicationCommitted || manifest.Publication == PublicationIndeterminate {
			return ErrInvalidTransition
		}
		manifest.RelativeDestination = relative
		manifest.UpdatedAt = now.UTC()
		return nil
	})
}

// SetPublicationState records publication authority. An indeterminate
// publication moves the disposition to needs_reconciliation while preserving
// the execution phase.
func (workspace *Workspace) SetPublicationState(expectedRevision, expectedGeneration uint64, state PublicationState, now time.Time) error {
	return workspace.update(expectedRevision, expectedGeneration, mutationAuthority{kind: mutationPublication}, func(manifest *Manifest) error {
		if now.IsZero() || manifest.Status != StatusActive || !validPublicationTransition(manifest.Publication, state, manifest.Phase) {
			return ErrInvalidTransition
		}
		if manifest.Publication == PublicationIndeterminate && state != PublicationIndeterminate {
			return ErrNeedsReconciliation
		}
		if state == PublicationIndeterminate {
			if manifest.Status != StatusNeedsReconciliation {
				if err := manifest.Transition(manifest.Phase, StatusNeedsReconciliation, manifest.Desired, now); err != nil {
					return err
				}
			}
		}
		manifest.Publication = state
		manifest.UpdatedAt = now.UTC()
		return nil
	})
}

// ResolvePublication is the dedicated reconciliation mutation for an
// indeterminate publication. Reconciliation may prove that publication
// committed or that it remains pending and is safe to retry.
func (workspace *Workspace) ResolvePublication(expectedRevision, expectedGeneration uint64, state PublicationState, now time.Time) error {
	return workspace.update(expectedRevision, expectedGeneration, mutationAuthority{kind: mutationPublicationResolution}, func(manifest *Manifest) error {
		if manifest.Status != StatusNeedsReconciliation || manifest.Publication != PublicationIndeterminate || (state != PublicationCommitted && state != PublicationPending) || now.IsZero() {
			return ErrInvalidTransition
		}
		manifest.Publication = state
		manifest.UpdatedAt = now.UTC()
		return nil
	})
}

// SetCleanupState records cleanup authority without removing anything.
func (workspace *Workspace) SetCleanupState(expectedRevision, expectedGeneration uint64, state CleanupState, now time.Time) error {
	return workspace.update(expectedRevision, expectedGeneration, mutationAuthority{kind: mutationCleanup}, func(manifest *Manifest) error {
		if now.IsZero() || !validCleanupTransition(manifest.Cleanup, state) || !terminalCleanupStatus(manifest.Status) {
			return ErrInvalidTransition
		}
		if state == CleanupIndeterminate {
			if err := manifest.transition(manifest.Phase, StatusNeedsReconciliation, manifest.Desired, now, false, true); err != nil {
				return err
			}
		}
		manifest.Cleanup = state
		manifest.UpdatedAt = now.UTC()
		return nil
	})
}

// ResolveCleanup records whether indeterminate cleanup completed or remains
// pending and safe to retry. The reconciliation disposition is retained until
// ResolveReconciliation records the resulting terminal lifecycle state.
func (workspace *Workspace) ResolveCleanup(expectedRevision, expectedGeneration uint64, state CleanupState, now time.Time) error {
	return workspace.update(expectedRevision, expectedGeneration, mutationAuthority{kind: mutationCleanupResolution}, func(manifest *Manifest) error {
		if manifest.Status != StatusNeedsReconciliation || manifest.Cleanup != CleanupIndeterminate || (state != CleanupComplete && state != CleanupPending) || now.IsZero() {
			return ErrInvalidTransition
		}
		manifest.Cleanup = state
		manifest.UpdatedAt = now.UTC()
		return nil
	})
}

func randomSessionID() (string, error) {
	var bytes [16]byte
	if _, err := rand.Read(bytes[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(bytes[:]), nil
}

func persistManifest(path string, manifest Manifest) error {
	if err := validateManifestTarget(path, true); err != nil {
		return err
	}
	if err := manifest.Validate(); err != nil {
		return err
	}
	err := atomicManifestWrite(path, 0o600, func(writer io.Writer) error {
		encoder := json.NewEncoder(writer)
		encoder.SetEscapeHTML(false)
		return encoder.Encode(manifest)
	})
	if err == nil {
		return nil
	}
	return wrapManifestCommit(err)
}

func validateManifestDerivedPaths(outputRoot, workspacePath string, manifest Manifest) error {
	if manifest.RelativeDestination != "" {
		if err := validateDestination(outputRoot, manifest.RelativeDestination); err != nil {
			return err
		}
	}
	for _, component := range manifest.Components {
		if component.Checkpoint.RelativePath == "" {
			continue
		}
		if err := validateCheckpointDerivedPath(workspacePath, component.Checkpoint.RelativePath); err != nil {
			return err
		}
	}
	return nil
}

func validateCheckpointDerivedPath(workspacePath, relative string) error {
	if !isCheckpointRelativePath(relative) {
		return ErrUnsafePath
	}
	target := filepath.Join(workspacePath, filepath.FromSlash(relative))
	contained, err := filepath.Rel(workspacePath, target)
	if err != nil || contained != filepath.FromSlash(relative) || filepath.IsAbs(contained) || strings.HasPrefix(contained, ".."+string(filepath.Separator)) {
		return ErrUnsafePath
	}
	current := workspacePath
	parts := strings.Split(relative, "/")
	for index, part := range parts {
		current = filepath.Join(current, part)
		info, statErr := os.Lstat(current)
		if errors.Is(statErr, os.ErrNotExist) {
			return nil
		}
		if statErr != nil || info.Mode()&os.ModeSymlink != 0 {
			return ErrUnsafePath
		}
		if index == len(parts)-1 {
			if !info.Mode().IsRegular() || !ownerOnlyFileAt(current, info) {
				return ErrUnsafePath
			}
		} else if !info.IsDir() || !ownerOnlyDirectoryAt(current, info) {
			return ErrUnsafePath
		}
	}
	return nil
}

func cleanupCreatedWorkspace(workspacePath string) bool {
	info, err := os.Lstat(workspacePath)
	if errors.Is(err, os.ErrNotExist) {
		return true
	}
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return false
	}
	entries, err := os.ReadDir(workspacePath)
	if err != nil {
		return false
	}
	for _, entry := range entries {
		if entry.Name() != ManifestFileName && entry.Name() != LeaseFileName {
			// In particular, retained .atomic-* evidence is never silently
			// removed when cleanup cannot prove a confirmed precommit state.
			return false
		}
		child := filepath.Join(workspacePath, entry.Name())
		childInfo, statErr := os.Lstat(child)
		if statErr != nil || childInfo.Mode()&os.ModeSymlink != 0 || !childInfo.Mode().IsRegular() || !ownerOnlyFileAt(child, childInfo) {
			return false
		}
	}
	for _, name := range []string{ManifestFileName, LeaseFileName} {
		if err := os.Remove(filepath.Join(workspacePath, name)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return false
		}
	}
	return os.Remove(workspacePath) == nil
}

func readManifest(path string) (Manifest, error) {
	if err := validateManifestTarget(path, false); err != nil {
		return Manifest{}, ErrCorruptManifest
	}
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || !ownerOnlyFileAt(path, info) || info.Size() > maxManifestBytes {
		return Manifest{}, ErrCorruptManifest
	}
	file, err := os.Open(path)
	if err != nil {
		return Manifest{}, ErrCorruptManifest
	}
	defer file.Close()
	encoded, err := io.ReadAll(io.LimitReader(file, maxManifestBytes+1))
	if err != nil || len(encoded) > maxManifestBytes {
		return Manifest{}, ErrCorruptManifest
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	var manifest Manifest
	if err := decoder.Decode(&manifest); err != nil {
		return Manifest{}, ErrCorruptManifest
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return Manifest{}, ErrCorruptManifest
	}
	if err := manifest.Validate(); err != nil {
		if errors.Is(err, ErrUnknownManifestVersion) {
			return Manifest{}, err
		}
		return Manifest{}, ErrCorruptManifest
	}
	return manifest, nil
}

func validateManifestTarget(path string, allowMissing bool) error {
	if path == "" || !filepath.IsAbs(path) || filepath.Base(path) != ManifestFileName || containsNUL(path) {
		return ErrUnsafePath
	}
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		if allowMissing {
			return nil
		}
		return ErrCorruptManifest
	}
	if err != nil {
		return ErrUnsafePath
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || !ownerOnlyFileAt(path, info) {
		return ErrUnsafePath
	}
	return nil
}

func containsNUL(value string) bool {
	for index := 0; index < len(value); index++ {
		if value[index] == 0 {
			return true
		}
	}
	return false
}

func wrapManifestCommit(err error) error {
	if err == nil {
		return nil
	}
	var outcome atomicfile.CommitError
	if !errors.As(err, &outcome) {
		return &manifestCommitError{err: err}
	}
	return &manifestCommitError{err: err, committed: outcome.Committed(), indeterminate: outcome.Indeterminate()}
}

type manifestCommitError struct {
	err           error
	committed     bool
	indeterminate bool
}

func (err *manifestCommitError) Error() string {
	if err.indeterminate {
		return "session manifest commit outcome is indeterminate"
	}
	if err.committed {
		return "session manifest commit completed with a durability error"
	}
	return "session manifest commit failed before replacement"
}

func (err *manifestCommitError) Is(target error) bool {
	return target == ErrManifestCommit
}

func (err *manifestCommitError) Committed() bool { return err.committed }

func (err *manifestCommitError) Indeterminate() bool { return err.indeterminate }

func safeMutationError(err error) error {
	safe := []error{ErrInvalidManifest, ErrUnsafePath, ErrInvalidTransition, ErrNeedsReconciliation}
	for _, candidate := range safe {
		if errors.Is(err, candidate) {
			return candidate
		}
	}
	return ErrMutationRejected
}
