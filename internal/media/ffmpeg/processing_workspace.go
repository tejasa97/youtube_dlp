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
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"

	"github.com/tejasa97/youtube_dlp/internal/atomicfile"
	"github.com/tejasa97/youtube_dlp/internal/events"
)

var (
	ErrInvalidProcessingWorkspace = errors.New("invalid ffmpeg processing workspace")
	ErrProcessingReconciliation   = errors.New("ffmpeg processing workspace requires reconciliation")
	ErrProcessingCommitted        = errors.New("ffmpeg output publication committed with retained workspace evidence")
	ErrProcessingBusy             = errors.New("ffmpeg processing workspace is already in use")
	errProcessingLeaseIdentity    = errors.New("processing lease path identity does not match locked handle")
)

const (
	processingStateVersion    = 1
	maxProcessingState        = 64 << 10
	maxProcessingIdentity     = 512
	maxProcessingPath         = 4096
	maxProcessingEntries      = 128
	maxProcessingArtifact     = int64(1) << 50
	processingCleanupPhase    = "cleanup_pending"
	processingGuardName       = ".guard"
	processingGuardLockSuffix = ".lock"
	processingCleanupName     = ".cleanup"
	processingLeaseName       = ".lease"

	processingPhaseRunning        = "running"
	processingPhaseOutputComplete = "output_complete"
	processingPhasePublishing     = "publication_pending"
	processingPhaseCommitted      = "publication_committed"
	processingPhaseIndeterminate  = "publication_indeterminate"
)

// ProcessingWorkspace opts one finite FFmpeg operation into restart-safe
// processing. Directory and OutputRoot must be canonical absolute paths.
// OperationIdentity and InputFingerprint are bounded non-secret caller
// authorities that bind the exact operation, output format, and complete input
// set. Paths, arguments, URLs, headers, and credentials are never persisted.
// Directory is an exclusive-use capability: this implementation holds a
// cross-process OS lease for the complete probe, processing, publication, and
// cleanup interval. Callers must not use one Directory for different
// operations concurrently; a busy lease is returned as ErrProcessingBusy.
type ProcessingWorkspace struct {
	OutputRoot        string
	Directory         string
	OperationIdentity string
	InputFingerprint  string
}

type ProcessingWorkspaceError struct {
	Kind   error
	Detail string
	Cause  error
}

func (failure *ProcessingWorkspaceError) Error() string {
	if failure.Cause != nil {
		return fmt.Sprintf("%v: %s: %v", failure.Kind, failure.Detail, failure.Cause)
	}
	return fmt.Sprintf("%v: %s", failure.Kind, failure.Detail)
}

func (failure *ProcessingWorkspaceError) Unwrap() []error {
	if failure.Cause == nil {
		return []error{failure.Kind}
	}
	return []error{failure.Kind, failure.Cause}
}

type ProcessingAuthorityError struct {
	Cause         error
	committed     bool
	indeterminate bool
}

func (failure *ProcessingAuthorityError) Error() string {
	kind := ErrProcessingReconciliation
	if failure.committed {
		kind = ErrProcessingCommitted
	}
	return fmt.Sprintf("%v: %v", kind, failure.Cause)
}

func (failure *ProcessingAuthorityError) Unwrap() []error {
	kind := ErrProcessingReconciliation
	if failure.committed {
		kind = ErrProcessingCommitted
	}
	return []error{kind, failure.Cause}
}

func (failure *ProcessingAuthorityError) Committed() bool     { return failure.committed }
func (failure *ProcessingAuthorityError) Indeterminate() bool { return failure.indeterminate }

type processingState struct {
	Version           int                 `json:"version"`
	OperationIdentity string              `json:"operation_identity"`
	InputFingerprint  string              `json:"input_fingerprint"`
	Phase             string              `json:"phase"`
	Output            *processingArtifact `json:"output,omitempty"`
}

type processingArtifact struct {
	Bytes  int64  `json:"bytes"`
	SHA256 string `json:"sha256"`
}

type processingCleanupMarker struct {
	Version           int    `json:"version"`
	OperationIdentity string `json:"operation_identity"`
	InputFingerprint  string `json:"input_fingerprint"`
	Phase             string `json:"phase"`
}

type processingLease struct {
	identity           string
	guardLockIdentity  string
	validateFn         func(bool, bool) error
	removeFn           func() error
	removeGuardLockFn  func() error
	releaseLeaseFn     func() error
	releaseGuardLockFn func() error
	markFn             func(ProcessingWorkspace) error
	leaseReleaseOnce   sync.Once
	guardReleaseOnce   sync.Once
	leaseReleaseErr    error
	guardReleaseErr    error
}

type processingGuardRecovery struct {
	identity   string
	validateFn func() error
	removeFn   func() error
	releaseFn  func() error
	once       sync.Once
	err        error
}

func (recovery *processingGuardRecovery) validate() error {
	if recovery == nil || recovery.validateFn == nil {
		return errors.New("processing guard recovery cannot validate its locked handle")
	}
	return recovery.validateFn()
}

func (recovery *processingGuardRecovery) removeNamedPath() error {
	if recovery == nil || recovery.removeFn == nil {
		return errors.New("processing guard recovery cannot remove its named path")
	}
	return recovery.removeFn()
}

func (recovery *processingGuardRecovery) release() error {
	if recovery == nil || recovery.releaseFn == nil {
		return nil
	}
	recovery.once.Do(func() { recovery.err = recovery.releaseFn() })
	return recovery.err
}

func (lease *processingLease) release() error {
	if lease == nil {
		return nil
	}
	return errors.Join(lease.releaseLeaseHandle(), lease.releaseGuardLockHandle())
}

func (lease *processingLease) releaseLeaseHandle() error {
	if lease == nil || lease.releaseLeaseFn == nil {
		return errors.New("processing workspace lease cannot release its lease handle")
	}
	lease.leaseReleaseOnce.Do(func() { lease.leaseReleaseErr = lease.releaseLeaseFn() })
	return lease.leaseReleaseErr
}

func (lease *processingLease) releaseGuardLockHandle() error {
	if lease == nil || lease.releaseGuardLockFn == nil {
		return errors.New("processing workspace lease cannot release its guard lock handle")
	}
	lease.guardReleaseOnce.Do(func() { lease.guardReleaseErr = lease.releaseGuardLockFn() })
	return lease.guardReleaseErr
}

func (lease *processingLease) removeNamedPath() error {
	if lease == nil || lease.removeFn == nil {
		return errors.New("processing workspace lease cannot remove its named path")
	}
	return lease.removeFn()
}

func (lease *processingLease) removeGuardLockNamedPath() error {
	if lease == nil || lease.removeGuardLockFn == nil {
		return errors.New("processing workspace lease cannot remove its guard lock path")
	}
	return lease.removeGuardLockFn()
}

func (lease *processingLease) validateLockedIdentity(leaseHeld, guardLockHeld bool) error {
	if lease == nil || lease.validateFn == nil {
		return errors.New("processing workspace lease cannot validate its locked handle")
	}
	return lease.validateFn(leaseHeld, guardLockHeld)
}

func (lease *processingLease) markCleanupComplete(workspace ProcessingWorkspace) error {
	if lease == nil || lease.markFn == nil {
		return errors.New("processing workspace lease cannot record cleanup")
	}
	return lease.markFn(workspace)
}

type processingLeaseMarker struct {
	Version           int    `json:"version"`
	OperationIdentity string `json:"operation_identity"`
	InputFingerprint  string `json:"input_fingerprint"`
	Phase             string `json:"phase"`
}

type processingPreflight struct {
	workspace        ProcessingWorkspace
	destination      string
	stage            string
	statePath        string
	cleanupPath      string
	leasePath        string
	guardPath        string
	guardLockPath    string
	finalCleanupPath string
	state            processingState
	fresh            bool
	lease            *processingLease
	rootIdentity     string
	workspaceID      string
	guardID          string
	leaseID          string
	guardLockID      string
}

type processingCleanupIdentities struct {
	rootID           string
	workspaceID      string
	workspacePresent bool
	guardID          string
	guardPresent     bool
	leaseID          string
	leasePresent     bool
	guardLockID      string
	guardLockPresent bool
}

func (preflight *processingPreflight) cleanupIdentities() processingCleanupIdentities {
	return processingCleanupIdentities{
		rootID: preflight.rootIdentity, workspaceID: preflight.workspaceID, workspacePresent: true,
		guardID: preflight.guardID, guardPresent: true, leaseID: preflight.leaseID, leasePresent: true,
		guardLockID: preflight.guardLockID, guardLockPresent: true,
	}
}

type processingWorkspaceOps struct {
	writeAtomic                     func(string, os.FileMode, func(io.Writer) error) error
	replaceAtomic                   func(string, string) error
	publishNoClobber                func(string, string) error
	remove                          func(string) error
	syncDirectory                   func(string) error
	beforeInitialWorkspaceMutation  func(string) error
	beforeProcessingEvidenceRead    func(string) error
	afterProcessingLeaseAcquisition func(string) error
	afterDestinationParentCreation  func(string) error
	beforeCleanupMutation           func(string) error
	beforeFinalCleanupRecovery      func(string) error
}

var productionProcessingWorkspaceOps = processingWorkspaceOps{
	writeAtomic: atomicfile.Write, replaceAtomic: atomicfile.Replace,
	publishNoClobber: processingPublishNoClobber, remove: os.Remove,
	syncDirectory: syncProcessingDirectory,
}

func processingFailure(kind error, detail string, cause error) error {
	return &ProcessingWorkspaceError{Kind: kind, Detail: detail, Cause: cause}
}

func validateProcessingNamedIdentity(label, path, expected string) error {
	actual, err := processingPathIdentity(path)
	if err != nil || actual != expected {
		return processingFailure(ErrProcessingReconciliation, label+" identity changed", err)
	}
	return nil
}

func validateProcessingLeaseIdentity(label, path, expected string) error {
	actual, err := processingLeasePathIdentity(path)
	if err != nil || actual != expected {
		return processingFailure(ErrProcessingReconciliation, label+" identity changed", err)
	}
	return nil
}

func processingLeaseHandleIdentityMatches(expected, actual string, unlinked bool) bool {
	if expected == actual {
		return true
	}
	if !unlinked {
		return false
	}
	expectedSeparator := strings.LastIndexByte(expected, ':')
	actualSeparator := strings.LastIndexByte(actual, ':')
	return expectedSeparator > 0 && actualSeparator > 0 &&
		expected[:expectedSeparator] == actual[:actualSeparator] && actual[actualSeparator+1:] == "0"
}

func validateOptionalProcessingLeaseIdentity(label, path, expected string, present bool) error {
	actual, err := processingLeasePathIdentity(path)
	if !present && errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil || !present || actual != expected {
		return processingFailure(ErrProcessingReconciliation, label+" identity changed", err)
	}
	return nil
}

func captureOptionalProcessingLeaseIdentity(path string) (string, bool, error) {
	identity, err := processingLeasePathIdentity(path)
	if errors.Is(err, os.ErrNotExist) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return identity, true, nil
}

func validateProcessingCleanupIdentities(ids processingCleanupIdentities, root, workDir, guardPath, leasePath, guardLockPath string) error {
	if err := validateProcessingNamedIdentity("output root during cleanup", root, ids.rootID); err != nil {
		return err
	}
	if err := validateOptionalProcessingIdentity("workspace during cleanup", workDir, ids.workspaceID, ids.workspacePresent); err != nil {
		return err
	}
	if err := validateOptionalProcessingIdentity("processing guard during cleanup", guardPath, ids.guardID, ids.guardPresent); err != nil {
		return err
	}
	if err := validateOptionalProcessingLeaseIdentity("processing lease during cleanup", leasePath, ids.leaseID, ids.leasePresent); err != nil {
		return err
	}
	return validateOptionalProcessingLeaseIdentity("processing guard lock during cleanup", guardLockPath, ids.guardLockID, ids.guardLockPresent)
}

func captureOptionalProcessingIdentity(path string) (string, bool, error) {
	identity, err := processingPathIdentity(path)
	if errors.Is(err, os.ErrNotExist) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return identity, true, nil
}

func validateOptionalProcessingIdentity(label, path, expected string, present bool) error {
	actual, err := processingPathIdentity(path)
	if !present && errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil || !present || actual != expected {
		return processingFailure(ErrProcessingReconciliation, label+" identity changed", err)
	}
	return nil
}

func (tools *Toolset) runAtomicWorkspace(
	ctx context.Context,
	destination string,
	overwrite bool,
	sink events.Sink,
	workspace ProcessingWorkspace,
	prepare func(string) error,
	operation func(string) []string,
) error {
	return tools.runAtomicWorkspaceWithOps(ctx, destination, overwrite, sink, workspace, prepare, operation, productionProcessingWorkspaceOps)
}

func (tools *Toolset) runAtomicWorkspaceWithOps(
	ctx context.Context,
	destination string,
	overwrite bool,
	sink events.Sink,
	workspace ProcessingWorkspace,
	prepare func(string) error,
	operation func(string) []string,
	ops processingWorkspaceOps,
) error {
	preflight, err := preflightProcessingWorkspace(ctx, destination, overwrite, workspace, ops)
	if err != nil {
		return err
	}
	return tools.executeProcessingWorkspace(ctx, overwrite, sink, preflight, prepare, operation, ops)
}

func preflightProcessingWorkspace(
	ctx context.Context,
	destination string,
	overwrite bool,
	workspace ProcessingWorkspace,
	ops processingWorkspaceOps,
) (*processingPreflight, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := validateProcessingWorkspace(workspace, destination); err != nil {
		return nil, err
	}
	if err := inspectProcessingRoot(workspace.OutputRoot); err != nil {
		return nil, err
	}
	rootID, err := processingPathIdentity(workspace.OutputRoot)
	if err != nil {
		return nil, processingFailure(ErrInvalidProcessingWorkspace, "capture output-root identity", err)
	}
	if err := inspectProcessingParentChain(workspace.OutputRoot, workspace.Directory); err != nil {
		return nil, err
	}
	if err := validateProcessingNamedIdentity("output root before processing evidence access", workspace.OutputRoot, rootID); err != nil {
		return nil, err
	}
	destinationExists, err := inspectProcessingDestination(workspace.OutputRoot, destination)
	if err != nil {
		return nil, err
	}
	cleanupPath := processingCleanupMarkerPath(workspace.Directory)
	finalCleanupPath := processingFinalCleanupMarkerPath(workspace.Directory)
	leasePath := processingLeasePath(workspace.Directory)
	guardPath := processingGuardPath(workspace.Directory)
	guardLockPath := processingGuardLockPath(workspace.Directory)
	workspaceID, workspacePresent, err := captureOptionalProcessingIdentity(workspace.Directory)
	if err != nil {
		return nil, processingFailure(ErrInvalidProcessingWorkspace, "capture workspace identity", err)
	}
	guardID, guardPresent, err := captureOptionalProcessingIdentity(guardPath)
	if err != nil {
		return nil, processingFailure(ErrProcessingReconciliation, "capture processing-guard identity", err)
	}
	guardLockID, guardLockPresent, err := captureOptionalProcessingLeaseIdentity(guardLockPath)
	if err != nil {
		return nil, processingFailure(ErrProcessingReconciliation, "capture processing guard-lock identity", err)
	}
	finalCleanupSeen, finalCleanupErr := inspectProcessingCleanupMarker(finalCleanupPath, workspace)
	if finalCleanupErr != nil {
		return nil, finalCleanupErr
	}
	finalCleanupID, finalCleanupPresent, finalCleanupIdentityErr := captureOptionalProcessingIdentity(finalCleanupPath)
	if finalCleanupIdentityErr != nil {
		return nil, processingFailure(ErrProcessingReconciliation, "capture final cleanup marker identity", finalCleanupIdentityErr)
	}
	if finalCleanupSeen && !finalCleanupPresent {
		return nil, processingFailure(ErrProcessingReconciliation, "final cleanup marker disappeared during preflight", nil)
	}
	if guardLockPresent && !guardPresent {
		if ops.beforeFinalCleanupRecovery != nil {
			if err := ops.beforeFinalCleanupRecovery(guardLockPath); err != nil {
				return nil, err
			}
		}
		if err := recoverProcessingFinalCleanup(workspace, rootID, guardLockPath, guardLockID, finalCleanupPath, finalCleanupID, finalCleanupPresent, ops); err != nil {
			return nil, &ProcessingAuthorityError{Cause: err, committed: true}
		}
		return nil, &ProcessingAuthorityError{Cause: errors.New("durable cleanup evidence records committed publication"), committed: true}
	}
	if finalCleanupSeen && !guardPresent {
		return nil, &ProcessingAuthorityError{Cause: errors.New("durable cleanup evidence requires manual reconciliation without guard authority"), committed: true}
	}
	if guardPresent && ops.beforeProcessingEvidenceRead != nil {
		if err := ops.beforeProcessingEvidenceRead(guardPath); err != nil {
			return nil, err
		}
	}
	if err := validateProcessingNamedIdentity("output root before guard inspection", workspace.OutputRoot, rootID); err != nil {
		return nil, err
	}
	if err := validateOptionalProcessingIdentity("processing guard before inspection", guardPath, guardID, guardPresent); err != nil {
		return nil, err
	}
	if err := validateOptionalProcessingLeaseIdentity("processing guard lock before inspection", guardLockPath, guardLockID, guardLockPresent); err != nil {
		return nil, err
	}
	if guardPresent {
		if err := inspectProcessingGuardAuthority(guardPath); err != nil {
			return nil, err
		}
	}
	if err := validateProcessingNamedIdentity("output root before lease acquisition", workspace.OutputRoot, rootID); err != nil {
		return nil, err
	}
	if err := validateOptionalProcessingIdentity("processing guard before lease acquisition", guardPath, guardID, guardPresent); err != nil {
		return nil, err
	}
	if err := validateOptionalProcessingLeaseIdentity("processing guard lock before lease acquisition", guardLockPath, guardLockID, guardLockPresent); err != nil {
		return nil, err
	}

	var lease *processingLease
	if guardPresent {
		lease, err = acquireProcessingLease(leasePath)
		if err != nil {
			if errors.Is(err, errProcessingLeaseIdentity) {
				return nil, processingFailure(ErrProcessingReconciliation, "acquire processing lease", err)
			}
			return nil, processingFailure(ErrProcessingBusy, "acquire cleanup workspace lease", err)
		}
		if ops.afterProcessingLeaseAcquisition != nil {
			if err := ops.afterProcessingLeaseAcquisition(leasePath); err != nil {
				_ = lease.release()
				return nil, err
			}
		}
		if err := validateProcessingLeaseIdentity("processing lease after acquisition", leasePath, lease.identity); err != nil {
			_ = lease.release()
			return nil, err
		}
		guardLockID, guardLockPresent = lease.guardLockIdentity, true
		if err := validateProcessingLeaseIdentity("processing guard lock after acquisition", guardLockPath, guardLockID); err != nil {
			_ = lease.release()
			return nil, err
		}
		if err := validateProcessingNamedIdentity("output root after lease acquisition", workspace.OutputRoot, rootID); err != nil {
			_ = lease.release()
			return nil, err
		}
		if err := validateOptionalProcessingIdentity("processing guard after lease acquisition", guardPath, guardID, guardPresent); err != nil {
			_ = lease.release()
			return nil, err
		}
		if err := inspectProcessingGuardDirectory(guardPath); err != nil {
			_ = lease.release()
			return nil, err
		}
		if err := validateOptionalProcessingIdentity("processing guard before cleanup evidence read", guardPath, guardID, guardPresent); err != nil {
			_ = lease.release()
			return nil, err
		}
		if err := validateProcessingLeaseIdentity("processing lease before cleanup evidence read", leasePath, lease.identity); err != nil {
			_ = lease.release()
			return nil, err
		}
		if err := validateProcessingLeaseIdentity("processing guard lock before cleanup evidence read", guardLockPath, guardLockID); err != nil {
			_ = lease.release()
			return nil, err
		}
		markerSeen, markerErr := inspectProcessingCleanupMarker(cleanupPath, workspace)
		if markerErr != nil {
			_ = lease.release()
			return nil, markerErr
		}
		leaseComplete, leaseErr := inspectProcessingLeaseCompletion(leasePath, workspace)
		if leaseErr != nil {
			_ = lease.release()
			return nil, leaseErr
		}
		guardLockComplete, guardLockErr := inspectProcessingLeaseCompletion(guardLockPath, workspace)
		if guardLockErr != nil {
			_ = lease.release()
			return nil, guardLockErr
		}
		if err := validateProcessingNamedIdentity("output root after cleanup evidence read", workspace.OutputRoot, rootID); err != nil {
			_ = lease.release()
			return nil, err
		}
		if err := validateOptionalProcessingIdentity("processing guard after cleanup evidence read", guardPath, guardID, guardPresent); err != nil {
			_ = lease.release()
			return nil, err
		}
		if err := validateProcessingLeaseIdentity("processing lease after cleanup evidence read", leasePath, lease.identity); err != nil {
			_ = lease.release()
			return nil, err
		}
		if err := validateProcessingLeaseIdentity("processing guard lock after cleanup evidence read", guardLockPath, guardLockID); err != nil {
			_ = lease.release()
			return nil, err
		}
		if err := validateOptionalProcessingIdentity("workspace after cleanup evidence read", workspace.Directory, workspaceID, workspacePresent); err != nil {
			_ = lease.release()
			return nil, err
		}
		if markerSeen || leaseComplete || guardLockComplete || finalCleanupSeen {
			cleanupIDs := processingCleanupIdentities{
				rootID: rootID, workspaceID: workspaceID, workspacePresent: workspacePresent,
				guardID: guardID, guardPresent: guardPresent, leaseID: lease.identity, leasePresent: true,
				guardLockID: guardLockID, guardLockPresent: true,
			}
			cleanupErr := cleanupCommittedProcessingWorkspace(workspace.Directory, filepath.Join(workspace.Directory, "state.json"), cleanupPath, leasePath, lease, workspace, cleanupIDs, ops)
			if cleanupErr != nil {
				_ = lease.release()
				return nil, &ProcessingAuthorityError{Cause: cleanupErr, committed: true}
			}
			return nil, &ProcessingAuthorityError{Cause: processingFailure(ErrProcessingReconciliation, "durable cleanup marker records committed publication", nil), committed: true}
		}
		if !workspacePresent {
			_ = lease.release()
			return nil, processingFailure(ErrProcessingReconciliation, "workspace disappeared while processing guard remains", nil)
		}
	}

	if !guardPresent {
		if ops.beforeInitialWorkspaceMutation != nil {
			if err := ops.beforeInitialWorkspaceMutation(workspace.OutputRoot); err != nil {
				return nil, err
			}
		}
		if err := validateProcessingNamedIdentity("output root before workspace creation", workspace.OutputRoot, rootID); err != nil {
			return nil, err
		}
		if err := ensureProcessingWorkspaceDirectory(workspace.OutputRoot, workspace.Directory); err != nil {
			return nil, err
		}
		if err := validateProcessingNamedIdentity("output root after workspace creation", workspace.OutputRoot, rootID); err != nil {
			return nil, err
		}
		createdWorkspaceID, captureErr := processingPathIdentity(workspace.Directory)
		if captureErr != nil {
			return nil, processingFailure(ErrInvalidProcessingWorkspace, "capture created workspace identity", captureErr)
		}
		if workspacePresent && createdWorkspaceID != workspaceID {
			return nil, processingFailure(ErrProcessingReconciliation, "workspace identity changed during creation", nil)
		}
		workspaceID, workspacePresent = createdWorkspaceID, true
		if err := validateProcessingNamedIdentity("output root before guard creation", workspace.OutputRoot, rootID); err != nil {
			return nil, err
		}
		if err := ensureProcessingGuardDirectory(workspace.OutputRoot, workspace.Directory); err != nil {
			return nil, err
		}
		if err := validateProcessingNamedIdentity("output root after guard creation", workspace.OutputRoot, rootID); err != nil {
			return nil, err
		}
		guardID, guardPresent, err = captureOptionalProcessingIdentity(guardPath)
		if err != nil || !guardPresent {
			return nil, processingFailure(ErrInvalidProcessingWorkspace, "capture created processing-guard identity", err)
		}
		if err := inspectProcessingGuardAuthority(guardPath); err != nil {
			return nil, err
		}
	}
	if guardPresent {
		if err := validateProcessingNamedIdentity("output root before workspace validation", workspace.OutputRoot, rootID); err != nil {
			_ = lease.release()
			return nil, err
		}
		if err := validateOptionalProcessingIdentity("workspace before validation", workspace.Directory, workspaceID, workspacePresent); err != nil {
			_ = lease.release()
			return nil, err
		}
		if err := ensureProcessingWorkspaceDirectory(workspace.OutputRoot, workspace.Directory); err != nil {
			_ = lease.release()
			return nil, err
		}
		if err := validateProcessingNamedIdentity("output root after workspace validation", workspace.OutputRoot, rootID); err != nil {
			_ = lease.release()
			return nil, err
		}
		if err := validateOptionalProcessingIdentity("workspace after validation", workspace.Directory, workspaceID, workspacePresent); err != nil {
			_ = lease.release()
			return nil, err
		}
	}

	if err := validateOptionalProcessingIdentity("workspace before state access", workspace.Directory, workspaceID, workspacePresent); err != nil {
		if lease != nil {
			_ = lease.release()
		}
		return nil, err
	}
	if err := validateOptionalProcessingIdentity("processing guard before state access", guardPath, guardID, guardPresent); err != nil {
		if lease != nil {
			_ = lease.release()
		}
		return nil, err
	}
	if err := validateProcessingNamedIdentity("output root before state access", workspace.OutputRoot, rootID); err != nil {
		if lease != nil {
			_ = lease.release()
		}
		return nil, err
	}
	if lease == nil {
		lease, err = acquireProcessingLease(leasePath)
		if err != nil {
			if errors.Is(err, errProcessingLeaseIdentity) {
				return nil, processingFailure(ErrProcessingReconciliation, "acquire processing lease", err)
			}
			return nil, processingFailure(ErrProcessingBusy, "acquire exclusive processing workspace lease", err)
		}
		if ops.afterProcessingLeaseAcquisition != nil {
			if err := ops.afterProcessingLeaseAcquisition(leasePath); err != nil {
				_ = lease.release()
				return nil, err
			}
		}
		if err := validateProcessingLeaseIdentity("processing lease after acquisition", leasePath, lease.identity); err != nil {
			_ = lease.release()
			return nil, err
		}
		guardLockID, guardLockPresent = lease.guardLockIdentity, true
		if err := validateProcessingLeaseIdentity("processing guard lock after acquisition", guardLockPath, guardLockID); err != nil {
			_ = lease.release()
			return nil, err
		}
	}
	if err := validateProcessingNamedIdentity("output root before state access", workspace.OutputRoot, rootID); err != nil {
		_ = lease.release()
		return nil, err
	}
	if err := validateOptionalProcessingIdentity("workspace before state access", workspace.Directory, workspaceID, workspacePresent); err != nil {
		_ = lease.release()
		return nil, err
	}
	if err := validateOptionalProcessingIdentity("processing guard before state access", guardPath, guardID, guardPresent); err != nil {
		_ = lease.release()
		return nil, err
	}
	if err := validateProcessingLeaseIdentity("processing lease before state access", leasePath, lease.identity); err != nil {
		_ = lease.release()
		return nil, err
	}
	if err := validateProcessingLeaseIdentity("processing guard lock before state access", guardLockPath, guardLockID); err != nil {
		_ = lease.release()
		return nil, err
	}
	stage := filepath.Join(workspace.Directory, "output"+filepath.Ext(destination))
	statePath := filepath.Join(workspace.Directory, "state.json")
	state, fresh, err := openProcessingState(statePath, stage, workspace, ops)
	if err != nil {
		_ = lease.release()
		return nil, err
	}
	if state.Phase == processingPhaseCommitted {
		if err := validateProcessingNamedIdentity("output root before committed cleanup", workspace.OutputRoot, rootID); err != nil {
			_ = lease.release()
			return nil, err
		}
		if err := validateOptionalProcessingIdentity("workspace before committed cleanup", workspace.Directory, workspaceID, workspacePresent); err != nil {
			_ = lease.release()
			return nil, err
		}
		if err := validateOptionalProcessingIdentity("processing guard before committed cleanup", guardPath, guardID, guardPresent); err != nil {
			_ = lease.release()
			return nil, err
		}
		if err := validateProcessingLeaseIdentity("processing lease before committed cleanup", leasePath, lease.identity); err != nil {
			_ = lease.release()
			return nil, err
		}
		if err := validateProcessingLeaseIdentity("processing guard lock before committed cleanup", guardLockPath, guardLockID); err != nil {
			_ = lease.release()
			return nil, err
		}
		if err := inspectProcessingGuardDirectory(guardPath); err != nil {
			_ = lease.release()
			return nil, err
		}
		if err := validateOptionalProcessingIdentity("processing guard after committed cleanup inspection", guardPath, guardID, guardPresent); err != nil {
			_ = lease.release()
			return nil, err
		}
		cleanupErr := cleanupCommittedProcessingWorkspace(workspace.Directory, statePath, cleanupPath, leasePath, lease, workspace, processingCleanupIdentities{
			rootID: rootID, workspaceID: workspaceID, workspacePresent: workspacePresent,
			guardID: guardID, guardPresent: guardPresent, leaseID: lease.identity, leasePresent: true,
			guardLockID: guardLockID, guardLockPresent: true,
		}, ops)
		if cleanupErr != nil {
			_ = lease.release()
			return nil, &ProcessingAuthorityError{Cause: cleanupErr, committed: true}
		}
		return nil, &ProcessingAuthorityError{Cause: processingFailure(ErrProcessingReconciliation, "durable state records committed publication", nil), committed: true}
	}
	if destinationExists && !overwrite {
		_ = lease.release()
		if state.Phase == processingPhaseCommitted || state.Phase == processingPhasePublishing || state.Phase == processingPhaseIndeterminate {
			return nil, processingPhaseFailure(state.Phase)
		}
		return nil, fmt.Errorf("%w: %s", ErrDestinationExists, destination)
	}
	return &processingPreflight{
		workspace: workspace, destination: destination, stage: stage,
		statePath: statePath, cleanupPath: cleanupPath, leasePath: leasePath,
		guardPath: guardPath, guardLockPath: guardLockPath, finalCleanupPath: finalCleanupPath, state: state, fresh: fresh, lease: lease,
		rootIdentity: rootID, workspaceID: workspaceID, guardID: guardID, leaseID: lease.identity,
		guardLockID: guardLockID,
	}, nil
}

func (tools *Toolset) executeProcessingWorkspace(
	ctx context.Context,
	overwrite bool,
	sink events.Sink,
	preflight *processingPreflight,
	prepare func(string) error,
	operation func(string) []string,
	ops processingWorkspaceOps,
) error {
	if sink == nil {
		sink = events.Nop()
	}
	defer preflight.lease.release()
	state := preflight.state
	stage := preflight.stage
	statePath := preflight.statePath
	workspace := preflight.workspace
	destination := preflight.destination
	fresh := preflight.fresh
	if err := validateProcessingPreflightIdentity(preflight); err != nil {
		return err
	}
	if fresh || state.Phase == processingPhaseRunning {
		_ = hardenProcessingStage(stage)
		if err := discardIncompleteProcessingOutput(stage, ops); err != nil {
			return err
		}
		if !fresh {
			state.Output = nil
		}
		args := []string{"-nostdin", "-hide_banner", "-loglevel", "error", "-y"}
		args = append(args, operation(stage)...)
		if err := sink.Emit(ctx, events.Event{Kind: events.KindPostprocessStarting, Path: destination}); err != nil {
			return err
		}
		var totalSize int64
		_, runErr := tools.execute(ctx, tools.ffmpeg, args, func(line string) error {
			key, value, found := strings.Cut(line, "=")
			if !found {
				return nil
			}
			if key == "total_size" {
				_, _ = fmt.Sscan(value, &totalSize)
			}
			if key == "progress" {
				return sink.Emit(ctx, events.Event{Kind: events.KindPostprocessProgress, Path: destination, Bytes: totalSize, Message: value})
			}
			return nil
		})
		if runErr != nil {
			_ = hardenProcessingStage(stage)
			cleanupErr := discardIncompleteProcessingOutput(stage, ops)
			if ctxErr := ctx.Err(); ctxErr != nil {
				return errors.Join(ctxErr, cleanupErr)
			}
			return errors.Join(runErr, cleanupErr)
		}
		if err := ctx.Err(); err != nil {
			_ = hardenProcessingStage(stage)
			_ = discardIncompleteProcessingOutput(stage, ops)
			return err
		}
		if prepare != nil {
			if err := prepare(stage); err != nil {
				_ = hardenProcessingStage(stage)
				_ = discardIncompleteProcessingOutput(stage, ops)
				return fmt.Errorf("%w: prepare output: %v", ErrMediaFailure, err)
			}
		}
		if err := hardenProcessingStage(stage); err != nil {
			return processingFailure(ErrInvalidProcessingWorkspace, "processing output could not be protected", err)
		}
		artifact, err := inspectProcessingArtifact(stage)
		if err != nil {
			return err
		}
		state.Phase = processingPhaseOutputComplete
		state.Output = &artifact
		if err := writeProcessingState(statePath, state, ops.writeAtomic); err != nil {
			return classifyProcessingStateCommit(workspace.Directory, "record complete processing output", err)
		}
	}
	if state.Phase == processingPhaseOutputComplete {
		if state.Output == nil {
			return processingFailure(ErrInvalidProcessingWorkspace, "output-complete state lacks artifact metadata", nil)
		}
		artifact, err := inspectProcessingArtifact(stage)
		if err != nil || artifact != *state.Output {
			return processingFailure(ErrProcessingReconciliation, "complete processing output does not match durable metadata", err)
		}
	} else {
		return processingPhaseFailure(state.Phase)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	state.Phase = processingPhasePublishing
	if err := writeProcessingState(statePath, state, ops.writeAtomic); err != nil {
		return classifyProcessingStateCommit(workspace.Directory, "enter publication boundary", err)
	}
	if err := validateProcessingPreflightIdentity(preflight); err != nil {
		return settleProcessingPublicationFailure(statePath, workspace.Directory, state, &processingPublicationError{operation: "revalidate processing identities", cause: err}, ops)
	}
	if _, err := prepareProcessingDestinationWithAuthority(workspace.OutputRoot, preflight.rootIdentity, preflight.destination, overwrite, ops.afterDestinationParentCreation); err != nil {
		return settleProcessingPublicationFailure(statePath, workspace.Directory, state, &processingPublicationError{operation: "prepare destination", cause: err}, ops)
	}
	if err := validateProcessingPreflightIdentity(preflight); err != nil {
		return settleProcessingPublicationFailure(statePath, workspace.Directory, state, &processingPublicationError{operation: "revalidate processing identities before publication", cause: err}, ops)
	}
	if err := ctx.Err(); err != nil {
		return settleProcessingCancellation(statePath, workspace.Directory, state, err, ops)
	}
	var publicationErr error
	if overwrite {
		publicationErr = ops.replaceAtomic(stage, destination)
	} else {
		publicationErr = ops.publishNoClobber(stage, destination)
	}
	if publicationErr != nil {
		return settleProcessingPublicationFailure(statePath, workspace.Directory, state, publicationErr, ops)
	}
	state.Phase = processingPhaseCommitted
	if err := writeProcessingState(statePath, state, ops.writeAtomic); err != nil {
		return &ProcessingAuthorityError{Cause: errors.Join(errors.New("record committed publication"), err), committed: true}
	}
	if err := cleanupCommittedProcessingWorkspace(workspace.Directory, statePath, preflight.cleanupPath, preflight.leasePath, preflight.lease, workspace, preflight.cleanupIdentities(), ops); err != nil {
		return &ProcessingAuthorityError{Cause: err, committed: true}
	}
	_ = sink.Emit(ctx, events.Event{Kind: events.KindPostprocessCompleted, Path: destination, Bytes: state.Output.Bytes})
	return nil
}

func validateProcessingPreflightIdentity(preflight *processingPreflight) error {
	if preflight.lease == nil || preflight.lease.identity == "" || preflight.lease.identity != preflight.leaseID {
		return processingFailure(ErrProcessingReconciliation, "processing lease handle identity changed during processing", nil)
	}
	if err := preflight.lease.validateLockedIdentity(true, true); err != nil {
		return processingFailure(ErrProcessingReconciliation, "processing lease handle identity changed during processing", err)
	}
	for _, identity := range []struct {
		label    string
		path     string
		expected string
	}{
		{label: "output root", path: preflight.workspace.OutputRoot, expected: preflight.rootIdentity},
		{label: "workspace", path: preflight.workspace.Directory, expected: preflight.workspaceID},
		{label: "processing guard", path: preflight.guardPath, expected: preflight.guardID},
	} {
		if err := validateProcessingNamedIdentity(identity.label+" during processing", identity.path, identity.expected); err != nil {
			return err
		}
	}
	if err := validateProcessingLeaseIdentity("processing lease during processing", preflight.leasePath, preflight.leaseID); err != nil {
		return err
	}
	if err := validateProcessingLeaseIdentity("processing guard lock during processing", preflight.guardLockPath, preflight.guardLockID); err != nil {
		return err
	}
	if err := inspectProcessingGuardDirectory(preflight.guardPath); err != nil {
		return err
	}
	return validateProcessingNamedIdentity("processing guard after authority inspection", preflight.guardPath, preflight.guardID)
}

func recoverProcessingFinalCleanup(workspace ProcessingWorkspace, rootID, guardLockPath, guardLockID, finalCleanupPath, finalCleanupID string, finalCleanupPresent bool, ops processingWorkspaceOps) (result error) {
	if err := validateProcessingNamedIdentity("output root before final cleanup recovery", workspace.OutputRoot, rootID); err != nil {
		return err
	}
	if err := validateOptionalProcessingLeaseIdentity("final cleanup recovery lock before acquisition", guardLockPath, guardLockID, true); err != nil {
		return err
	}
	recovery, err := acquireProcessingGuardRecovery(guardLockPath)
	if err != nil {
		return processingFailure(ErrProcessingReconciliation, "acquire final cleanup recovery authority", err)
	}
	if recovery.identity != guardLockID {
		_ = recovery.release()
		return processingFailure(ErrProcessingReconciliation, "final cleanup recovery lock identity changed before acquisition", nil)
	}
	defer func() {
		if releaseErr := recovery.release(); releaseErr != nil {
			result = errors.Join(result, fmt.Errorf("release final cleanup recovery lock: %w", releaseErr))
		}
	}()
	lockPresent := true
	validate := func() error {
		if err := validateProcessingNamedIdentity("output root during final cleanup recovery", workspace.OutputRoot, rootID); err != nil {
			return err
		}
		if err := recovery.validate(); err != nil {
			return processingFailure(ErrProcessingReconciliation, "final cleanup recovery lock identity changed", err)
		}
		return validateOptionalProcessingLeaseIdentity("final cleanup recovery lock path", guardLockPath, recovery.identity, lockPresent)
	}
	if err := validate(); err != nil {
		return err
	}
	markerSeen, err := inspectProcessingCleanupMarker(finalCleanupPath, workspace)
	if err != nil {
		return err
	}
	markerID, markerPresent, err := captureOptionalProcessingIdentity(finalCleanupPath)
	if err != nil {
		return processingFailure(ErrProcessingReconciliation, "capture final cleanup recovery marker", err)
	}
	if markerSeen != finalCleanupPresent || (markerSeen && markerID != finalCleanupID) {
		return processingFailure(ErrProcessingReconciliation, "final cleanup marker identity changed before recovery", nil)
	}
	lockComplete, err := inspectProcessingLeaseCompletion(guardLockPath, workspace)
	if err != nil {
		return err
	}
	if !lockComplete {
		return processingFailure(ErrProcessingReconciliation, "guard lock lacks completed cleanup authority", nil)
	}
	if markerSeen {
		if err := validateOptionalProcessingIdentity("final cleanup recovery marker", finalCleanupPath, finalCleanupID, markerPresent); err != nil {
			return err
		}
		if ops.beforeCleanupMutation != nil {
			if err := ops.beforeCleanupMutation(finalCleanupPath); err != nil {
				return err
			}
		}
		if err := validateOptionalProcessingIdentity("final cleanup recovery marker before removal", finalCleanupPath, finalCleanupID, true); err != nil {
			return err
		}
		if err := validate(); err != nil {
			return err
		}
		if err := ops.remove(finalCleanupPath); err != nil {
			return fmt.Errorf("remove recovered final processing cleanup marker: %w", err)
		}
		if err := validate(); err != nil {
			return err
		}
	}
	if ops.beforeCleanupMutation != nil {
		if err := ops.beforeCleanupMutation(guardLockPath); err != nil {
			return err
		}
	}
	if err := validate(); err != nil {
		return err
	}
	if err := recovery.removeNamedPath(); err != nil {
		return fmt.Errorf("remove recovered processing guard lock: %w", err)
	}
	lockPresent = false
	if err := validate(); err != nil {
		return err
	}
	if err := ops.syncDirectory(filepath.Dir(guardLockPath)); err != nil {
		return fmt.Errorf("durably remove recovered processing guard lock: %w", err)
	}
	return validate()
}

func validateProcessingWorkspace(workspace ProcessingWorkspace, destination string) error {
	for label, value := range map[string]string{
		"operation identity": workspace.OperationIdentity,
		"input fingerprint":  workspace.InputFingerprint,
	} {
		if len(value) == 0 || len(value) > maxProcessingIdentity || !utf8.ValidString(value) || strings.IndexFunc(value, unicode.IsControl) >= 0 {
			return processingFailure(ErrInvalidProcessingWorkspace, label+" is missing, oversized, or contains invalid text", nil)
		}
	}
	for label, value := range map[string]string{"output root": workspace.OutputRoot, "workspace": workspace.Directory} {
		if len(value) == 0 || len(value) > maxProcessingPath || !filepath.IsAbs(value) || filepath.Clean(value) != value || strings.IndexFunc(value, unicode.IsControl) >= 0 {
			return processingFailure(ErrInvalidProcessingWorkspace, label+" must be a canonical absolute path", nil)
		}
	}
	if len(destination) == 0 || len(destination) > maxProcessingPath || !filepath.IsAbs(destination) || filepath.Clean(destination) != destination || strings.IndexFunc(destination, unicode.IsControl) >= 0 {
		return processingFailure(ErrInvalidProcessingWorkspace, "destination must be a canonical absolute path", nil)
	}
	relative, err := filepath.Rel(workspace.OutputRoot, workspace.Directory)
	if err != nil || relative == "." || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return processingFailure(ErrInvalidProcessingWorkspace, "workspace escapes output root", err)
	}
	guardPath := processingGuardPath(workspace.Directory)
	guardLockPath := processingGuardLockPath(workspace.Directory)
	finalCleanupPath := processingFinalCleanupMarkerPath(workspace.Directory)
	if processingPathContains(workspace.Directory, destination) || processingPathContains(destination, workspace.Directory) || processingPathContains(guardPath, destination) || processingPathContains(destination, guardPath) || processingPathContains(guardLockPath, destination) || processingPathContains(destination, guardLockPath) || processingPathContains(finalCleanupPath, destination) || processingPathContains(destination, finalCleanupPath) {
		return processingFailure(ErrInvalidProcessingWorkspace, "workspace and destination must be disjoint", nil)
	}
	if !processingPathContains(workspace.OutputRoot, destination) {
		return processingFailure(ErrInvalidProcessingWorkspace, "destination escapes output root", nil)
	}
	for label, path := range map[string]string{
		"guard":                processingGuardPath(workspace.Directory),
		"guard lock":           processingGuardLockPath(workspace.Directory),
		"cleanup marker":       processingCleanupMarkerPath(workspace.Directory),
		"final cleanup marker": processingFinalCleanupMarkerPath(workspace.Directory),
		"lease":                processingLeasePath(workspace.Directory),
	} {
		if len(path) > maxProcessingPath || strings.IndexFunc(path, unicode.IsControl) >= 0 {
			return processingFailure(ErrInvalidProcessingWorkspace, label+" path is oversized or invalid", nil)
		}
	}
	return nil
}

func inspectProcessingRoot(root string) error {
	if len(root) == 0 || len(root) > maxProcessingPath || strings.IndexFunc(root, unicode.IsControl) >= 0 {
		return processingFailure(ErrInvalidProcessingWorkspace, "output root path is invalid or oversized", nil)
	}
	info, err := os.Lstat(root)
	if err != nil {
		return processingFailure(ErrInvalidProcessingWorkspace, "inspect output root", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return processingFailure(ErrInvalidProcessingWorkspace, "output root is a symlink or non-directory", nil)
	}
	if reparse, err := processingPathIsReparse(root); err != nil || reparse {
		return processingFailure(ErrInvalidProcessingWorkspace, "output root is a reparse point", err)
	}
	return nil
}

func inspectProcessingDestination(root, destination string) (bool, error) {
	if err := inspectProcessingOutputDirectory(root, filepath.Dir(destination)); err != nil {
		return false, err
	}
	info, err := os.Lstat(destination)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return false, processingFailure(ErrInvalidProcessingWorkspace, "destination is a symlink or non-regular file", nil)
	}
	if reparse, err := processingPathIsReparse(destination); err != nil || reparse {
		return false, processingFailure(ErrInvalidProcessingWorkspace, "destination is a reparse point", err)
	}
	return true, nil
}

// validateProcessingDestination is retained as a read-only test seam. It
// deliberately never creates destination parents; publication uses
// prepareProcessingDestination at the commit boundary.
func validateProcessingDestination(root, destination string) (bool, error) {
	return inspectProcessingDestination(root, destination)
}

func inspectProcessingOutputDirectory(root, directory string) error {
	relative, err := filepath.Rel(root, directory)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return processingFailure(ErrInvalidProcessingWorkspace, "destination directory escapes output root", err)
	}
	current := root
	for _, part := range strings.Split(relative, string(filepath.Separator)) {
		if part == "" || part == "." {
			continue
		}
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return processingFailure(ErrInvalidProcessingWorkspace, "destination directory chain contains a symlink or non-directory", nil)
		}
		if reparse, err := processingPathIsReparse(current); err != nil || reparse {
			return processingFailure(ErrInvalidProcessingWorkspace, "destination directory is a reparse point", err)
		}
	}
	return nil
}

func ensureProcessingOutputDirectory(root, directory string) error {
	if err := inspectProcessingRoot(root); err != nil {
		return err
	}
	relative, err := filepath.Rel(root, directory)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return processingFailure(ErrInvalidProcessingWorkspace, "destination directory escapes output root", err)
	}
	current := root
	for _, part := range strings.Split(relative, string(filepath.Separator)) {
		if part == "" || part == "." {
			continue
		}
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if errors.Is(err, os.ErrNotExist) {
			if err := os.Mkdir(current, 0o755); err != nil && !errors.Is(err, os.ErrExist) {
				return err
			}
			info, err = os.Lstat(current)
		}
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return processingFailure(ErrInvalidProcessingWorkspace, "destination directory chain contains a symlink or non-directory", nil)
		}
		if reparse, err := processingPathIsReparse(current); err != nil || reparse {
			return processingFailure(ErrInvalidProcessingWorkspace, "destination directory is a reparse point", err)
		}
	}
	return nil
}

func prepareProcessingDestination(root, destination string, overwrite bool) (bool, error) {
	return prepareProcessingDestinationWithAuthority(root, "", destination, overwrite, nil)
}

func prepareProcessingDestinationWithAuthority(root, rootID, destination string, overwrite bool, afterParentCreation func(string) error) (bool, error) {
	if rootID != "" {
		if err := validateProcessingNamedIdentity("output root before destination parent creation", root, rootID); err != nil {
			return false, err
		}
	}
	if err := ensureProcessingOutputDirectory(root, filepath.Dir(destination)); err != nil {
		return false, err
	}
	if afterParentCreation != nil {
		if err := afterParentCreation(filepath.Dir(destination)); err != nil {
			return false, err
		}
	}
	if rootID != "" {
		// The directory walk is checked before and after creation. A same-user
		// replacement concurrent with an individual mkdir syscall cannot be
		// excluded portably without a parent-directory handle API; the
		// post-walk identity check fails closed and publication never follows.
		if err := validateProcessingNamedIdentity("output root after destination parent creation", root, rootID); err != nil {
			return false, err
		}
	}
	destinationExists, err := inspectProcessingDestination(root, destination)
	if err != nil {
		return false, err
	}
	if destinationExists && !overwrite {
		return true, fmt.Errorf("%w: %s", ErrDestinationExists, destination)
	}
	return destinationExists, nil
}

func processingPathContains(parent, child string) bool {
	relative, err := filepath.Rel(parent, child)
	return err == nil && (relative == "." || (relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))))
}

func processingGuardPath(workspace string) string { return workspace + processingGuardName }
func processingGuardLockPath(workspace string) string {
	return processingGuardPath(workspace) + processingGuardLockSuffix
}
func processingFinalCleanupMarkerPath(workspace string) string {
	return workspace + processingCleanupName
}
func processingCleanupMarkerPath(workspace string) string {
	return filepath.Join(processingGuardPath(workspace), processingCleanupName)
}
func processingLeasePath(workspace string) string {
	return filepath.Join(processingGuardPath(workspace), processingLeaseName)
}

func inspectProcessingGuardDirectory(path string) error {
	return inspectProcessingGuardDirectoryWithEvidence(path, true)
}

func inspectProcessingGuardAuthority(path string) error {
	return inspectProcessingGuardDirectoryWithEvidence(path, false)
}

func inspectProcessingGuardDirectoryWithEvidence(path string, validateEvidence bool) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return processingFailure(ErrProcessingReconciliation, "processing guard is a symlink or non-directory", nil)
	}
	if reparse, err := processingPathIsReparse(path); err != nil || reparse {
		return processingFailure(ErrProcessingReconciliation, "processing guard is a reparse point", err)
	}
	protected, err := processingDirectoryProtected(path, info)
	if err != nil {
		return err
	}
	if !protected {
		return processingFailure(ErrProcessingReconciliation, "processing guard is not owner-only", nil)
	}
	entries, err := readProcessingWorkspaceEntries(path)
	if err != nil {
		return err
	}
	if len(entries) > maxProcessingEntries {
		return processingFailure(ErrProcessingReconciliation, "processing guard contains too much evidence", nil)
	}
	for _, entry := range entries {
		name := entry.Name()
		if name != processingCleanupName && name != processingLeaseName {
			return processingFailure(ErrProcessingReconciliation, "processing guard contains unknown evidence", nil)
		}
		if validateEvidence {
			if err := validateProcessingEvidence(filepath.Join(path, name)); err != nil {
				return processingFailure(ErrInvalidProcessingWorkspace, "processing guard evidence is not private and regular", err)
			}
		}
	}
	return nil
}

func inspectProcessingCleanupMarker(path string, workspace ProcessingWorkspace) (bool, error) {
	if len(path) > maxProcessingPath {
		return false, processingFailure(ErrInvalidProcessingWorkspace, "cleanup marker path is oversized", nil)
	}
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return false, processingFailure(ErrProcessingReconciliation, "cleanup marker is a symlink or non-regular", nil)
	}
	encoded, err := readProcessingEvidence(path, maxProcessingState)
	if err != nil {
		return false, processingFailure(ErrProcessingReconciliation, "cleanup marker cannot be safely read", err)
	}
	var marker processingCleanupMarker
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&marker); err != nil {
		return false, processingFailure(ErrProcessingReconciliation, "cleanup marker is corrupt", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return false, processingFailure(ErrProcessingReconciliation, "cleanup marker has trailing data", err)
	}
	if marker.Version != processingStateVersion || marker.Phase != processingCleanupPhase || marker.OperationIdentity != workspace.OperationIdentity || marker.InputFingerprint != workspace.InputFingerprint {
		return false, processingFailure(ErrProcessingReconciliation, "cleanup marker authority does not match this operation", nil)
	}
	return true, nil
}

func writeProcessingCleanupMarker(path string, workspace ProcessingWorkspace, write func(string, os.FileMode, func(io.Writer) error) error) error {
	marker := processingCleanupMarker{Version: processingStateVersion, OperationIdentity: workspace.OperationIdentity, InputFingerprint: workspace.InputFingerprint, Phase: processingCleanupPhase}
	encoded, err := json.Marshal(marker)
	if err != nil {
		return err
	}
	return write(path, 0o600, func(writer io.Writer) error {
		_, err := writer.Write(encoded)
		return err
	})
}

func markProcessingLeaseComplete(file *os.File, workspace ProcessingWorkspace) error {
	marker := processingLeaseMarker{Version: processingStateVersion, OperationIdentity: workspace.OperationIdentity, InputFingerprint: workspace.InputFingerprint, Phase: processingCleanupPhase}
	encoded, err := json.Marshal(marker)
	if err != nil {
		return err
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return err
	}
	if err := file.Truncate(0); err != nil {
		return err
	}
	if _, err := file.Write(encoded); err != nil {
		return err
	}
	return file.Sync()
}

func inspectProcessingLeaseCompletion(path string, workspace ProcessingWorkspace) (bool, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return false, processingFailure(ErrProcessingReconciliation, "processing lease evidence is a symlink or non-regular", nil)
	}
	encoded, err := readProcessingEvidence(path, maxProcessingState)
	if err != nil {
		return false, processingFailure(ErrProcessingReconciliation, "processing lease evidence cannot be safely read", err)
	}
	if len(encoded) == 0 {
		return false, nil
	}
	var marker processingLeaseMarker
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&marker); err != nil {
		return false, processingFailure(ErrProcessingReconciliation, "processing lease evidence is corrupt", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return false, processingFailure(ErrProcessingReconciliation, "processing lease evidence has trailing data", err)
	}
	if marker.Version != processingStateVersion || marker.Phase != processingCleanupPhase || marker.OperationIdentity != workspace.OperationIdentity || marker.InputFingerprint != workspace.InputFingerprint {
		return false, processingFailure(ErrProcessingReconciliation, "processing lease evidence does not match this operation", nil)
	}
	return true, nil
}

func openProcessingState(statePath, stage string, workspace ProcessingWorkspace, ops processingWorkspaceOps) (processingState, bool, error) {
	entries, err := readProcessingWorkspaceEntries(workspace.Directory)
	if err != nil {
		return processingState{}, false, err
	}
	if len(entries) > maxProcessingEntries {
		return processingState{}, false, processingFailure(ErrProcessingReconciliation, "workspace contains too much evidence", nil)
	}
	allowedStage := filepath.Base(stage)
	for _, entry := range entries {
		name := entry.Name()
		if len(name) == 0 || len(name) > 255 || strings.IndexFunc(name, unicode.IsControl) >= 0 {
			return processingState{}, false, processingFailure(ErrInvalidProcessingWorkspace, "workspace evidence name is invalid", nil)
		}
		if name != "state.json" && name != allowedStage && name != "reconcile.json" && !strings.HasPrefix(name, ".atomic-") {
			return processingState{}, false, processingFailure(ErrProcessingReconciliation, "workspace contains unknown evidence", nil)
		}
		if name == "reconcile.json" || strings.HasPrefix(name, ".atomic-") {
			if err := validateProcessingEvidence(filepath.Join(workspace.Directory, name)); err != nil {
				return processingState{}, false, processingFailure(ErrInvalidProcessingWorkspace, "retained atomic evidence is not private and regular", err)
			}
			return processingState{}, false, processingFailure(ErrProcessingReconciliation, "workspace retains atomic reconciliation evidence", nil)
		}
		info, infoErr := entry.Info()
		if infoErr != nil {
			return processingState{}, false, infoErr
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return processingState{}, false, processingFailure(ErrInvalidProcessingWorkspace, "workspace evidence is a symlink or non-regular file", nil)
		}
		if links, linkErr := processingFileLinkCount(filepath.Join(workspace.Directory, name)); linkErr != nil || links != 1 {
			return processingState{}, false, processingFailure(ErrInvalidProcessingWorkspace, "workspace evidence has unexpected hard links", linkErr)
		}
	}
	encoded, err := readProcessingEvidence(statePath, maxProcessingState)
	if errors.Is(err, os.ErrNotExist) {
		if len(entries) != 0 {
			return processingState{}, false, processingFailure(ErrProcessingReconciliation, "workspace state is missing while evidence remains", nil)
		}
		state := processingState{Version: processingStateVersion, OperationIdentity: workspace.OperationIdentity, InputFingerprint: workspace.InputFingerprint, Phase: processingPhaseRunning}
		if err := writeProcessingState(statePath, state, ops.writeAtomic); err != nil {
			return processingState{}, false, classifyProcessingStateCommit(workspace.Directory, "initialize processing state", err)
		}
		return state, true, nil
	}
	if err != nil {
		return processingState{}, false, err
	}
	state, decodeErr := decodeProcessingState(encoded)
	if decodeErr != nil {
		return processingState{}, false, decodeErr
	}
	if err := validateProcessingState(state); err != nil {
		return processingState{}, false, err
	}
	if state.OperationIdentity != workspace.OperationIdentity || state.InputFingerprint != workspace.InputFingerprint {
		return processingState{}, false, processingFailure(ErrProcessingReconciliation, "processing identity or input fingerprint changed", nil)
	}
	return state, false, nil
}

func decodeProcessingState(encoded []byte) (processingState, error) {
	var state processingState
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&state); err != nil {
		return processingState{}, processingFailure(ErrInvalidProcessingWorkspace, "processing state is corrupt", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return processingState{}, processingFailure(ErrInvalidProcessingWorkspace, "processing state has trailing data", err)
	}
	if err := validateProcessingState(state); err != nil {
		return processingState{}, err
	}
	return state, nil
}

func readProcessingWorkspaceEntries(directory string) ([]os.DirEntry, error) {
	file, err := openProcessingDirectory(directory)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	entries, err := file.ReadDir(maxProcessingEntries + 1)
	if err != nil && err != io.EOF {
		return nil, err
	}
	return entries, nil
}

func validateProcessingState(state processingState) error {
	for label, value := range map[string]string{
		"operation identity": state.OperationIdentity,
		"input fingerprint":  state.InputFingerprint,
	} {
		if len(value) == 0 || len(value) > maxProcessingIdentity || !utf8.ValidString(value) || strings.IndexFunc(value, unicode.IsControl) >= 0 {
			return processingFailure(ErrInvalidProcessingWorkspace, label+" in state is invalid", nil)
		}
	}
	if state.Version != processingStateVersion {
		return processingFailure(ErrInvalidProcessingWorkspace, "processing state version is invalid", nil)
	}
	switch state.Phase {
	case processingPhaseRunning:
		if state.Output != nil {
			return processingFailure(ErrInvalidProcessingWorkspace, "running state contains output metadata", nil)
		}
	case processingPhaseOutputComplete, processingPhasePublishing, processingPhaseCommitted, processingPhaseIndeterminate:
		if state.Output == nil || !validateProcessingArtifact(*state.Output) {
			return processingFailure(ErrInvalidProcessingWorkspace, "processing state artifact metadata is invalid", nil)
		}
	default:
		return processingFailure(ErrInvalidProcessingWorkspace, "processing state phase is invalid", nil)
	}
	return nil
}

func writeProcessingState(path string, state processingState, write func(string, os.FileMode, func(io.Writer) error) error) error {
	if err := validateProcessingState(state); err != nil {
		return err
	}
	encoded, err := json.Marshal(state)
	if err != nil {
		return err
	}
	if len(encoded) > maxProcessingState {
		return processingFailure(ErrInvalidProcessingWorkspace, "processing state exceeds size limit", nil)
	}
	return write(path, 0o600, func(writer io.Writer) error {
		_, err := writer.Write(encoded)
		return err
	})
}

func inspectProcessingArtifact(path string) (processingArtifact, error) {
	return processingArtifactDigest(path)
}

func discardIncompleteProcessingOutput(stage string, ops processingWorkspaceOps) error {
	if _, err := os.Lstat(stage); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return err
	}
	if err := validateProcessingEvidence(stage); err != nil {
		return processingFailure(ErrInvalidProcessingWorkspace, "incomplete output evidence is not private and regular", err)
	}
	if err := ops.remove(stage); err != nil {
		return err
	}
	return ops.syncDirectory(filepath.Dir(stage))
}

func classifyProcessingStateCommit(workDir, detail string, err error) error {
	var commitErr atomicfile.CommitError
	if errors.As(err, &commitErr) && (commitErr.Committed() || commitErr.Indeterminate()) {
		markerErr := writeProcessingReconciliationMarker(workDir, detail)
		return processingFailure(ErrProcessingReconciliation, detail, errors.Join(err, markerErr))
	}
	return err
}

func writeProcessingReconciliationMarker(workDir, reason string) error {
	if len(reason) == 0 || len(reason) > maxProcessingIdentity || strings.IndexFunc(reason, unicode.IsControl) >= 0 {
		return processingFailure(ErrInvalidProcessingWorkspace, "reconciliation reason is invalid", nil)
	}
	return atomicfile.Write(filepath.Join(workDir, "reconcile.json"), 0o600, func(writer io.Writer) error {
		return json.NewEncoder(writer).Encode(struct {
			Version int    `json:"version"`
			Reason  string `json:"reason"`
		}{Version: processingStateVersion, Reason: reason})
	})
}

func settleProcessingPublicationFailure(statePath, workDir string, state processingState, publicationErr error, ops processingWorkspaceOps) error {
	var commitErr atomicfile.CommitError
	if errors.As(publicationErr, &commitErr) {
		switch {
		case commitErr.Committed():
			state.Phase = processingPhaseCommitted
			stateErr := writeProcessingState(statePath, state, ops.writeAtomic)
			return &ProcessingAuthorityError{Cause: errors.Join(publicationErr, stateErr), committed: true}
		case commitErr.Indeterminate():
			state.Phase = processingPhaseIndeterminate
			stateErr := writeProcessingState(statePath, state, ops.writeAtomic)
			return &ProcessingAuthorityError{Cause: errors.Join(publicationErr, stateErr), indeterminate: true}
		}
	}
	state.Phase = processingPhaseOutputComplete
	if err := writeProcessingState(statePath, state, ops.writeAtomic); err != nil {
		return processingFailure(ErrProcessingReconciliation, "restore retryable output-complete state after pre-commit publication failure", errors.Join(publicationErr, err))
	}
	return fmt.Errorf("%w: publish processing output: %v", ErrMediaFailure, publicationErr)
}

func settleProcessingCancellation(statePath, workDir string, state processingState, ctxErr error, ops processingWorkspaceOps) error {
	state.Phase = processingPhaseOutputComplete
	if err := writeProcessingState(statePath, state, ops.writeAtomic); err != nil {
		return errors.Join(ctxErr, classifyProcessingStateCommit(workDir, "restore retryable output-complete state after cancellation", err))
	}
	return ctxErr
}

func processingPhaseFailure(phase string) error {
	switch phase {
	case processingPhaseCommitted:
		return &ProcessingAuthorityError{Cause: errors.New("durable state records committed publication"), committed: true}
	case processingPhasePublishing, processingPhaseIndeterminate:
		return &ProcessingAuthorityError{Cause: errors.New("durable state records uncertain publication"), indeterminate: true}
	default:
		return processingFailure(ErrInvalidProcessingWorkspace, "unknown processing phase", nil)
	}
}

func cleanupCommittedProcessingWorkspace(workDir, statePath, cleanupPath, leasePath string, lease *processingLease, workspace ProcessingWorkspace, ids processingCleanupIdentities, ops processingWorkspaceOps) error {
	guardPath := processingGuardPath(workDir)
	guardLockPath := processingGuardLockPath(workDir)
	finalCleanupPath := processingFinalCleanupMarkerPath(workDir)
	leaseHandleHeld := true
	guardLockHeld := true
	finalCleanupID, finalCleanupPresent, err := captureOptionalProcessingIdentity(finalCleanupPath)
	if err != nil {
		return processingFailure(ErrProcessingReconciliation, "capture final cleanup marker identity", err)
	}
	validate := func() error {
		if leaseHandleHeld || guardLockHeld {
			if err := lease.validateLockedIdentity(leaseHandleHeld, guardLockHeld); err != nil {
				return processingFailure(ErrProcessingReconciliation, "processing lease handle identity changed during cleanup", err)
			}
		}
		if err := validateProcessingCleanupIdentities(ids, workspace.OutputRoot, workDir, guardPath, leasePath, guardLockPath); err != nil {
			return err
		}
		return validateOptionalProcessingIdentity("final cleanup marker during cleanup", finalCleanupPath, finalCleanupID, finalCleanupPresent)
	}
	beforeRemove := func(path string) error {
		if ops.beforeCleanupMutation != nil {
			if err := ops.beforeCleanupMutation(path); err != nil {
				return err
			}
		}
		return validate()
	}

	// All cleanup paths are path-based. Identity checks bracket each mutation;
	// they fail closed on a replacement but cannot make an individual unlink
	// syscall a continuous handle-relative operation on every platform.
	if err := validate(); err != nil {
		return err
	}
	if err := writeProcessingCleanupMarker(finalCleanupPath, workspace, ops.writeAtomic); err != nil {
		return fmt.Errorf("record final processing cleanup authority: %w", err)
	}
	var finalCleanupErr error
	finalCleanupID, finalCleanupErr = processingPathIdentity(finalCleanupPath)
	if finalCleanupErr != nil {
		return processingFailure(ErrProcessingReconciliation, "capture final cleanup marker identity", finalCleanupErr)
	}
	finalCleanupPresent = true
	if err := validate(); err != nil {
		return err
	}
	if err := ops.syncDirectory(filepath.Dir(workDir)); err != nil {
		return fmt.Errorf("durably record final processing cleanup authority: %w", err)
	}
	if err := validate(); err != nil {
		return err
	}
	if err := writeProcessingCleanupMarker(cleanupPath, workspace, ops.writeAtomic); err != nil {
		return fmt.Errorf("record processing cleanup authority: %w", err)
	}
	if err := validate(); err != nil {
		return err
	}
	if err := ops.syncDirectory(guardPath); err != nil {
		return fmt.Errorf("durably record processing cleanup authority: %w", err)
	}
	if err := validate(); err != nil {
		return err
	}

	entries, err := readProcessingWorkspaceEntries(workDir)
	if errors.Is(err, os.ErrNotExist) {
		if ids.workspacePresent {
			return processingFailure(ErrProcessingReconciliation, "workspace disappeared before cleanup evidence was settled", nil)
		}
	} else if err != nil {
		return err
	} else {
		statePresent := false
		for _, entry := range entries {
			if entry.Name() == filepath.Base(statePath) {
				statePresent = true
				encoded, readErr := readProcessingEvidence(filepath.Join(workDir, entry.Name()), maxProcessingState)
				if readErr != nil {
					return processingFailure(ErrProcessingReconciliation, "committed state evidence is not safely readable", readErr)
				}
				state, decodeErr := decodeProcessingState(encoded)
				if decodeErr != nil || state.OperationIdentity != workspace.OperationIdentity || state.InputFingerprint != workspace.InputFingerprint {
					return processingFailure(ErrProcessingReconciliation, "committed state evidence is corrupt or mismatched", decodeErr)
				}
				continue
			}
			return processingFailure(ErrProcessingReconciliation, "unknown evidence blocks committed workspace cleanup", nil)
		}
		if err := validate(); err != nil {
			return err
		}
		if err := ops.syncDirectory(workDir); err != nil {
			return err
		}
		if err := validate(); err != nil {
			return err
		}
		if statePresent {
			if err := beforeRemove(statePath); err != nil {
				return err
			}
			if err := ops.remove(statePath); err != nil {
				return err
			}
			if err := validate(); err != nil {
				return err
			}
		}
		if err := ops.syncDirectory(workDir); err != nil {
			return fmt.Errorf("commit processing workspace cleanup: %w", err)
		}
		if err := validate(); err != nil {
			return err
		}
		if err := beforeRemove(workDir); err != nil {
			return err
		}
		if err := ops.remove(workDir); err != nil {
			return fmt.Errorf("remove empty processing workspace: %w", err)
		}
		ids.workspacePresent = false
		if err := validate(); err != nil {
			return err
		}
		if err := ops.syncDirectory(filepath.Dir(workDir)); err != nil {
			return fmt.Errorf("durably remove processing workspace: %w", err)
		}
		if err := validate(); err != nil {
			return err
		}
	}

	if err := validate(); err != nil {
		return err
	}
	if err := lease.markCleanupComplete(workspace); err != nil {
		return fmt.Errorf("record completed processing cleanup: %w", err)
	}
	if err := validate(); err != nil {
		return err
	}
	if err := beforeRemove(cleanupPath); err != nil {
		return err
	}
	if err := ops.remove(cleanupPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("commit processing cleanup marker removal: %w", err)
	}
	if err := validate(); err != nil {
		return err
	}
	if err := ops.syncDirectory(guardPath); err != nil {
		return fmt.Errorf("durably commit processing cleanup: %w", err)
	}
	if err := validate(); err != nil {
		return err
	}
	if err := beforeRemove(leasePath); err != nil {
		return err
	}
	// The platform callback unlinks the name while the OS lease handle is
	// still locked. Releasing first would make a recreated lease path
	// immediately acquirable while marker/guard cleanup is still pending.
	if err := lease.removeNamedPath(); err != nil {
		return fmt.Errorf("remove processing workspace lease: %w", err)
	}
	ids.leasePresent = false
	if err := validate(); err != nil {
		return err
	}
	if err := ops.syncDirectory(guardPath); err != nil {
		return fmt.Errorf("durably remove processing workspace lease: %w", err)
	}
	if err := validate(); err != nil {
		return err
	}
	if err := lease.releaseLeaseHandle(); err != nil {
		return fmt.Errorf("release processing workspace lease: %w", err)
	}
	leaseHandleHeld = false
	if err := validate(); err != nil {
		return err
	}
	if err := beforeRemove(guardPath); err != nil {
		return err
	}
	if err := ops.remove(guardPath); err != nil {
		return fmt.Errorf("remove processing guard: %w", err)
	}
	ids.guardPresent = false
	if err := validate(); err != nil {
		return err
	}
	if err := ops.syncDirectory(filepath.Dir(workDir)); err != nil {
		return fmt.Errorf("durably remove processing guard: %w", err)
	}
	if err := validate(); err != nil {
		return err
	}
	if err := validate(); err != nil {
		return err
	}
	if err := beforeRemove(finalCleanupPath); err != nil {
		return err
	}
	if err := ops.remove(finalCleanupPath); err != nil {
		return fmt.Errorf("remove final processing cleanup marker: %w", err)
	}
	finalCleanupPresent = false
	if err := validate(); err != nil {
		return err
	}
	if err := ops.syncDirectory(filepath.Dir(workDir)); err != nil {
		return fmt.Errorf("durably remove final processing cleanup marker: %w", err)
	}
	if err := validate(); err != nil {
		return err
	}
	if err := beforeRemove(guardLockPath); err != nil {
		return err
	}
	if err := lease.removeGuardLockNamedPath(); err != nil {
		return fmt.Errorf("remove processing guard lock: %w", err)
	}
	ids.guardLockPresent = false
	if err := validate(); err != nil {
		return err
	}
	if err := ops.syncDirectory(filepath.Dir(workDir)); err != nil {
		return fmt.Errorf("durably remove processing guard lock: %w", err)
	}
	if err := lease.releaseGuardLockHandle(); err != nil {
		return fmt.Errorf("release processing guard lock: %w", err)
	}
	guardLockHeld = false
	return validate()
}
