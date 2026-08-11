package session

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/tejasa97/youtube_dlp/internal/atomicfile"
)

const (
	maxCollectedOrphans = 128
	maxOrphanScan       = 1024
	// These limits exceed the manifest's 4096-component ceiling and the
	// 1024-byte checkpoint-path ceiling while keeping orphan cleanup bounded.
	maxDiscardTreeDepth     = 1024
	maxDiscardTreeEntries   = 16384
	discardMarkerName       = ".discarding"
	discardGuardPrefix      = ".discard-guard-"
	discardQuarantinePrefix = ".discard-"
	discardGuardRecordName  = discardMarkerName
	maxDiscardRecordBytes   = 4096
)

type discardFaultPoint string

const (
	discardFaultAfterMarker       discardFaultPoint = "after_marker"
	discardFaultAfterLeaseClose   discardFaultPoint = "after_lease_close"
	discardFaultBeforeRename      discardFaultPoint = "before_rename"
	discardFaultChildRemoval      discardFaultPoint = "child_removal"
	discardFaultDirectorySync     discardFaultPoint = "directory_sync"
	discardFaultQuarantineRemoval discardFaultPoint = "quarantine_removal"
)

var (
	discardAfterMarker      = func() {}
	discardCloseLease       = func(lease *workspaceLease) error { return lease.Close() }
	discardRename           = os.Rename
	discardRemove           = os.Remove
	discardSyncDirectory    = syncDirectory
	discardSyncHandle       = syncDiscardDirectoryHandle
	discardFault            = func(discardFaultPoint) error { return nil }
	discardBeforeRecordOpen = func(string) {}
	discardBeforeChildOpen  = func(string) {}
	discardTreeDepthLimit   = maxDiscardTreeDepth
	discardTreeEntryLimit   = maxDiscardTreeEntries
)

var errDiscardTreeBudget = errors.New("discard workspace cleanup budget exhausted")

// DiscardDisposition is the bounded terminal state of a destructive cleanup
// attempt. CleanupPending means the obligation is durable and restartable;
// ReconciliationRequired means evidence must be preserved for repair.
type DiscardDisposition string

const (
	Discarded             DiscardDisposition = "discarded"
	DiscardCleanupPending DiscardDisposition = "cleanup_pending"
	DiscardReconciliation DiscardDisposition = "reconciliation_required"
)

type discardRecord struct {
	Version           int    `json:"version"`
	SessionID         string `json:"session_id"`
	RootIdentity      string `json:"root_identity"`
	WorkspaceIdentity string `json:"workspace_identity"`
}

type discardHandleState struct {
	path              string
	originalPath      string
	quarantinePath    string
	guardPath         string
	workspaceIdentity string
	record            discardRecord
	cleanupOnly       bool
}

// DiscardHandle holds the workspace lease and a stable, non-deleted guard
// lease for one destructive operation. The guard remains outside the target
// tree until cleanup is complete, including during recursive deletion.
type DiscardHandle struct {
	mu     sync.Mutex
	ref    WorkspaceRef
	state  discardHandleState
	lease  *workspaceLease
	guard  *workspaceLease
	closed bool
}

// OrphanCollection is a bounded result of maintenance. It returns session IDs
// only; malformed evidence is never converted into a deletion success.
type OrphanCollection struct {
	Collected      []string
	CleanupPending []string
	Reconciliation []string
	Skipped        int
	Limited        bool
}

// PrepareDiscard validates and opens a restartable destructive authority. It
// accepts an intact session, a durable marked session, a deterministic
// quarantine, or a guard-only cleanup obligation. Unknown evidence fails
// closed and is never removed.
func PrepareDiscard(ref WorkspaceRef) (*DiscardHandle, error) {
	if err := ref.validate(); err != nil {
		return nil, err
	}
	if err := validateWorkspaceRootIdentity(ref); err != nil {
		return nil, err
	}
	root := filepath.Clean(ref.OutputRoot)
	sessionsRoot := filepath.Join(root, SessionsDirectoryName)
	if err := validateDirectoryChain(root, false); err != nil {
		return nil, err
	}
	if err := validateDirectoryChain(sessionsRoot, true); err != nil {
		return nil, err
	}
	originalPath, err := ref.Path()
	if err != nil {
		return nil, err
	}
	quarantinePath := filepath.Join(sessionsRoot, discardQuarantinePrefix+ref.SessionID)
	guardPath := filepath.Join(sessionsRoot, discardGuardPrefix+ref.SessionID)
	originalExists, originalErr := safeDirectoryExists(originalPath)
	quarantineExists, quarantineErr := safeDirectoryExists(quarantinePath)
	if originalErr != nil || quarantineErr != nil {
		return nil, ErrNeedsReconciliation
	}
	if originalExists && quarantineExists {
		return nil, ErrNeedsReconciliation
	}

	if !originalExists && !quarantineExists {
		guardExists, guardErr := safeDirectoryExists(guardPath)
		if guardErr != nil {
			return nil, ErrNeedsReconciliation
		}
		if !guardExists {
			return nil, ErrWorkspaceUnavailable
		}
		guardRecord, exists, unsafe := readDiscardRecord(filepath.Join(guardPath, discardGuardRecordName))
		if unsafe {
			return nil, ErrNeedsReconciliation
		}
		if !exists {
			if err := validateGuardDirectory(guardPath, false); err != nil {
				return nil, ErrNeedsReconciliation
			}
			guard, err := openEmptyDiscardGuard(guardPath)
			if err != nil {
				return nil, err
			}
			if err := validateWorkspaceRootIdentity(ref); err != nil {
				_ = guard.Close()
				return nil, err
			}
			return &DiscardHandle{ref: ref, guard: guard, state: discardHandleState{
				originalPath: originalPath, quarantinePath: quarantinePath,
				guardPath: guardPath, cleanupOnly: true,
			}}, nil
		}
		if err := validateDiscardRecord(guardRecord, ref); err != nil {
			return nil, ErrNeedsReconciliation
		}
		guard, err := openDiscardGuard(sessionsRoot, guardRecord)
		if err != nil {
			return nil, err
		}
		if err := validateWorkspaceRootIdentity(ref); err != nil {
			_ = guard.Close()
			return nil, err
		}
		return &DiscardHandle{ref: ref, guard: guard, state: discardHandleState{
			originalPath: originalPath, quarantinePath: quarantinePath,
			guardPath: guardPath, record: guardRecord, cleanupOnly: true,
		}}, nil
	}

	targetPath := originalPath
	if quarantineExists {
		targetPath = quarantinePath
	}
	if err := validateDirectoryChain(targetPath, true); err != nil {
		return nil, err
	}
	identity, err := directoryIdentity(targetPath)
	if err != nil {
		return nil, err
	}
	var (
		lease  *workspaceLease
		guard  *workspaceLease
		record discardRecord
	)
	lease, err = acquireWorkspaceLease(filepath.Join(targetPath, LeaseFileName), false, false)
	if err != nil && errors.Is(err, ErrMissingLease) && targetPath == quarantinePath {
		// Once marker-last cleanup has removed the target lease, the durable
		// guard is the remaining destructive authority. Reopen that authority
		// first, then recreate only this quarantine's lease for idempotent
		// completion. An intact/original workspace is never granted this
		// fallback because a missing lease there is ambiguous evidence.
		guardRecord, guardMarker, guardUnsafe := readDiscardRecord(filepath.Join(guardPath, discardGuardRecordName))
		if guardUnsafe || !guardMarker || validateDiscardRecord(guardRecord, ref) != nil || guardRecord.WorkspaceIdentity != identity {
			return nil, ErrNeedsReconciliation
		}
		if err := validateWorkspaceRootIdentity(ref); err != nil {
			return nil, err
		}
		guard, err = openDiscardGuard(sessionsRoot, guardRecord)
		if err != nil {
			return nil, err
		}
		record = guardRecord
		lease, err = acquireWorkspaceLease(filepath.Join(targetPath, LeaseFileName), true, false)
		if err != nil {
			_ = guard.Close()
			return nil, err
		}
	}
	if err != nil {
		return nil, err
	}
	closeWith := func(cause error) (*DiscardHandle, error) {
		_ = lease.Close()
		if guard != nil {
			_ = guard.Close()
		}
		return nil, cause
	}
	if guard == nil {
		var marker bool
		var unsafe bool
		record, marker, unsafe = readDiscardRecord(filepath.Join(targetPath, discardMarkerName))
		if unsafe {
			return closeWith(ErrNeedsReconciliation)
		}
		if marker {
			if err := validateDiscardRecord(record, ref); err != nil || record.WorkspaceIdentity != identity {
				return closeWith(ErrNeedsReconciliation)
			}
		} else {
			if hasAtomicManifestEvidence(targetPath) {
				return closeWith(ErrNeedsReconciliation)
			}
			guardRecord, guardMarker, guardUnsafe := readDiscardRecord(filepath.Join(guardPath, discardGuardRecordName))
			if guardUnsafe {
				return closeWith(ErrNeedsReconciliation)
			}
			if guardMarker {
				if err := validateDiscardRecord(guardRecord, ref); err != nil || guardRecord.WorkspaceIdentity != identity {
					return closeWith(ErrNeedsReconciliation)
				}
				record = guardRecord
			} else {
				manifest, manifestErr := readManifest(filepath.Join(targetPath, ManifestFileName))
				if manifestErr != nil || manifest.SessionID != ref.SessionID ||
					validateManifestDerivedPaths(ref.OutputRoot, targetPath, manifest) != nil ||
					manifest.Status == StatusNeedsReconciliation || manifest.Publication == PublicationIndeterminate || manifest.Cleanup == CleanupIndeterminate {
					return closeWith(ErrNeedsReconciliation)
				}
				record = discardRecord{Version: 1, SessionID: ref.SessionID, RootIdentity: ref.OutputRootIdentity, WorkspaceIdentity: identity}
			}
		}
		if err := validateWorkspaceRootIdentity(ref); err != nil {
			return closeWith(err)
		}
		guard, err = openDiscardGuard(sessionsRoot, record)
		if err != nil {
			return closeWith(err)
		}
	}
	if err := validateWorkspaceRootIdentity(ref); err != nil {
		_ = guard.Close()
		return closeWith(err)
	}
	if current, currentErr := directoryIdentity(targetPath); currentErr != nil || current != identity {
		_ = guard.Close()
		return closeWith(ErrNeedsReconciliation)
	}
	return &DiscardHandle{
		ref: ref, lease: lease, guard: guard,
		state: discardHandleState{path: targetPath, originalPath: originalPath, quarantinePath: quarantinePath,
			guardPath: guardPath, workspaceIdentity: identity, record: record},
	}, nil
}

// Discard settles a prepared operation. Every failure leaves either the
// marked workspace, quarantine, or guard as durable evidence that a later
// PrepareDiscard call can reopen and continue.
func (handle *DiscardHandle) Discard() (DiscardDisposition, error) {
	if handle == nil {
		return DiscardReconciliation, ErrWorkspaceClosed
	}
	handle.mu.Lock()
	defer handle.mu.Unlock()
	if handle.closed {
		return DiscardReconciliation, ErrWorkspaceClosed
	}
	handle.closed = true
	state := handle.state
	if state.cleanupOnly {
		return handle.finishGuardOnly(state)
	}
	if err := validateWorkspaceRootIdentity(handle.ref); err != nil {
		return handle.failWithLeases(DiscardReconciliation, err)
	}
	markerRecord, marker, unsafe := readDiscardRecord(filepath.Join(state.path, discardMarkerName))
	if unsafe || (marker && markerRecord != state.record) {
		return handle.failWithLeases(DiscardReconciliation, ErrNeedsReconciliation)
	} else if !marker {
		if err := writeDiscardMarker(filepath.Join(state.path, discardMarkerName), state.record); err != nil {
			return handle.failWithLeases(DiscardCleanupPending, err)
		}
	}
	if fault := discardFault(discardFaultAfterMarker); fault != nil {
		return handle.failWithLeases(DiscardCleanupPending, fault)
	}
	discardAfterMarker()
	if handle.lease != nil {
		lease := handle.lease
		if err := discardCloseLease(lease); err != nil {
			// A fault seam may report a close failure without releasing the
			// native lock. Always make a best-effort real close before leaving
			// durable marker evidence for the next attempt.
			_ = lease.Close()
			handle.lease = nil
			if handle.guard != nil {
				_ = handle.guard.Close()
			}
			handle.guard = nil
			return DiscardCleanupPending, err
		}
		handle.lease = nil
	}
	if fault := discardFault(discardFaultAfterLeaseClose); fault != nil {
		_ = handle.guard.Close()
		handle.guard = nil
		return DiscardCleanupPending, fault
	}
	if err := validateWorkspaceRootIdentity(handle.ref); err != nil {
		return handle.failWithLeases(DiscardReconciliation, err)
	}
	if state.path == state.originalPath {
		if identity, err := directoryIdentity(state.path); err != nil || identity != state.workspaceIdentity {
			return handle.failWithLeases(DiscardReconciliation, ErrNeedsReconciliation)
		}
		if fault := discardFault(discardFaultBeforeRename); fault != nil {
			_ = handle.guard.Close()
			handle.guard = nil
			return DiscardReconciliation, fault
		}
		if err := discardRename(state.originalPath, state.quarantinePath); err != nil {
			_ = handle.guard.Close()
			handle.guard = nil
			return DiscardReconciliation, err
		}
		state.path = state.quarantinePath
		handle.state.path = state.path
		if err := discardSyncDirectory(filepath.Dir(state.quarantinePath)); err != nil {
			_ = handle.guard.Close()
			handle.guard = nil
			return DiscardCleanupPending, err
		}
	}
	if identity, err := directoryIdentity(state.path); err != nil || identity != state.workspaceIdentity {
		return handle.failWithLeases(DiscardReconciliation, ErrNeedsReconciliation)
	}
	return handle.cleanupTree(state)
}

// Close releases authority without attempting cleanup. It is safe to race
// with Discard and never races the underlying lease fields.
func (handle *DiscardHandle) Close() error {
	if handle == nil {
		return nil
	}
	handle.mu.Lock()
	defer handle.mu.Unlock()
	if handle.closed {
		return nil
	}
	handle.closed = true
	var result error
	if handle.lease != nil {
		result = handle.lease.Close()
		handle.lease = nil
	}
	if handle.guard != nil {
		if err := handle.guard.Close(); result == nil {
			result = err
		}
		handle.guard = nil
	}
	return result
}

func (handle *DiscardHandle) cleanupTree(state discardHandleState) (DiscardDisposition, error) {
	if err := validateWorkspaceRootIdentity(handle.ref); err != nil {
		_ = handle.guard.Close()
		handle.guard = nil
		return DiscardReconciliation, err
	}
	parent, target, err := openDiscardTarget(state.path, state.workspaceIdentity)
	if err != nil {
		_ = handle.guard.Close()
		handle.guard = nil
		if errors.Is(err, ErrNeedsReconciliation) {
			return DiscardReconciliation, err
		}
		return DiscardCleanupPending, err
	}
	closeAuthority := func() error {
		var closeErr error
		if err := target.close(); err != nil {
			closeErr = err
		}
		if err := parent.close(); err != nil && closeErr == nil {
			closeErr = err
		}
		return closeErr
	}
	fail := func(cause error) (DiscardDisposition, error) {
		closeErr := closeAuthority()
		if cause == nil {
			cause = closeErr
		} else if closeErr != nil {
			cause = errors.Join(cause, closeErr)
		}
		_ = handle.guard.Close()
		handle.guard = nil
		if errors.Is(cause, ErrNeedsReconciliation) {
			return DiscardReconciliation, cause
		}
		return DiscardCleanupPending, cause
	}
	targetDirectory := &discardDirectory{file: target.file, path: target.path, identity: target.identity}
	if err := removeDiscardChildrenFromHandle(targetDirectory, &discardTreeBudget{}, true); err != nil {
		return fail(err)
	}
	if err := removeDiscardNamedFile(targetDirectory, LeaseFileName); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fail(err)
	}
	if err := discardSyncHandle(targetDirectory); err != nil {
		return fail(err)
	}
	if err := removeDiscardNamedFile(targetDirectory, discardMarkerName); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fail(err)
	}
	if err := discardSyncHandle(targetDirectory); err != nil {
		return fail(err)
	}
	if err := validateWorkspaceRootIdentity(handle.ref); err != nil {
		return fail(err)
	}
	if fault := discardFault(discardFaultQuarantineRemoval); fault != nil {
		return fail(fault)
	}
	if err := target.remove(); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fail(err)
	}
	if err := target.close(); err != nil {
		return fail(err)
	}
	if err := discardSyncHandle(parent); err != nil {
		return fail(err)
	}
	if err := parent.close(); err != nil {
		_ = handle.guard.Close()
		handle.guard = nil
		return DiscardCleanupPending, err
	}
	if err := handle.removeGuard(state); err != nil {
		return DiscardCleanupPending, err
	}
	return Discarded, nil
}

func (handle *DiscardHandle) removeGuard(state discardHandleState) error {
	if handle.guard != nil {
		if err := handle.guard.Close(); err != nil {
			handle.guard = nil
			return err
		}
		handle.guard = nil
	}
	guardRecordPath := filepath.Join(state.guardPath, discardGuardRecordName)
	if err := removeDiscardFile(guardRecordPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return handle.preserveGuardRecord(state, err)
	}
	if err := removeDiscardFile(filepath.Join(state.guardPath, LeaseFileName)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return handle.preserveGuardRecord(state, err)
	}
	if err := discardSyncDirectory(state.guardPath); err != nil {
		return handle.preserveGuardRecord(state, err)
	}
	if err := discardRemove(state.guardPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return handle.preserveGuardRecord(state, err)
	}
	if err := discardSyncDirectory(filepath.Dir(state.guardPath)); err != nil {
		return err
	}
	return nil
}

func (handle *DiscardHandle) preserveGuardRecord(state discardHandleState, cause error) error {
	if state.record.Version != 0 {
		if exists, err := safeDirectoryExists(state.guardPath); err == nil && exists {
			if writeErr := writeDiscardMarker(filepath.Join(state.guardPath, discardGuardRecordName), state.record); writeErr != nil {
				return errors.Join(cause, ErrNeedsReconciliation)
			}
			_ = discardSyncDirectory(state.guardPath)
		}
	}
	return cause
}

func (handle *DiscardHandle) finishGuardOnly(state discardHandleState) (DiscardDisposition, error) {
	if err := validateWorkspaceRootIdentity(handle.ref); err != nil {
		_ = handle.guard.Close()
		handle.guard = nil
		return DiscardReconciliation, err
	}
	if original, originalErr := safeDirectoryExists(state.originalPath); originalErr != nil {
		if handle.guard != nil {
			_ = handle.guard.Close()
			handle.guard = nil
		}
		return DiscardReconciliation, ErrNeedsReconciliation
	} else if original {
		_ = handle.guard.Close()
		handle.guard = nil
		return DiscardReconciliation, ErrNeedsReconciliation
	}
	if quarantine, quarantineErr := safeDirectoryExists(state.quarantinePath); quarantineErr != nil {
		if handle.guard != nil {
			_ = handle.guard.Close()
			handle.guard = nil
		}
		return DiscardReconciliation, ErrNeedsReconciliation
	} else if quarantine {
		_ = handle.guard.Close()
		handle.guard = nil
		return DiscardReconciliation, ErrNeedsReconciliation
	}
	if err := validateWorkspaceRootIdentity(handle.ref); err != nil {
		_ = handle.guard.Close()
		handle.guard = nil
		return DiscardReconciliation, err
	}
	if err := handle.removeGuard(state); err != nil {
		return DiscardCleanupPending, err
	}
	return Discarded, nil
}

func (handle *DiscardHandle) failWithLeases(disposition DiscardDisposition, cause error) (DiscardDisposition, error) {
	if handle.lease != nil {
		_ = handle.lease.Close()
		handle.lease = nil
	}
	if handle.guard != nil {
		_ = handle.guard.Close()
		handle.guard = nil
	}
	return disposition, cause
}

type discardTreeBudget struct {
	depth   int
	entries int
}

func removeDiscardFile(path string) error {
	if fault := discardFault(discardFaultChildRemoval); fault != nil {
		return fault
	}
	return discardRemove(path)
}

func safeDirectoryExists(path string) (bool, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() || !ownerOnlyDirectoryAt(path, info) {
		return false, ErrUnsafePath
	}
	return true, nil
}

func validateDiscardRecord(record discardRecord, ref WorkspaceRef) error {
	if record.Version != 1 || record.SessionID != ref.SessionID || record.RootIdentity != ref.OutputRootIdentity || !validRootIdentity(record.RootIdentity) || !validDiscardIdentity(record.WorkspaceIdentity) {
		return ErrInvalidManifest
	}
	return nil
}

func validDiscardIdentity(identity string) bool {
	return validRootIdentity(identity)
}

func readDiscardRecord(path string) (discardRecord, bool, bool) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return discardRecord{}, false, false
	}
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || !ownerOnlyFileAt(path, info) || info.Size() > maxDiscardRecordBytes {
		return discardRecord{}, true, true
	}
	discardBeforeRecordOpen(path)
	file, err := openDiscardRecordFile(path, info)
	if err != nil {
		return discardRecord{}, true, true
	}
	defer file.Close()
	encoded, err := io.ReadAll(io.LimitReader(file, maxDiscardRecordBytes+1))
	if err != nil || len(encoded) > maxDiscardRecordBytes {
		return discardRecord{}, true, true
	}
	var record discardRecord
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&record); err != nil {
		return discardRecord{}, true, true
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return discardRecord{}, true, true
	}
	return record, true, false
}

func writeDiscardMarker(path string, record discardRecord) error {
	if err := atomicfile.Write(path, 0o600, func(writer io.Writer) error {
		encoder := json.NewEncoder(writer)
		encoder.SetEscapeHTML(false)
		return encoder.Encode(record)
	}); err != nil {
		return ErrNeedsReconciliation
	}
	observed, exists, unsafe := readDiscardRecord(path)
	if !exists || unsafe || observed != record {
		return ErrNeedsReconciliation
	}
	return nil
}

func openDiscardGuard(sessionsRoot string, record discardRecord) (*workspaceLease, error) {
	path := filepath.Join(sessionsRoot, discardGuardPrefix+record.SessionID)
	if err := ensureDirectory(path, 0o700, true); err != nil {
		return nil, err
	}
	if err := validateGuardDirectory(path, false); err != nil {
		return nil, err
	}
	recordPath := filepath.Join(path, discardGuardRecordName)
	old, exists, unsafe := readDiscardRecord(recordPath)
	if unsafe || (exists && old != (discardRecord{}) && old != record) {
		return nil, ErrNeedsReconciliation
	}
	guard, err := acquireWorkspaceLease(filepath.Join(path, LeaseFileName), true, false)
	if err != nil {
		return nil, err
	}
	if !exists || old == (discardRecord{}) {
		if err := writeDiscardMarker(recordPath, record); err != nil {
			_ = guard.Close()
			return nil, ErrNeedsReconciliation
		}
		if err := discardSyncDirectory(path); err != nil {
			_ = guard.Close()
			return nil, err
		}
		if err := discardSyncDirectory(sessionsRoot); err != nil {
			_ = guard.Close()
			return nil, err
		}
	}
	return guard, nil
}

func openEmptyDiscardGuard(path string) (*workspaceLease, error) {
	if err := validateGuardDirectory(path, false); err != nil {
		return nil, err
	}
	guard, err := acquireWorkspaceLease(filepath.Join(path, LeaseFileName), true, false)
	if err != nil {
		return nil, err
	}
	return guard, nil
}

func validateGuardDirectory(path string, requireRecord bool) error {
	entries, err := os.ReadDir(path)
	if err != nil {
		return ErrNeedsReconciliation
	}
	hasRecord := false
	for _, entry := range entries {
		switch entry.Name() {
		case discardGuardRecordName:
			if entry.IsDir() {
				return ErrNeedsReconciliation
			}
			hasRecord = true
		case LeaseFileName:
			if entry.IsDir() {
				return ErrNeedsReconciliation
			}
		default:
			return ErrNeedsReconciliation
		}
	}
	if requireRecord && !hasRecord {
		return ErrNeedsReconciliation
	}
	return nil
}

// CollectOrphans removes bounded idle session or quarantine workspaces. Old
// quarantines are routed through PrepareDiscard, so recovery uses the same
// marker, guard, identity, and marker-last cleanup protocol.
func CollectOrphans(outputRoot, outputRootIdentity string, live map[string]struct{}, olderThan time.Time) (OrphanCollection, error) {
	var result OrphanCollection
	if olderThan.IsZero() {
		return result, ErrInvalidReference
	}
	rootRef := RootRef{CanonicalPath: outputRoot, Identity: outputRootIdentity}
	if err := ValidateRootRef(rootRef); err != nil {
		return result, err
	}
	root := rootRef.CanonicalPath
	sessionsRoot := filepath.Join(root, SessionsDirectoryName)
	if err := validateDirectoryChain(sessionsRoot, true); err != nil {
		return result, err
	}
	directory, err := os.Open(sessionsRoot)
	if err != nil {
		return result, ErrWorkspaceUnavailable
	}
	entries, readErr := directory.ReadDir(maxOrphanScan + 1)
	closeErr := directory.Close()
	if (readErr != nil && !errors.Is(readErr, io.EOF)) || closeErr != nil {
		return result, ErrWorkspaceUnavailable
	}
	if len(entries) > maxOrphanScan {
		result.Limited = true
		entries = entries[:maxOrphanScan]
	}
	sort.Slice(entries, func(left, right int) bool { return entries[left].Name() < entries[right].Name() })
	reported := make(map[string]struct{})
	appendReported := func(destination *[]string, sessionID string) {
		if _, exists := reported[sessionID]; exists {
			return
		}
		reported[sessionID] = struct{}{}
		*destination = append(*destination, sessionID)
	}
	for _, entry := range entries {
		if len(reported) >= maxCollectedOrphans {
			result.Limited = true
			break
		}
		name := entry.Name()
		sessionID, candidate := maintenanceSessionID(name)
		if !candidate || !entry.IsDir() {
			result.Skipped++
			continue
		}
		if _, active := live[sessionID]; active {
			continue
		}
		info, statErr := entry.Info()
		if statErr != nil || !info.ModTime().Before(olderThan) {
			continue
		}
		if err := ValidateRootRef(rootRef); err != nil {
			return result, err
		}
		ref := WorkspaceRef{OutputRoot: root, OutputRootIdentity: outputRootIdentity, SessionID: sessionID}
		handle, prepareErr := PrepareDiscard(ref)
		if prepareErr != nil {
			if errors.Is(prepareErr, ErrNeedsReconciliation) {
				appendReported(&result.Reconciliation, sessionID)
			} else {
				result.Skipped++
			}
			continue
		}
		disposition, discardErr := handle.Discard()
		switch disposition {
		case Discarded:
			if discardErr == nil {
				appendReported(&result.Collected, sessionID)
			} else {
				appendReported(&result.CleanupPending, sessionID)
			}
		case DiscardCleanupPending:
			appendReported(&result.CleanupPending, sessionID)
		default:
			appendReported(&result.Reconciliation, sessionID)
		}
	}
	return result, nil
}

func maintenanceSessionID(name string) (string, bool) {
	if validSessionID(name) {
		return name, true
	}
	if strings.HasPrefix(name, discardQuarantinePrefix) && !strings.HasPrefix(name, discardGuardPrefix) {
		id := strings.TrimPrefix(name, discardQuarantinePrefix)
		return id, validSessionID(id)
	}
	if strings.HasPrefix(name, discardGuardPrefix) {
		id := strings.TrimPrefix(name, discardGuardPrefix)
		return id, validSessionID(id)
	}
	return "", false
}
