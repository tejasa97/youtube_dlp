package engine

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/tejasa97/youtube_dlp/engine/value"
	outputtemplate "github.com/tejasa97/youtube_dlp/internal/compat/template"
	"github.com/tejasa97/youtube_dlp/internal/session"
)

var ErrPauseRequested = errors.New("engine: pause requested")

var (
	// ErrDestinationCollision reports that a session publication encountered
	// an existing target whose bytes did not match the staged artifact. The
	// target is never replaced and the ready-to-publish session remains
	// retryable.
	ErrDestinationCollision = errors.New("engine: destination collision")
	// ErrResumeIdentityMismatch reports that a caller reused a session for a
	// different provider track or output plan. The existing evidence is left
	// untouched so a caller can reconcile it explicitly.
	ErrResumeIdentityMismatch = errors.New("engine: resume identity mismatch")
	// ErrResumeIdentityRequired reports that a session-enabled direct resource
	// did not expose a stable, non-secret provider/format identity.
	ErrResumeIdentityRequired = errors.New("engine: stable resume identity required")
	// ErrSessionNeedsReconciliation reports that publication or manifest
	// evidence cannot be classified without destructive guessing.
	ErrSessionNeedsReconciliation = errors.New("engine: resume session needs reconciliation")
	// ErrSessionInUse reports bounded lease contention without exposing the
	// workspace or holder metadata.
	ErrSessionInUse = errors.New("engine: resume session is in use")
)

// SessionDisposition is the bounded lifecycle result of a session-enabled
// direct run. It intentionally contains no paths or transport material.
type SessionDisposition string

const (
	SessionRetained         SessionDisposition = "retained"
	SessionDiscarded        SessionDisposition = "discarded"
	SessionCleanupPending   SessionDisposition = "cleanup_pending"
	SessionCollision        SessionDisposition = "collision"
	SessionPublished        SessionDisposition = "published"
	SessionRecoveryRequired SessionDisposition = "recovery_required"
)

type PublicationOutcome string

const (
	PublicationNotAttempted         PublicationOutcome = "not_attempted"
	PublicationReady                PublicationOutcome = "ready"
	PublicationWon                  PublicationOutcome = "published"
	PublicationCollision            PublicationOutcome = "collision"
	PublicationIndeterminateOutcome PublicationOutcome = "indeterminate"
)

type CleanupOutcome string

const (
	CleanupNotNeeded      CleanupOutcome = "not_needed"
	CleanupComplete       CleanupOutcome = "complete"
	CleanupPendingOutcome CleanupOutcome = "pending"
	CleanupRecoveryNeeded CleanupOutcome = "recovery_required"
)

// SessionOutcome is attached to Result for session-enabled requests. Zero
// values preserve the legacy result shape and behavior.
type SessionOutcome struct {
	SessionID   string
	Disposition SessionDisposition
	Phase       SessionPhase
	Publication PublicationOutcome
	Cleanup     CleanupOutcome
}

const (
	maxResumeCommitTargets = 64
	maxResumeComponents    = 128
	maxResumeBasenameBytes = 255
)

// OutputRootRef is the canonical, public reference to an existing output
// root. Identity is an opaque stable directory identity used to fail closed
// when a persisted root is replaced.
type OutputRootRef struct {
	CanonicalPath string
	Identity      string
}

// ValidateOutputRoot canonicalizes and validates an existing output root
// without following a symlink or Windows reparse point. It performs no
// mutation or network access. Identity is an opaque platform-specific value;
// callers must persist and compare it, never interpret it as a path.
func ValidateOutputRoot(path string) (OutputRootRef, error) {
	if path == "" {
		return OutputRootRef{}, fmt.Errorf("engine: invalid output root")
	}
	canonical, err := filepath.Abs(path)
	if err != nil {
		return OutputRootRef{}, fmt.Errorf("engine: invalid output root")
	}
	ref, err := session.ValidateOutputRoot(canonical)
	if err != nil {
		return OutputRootRef{}, fmt.Errorf("engine: invalid output root")
	}
	return OutputRootRef{CanonicalPath: ref.CanonicalPath, Identity: ref.Identity}, nil
}

// SessionPhase is a path-free durable session phase.
type SessionPhase string

// SessionStatus is a path-free durable session disposition.
type SessionStatus string

// SessionDesiredState is the last durable lifecycle request.
type SessionDesiredState string

// SessionPublicationState is the durable publication authority state.
type SessionPublicationState string

// SessionCleanupState is the durable workspace cleanup state.
type SessionCleanupState string

// ResumeInspectionClass is a bounded non-destructive inspection result.
type ResumeInspectionClass string

// ResumeComponent reports only bounded checkpoint progress. Checkpoint paths,
// digests, request material, and transport credentials are intentionally not
// public.
type ResumeComponent struct {
	ID             string
	Kind           string
	ObservedBytes  int64
	CommittedBytes int64
}

// ResumeSummary is a bounded, credential-free view of a durable session.
// It never contains workspace paths, destination paths, URLs, headers,
// cookies, tokens, checkpoint paths, or checkpoint digests.
type ResumeSummary struct {
	SessionID       string
	Classification  ResumeInspectionClass
	Classifications []ResumeInspectionClass
	LeaseContended  bool
	HasManifest     bool
	Phase           SessionPhase
	Status          SessionStatus
	Desired         SessionDesiredState
	Publication     SessionPublicationState
	Cleanup         SessionCleanupState
	Components      []ResumeComponent
	Truncated       bool
}

// InspectResumeState reads a session without modifying it. The returned
// summary omits all private workspace and credential-bearing material.
func InspectResumeState(ctx context.Context, root OutputRootRef, sessionID string) (ResumeSummary, error) {
	if err := contextError(ctx); err != nil {
		return ResumeSummary{}, err
	}
	ref, err := workspaceRef(root, sessionID)
	if err != nil {
		return ResumeSummary{}, err
	}
	inspection, err := session.Inspect(ref)
	if err != nil {
		return ResumeSummary{}, fmt.Errorf("engine: inspect resume state: %w", err)
	}
	if err := contextError(ctx); err != nil {
		return ResumeSummary{}, err
	}
	summary := ResumeSummary{
		SessionID:      sessionID,
		LeaseContended: inspection.LeaseContended,
		HasManifest:    inspection.HasManifest,
	}
	for _, class := range inspection.Classifications {
		summary.Classifications = append(summary.Classifications, ResumeInspectionClass(class))
	}
	if inspection.Classification != "" {
		summary.Classification = ResumeInspectionClass(inspection.Classification)
	}
	if !inspection.HasManifest {
		return summary, nil
	}
	manifest := inspection.Manifest
	summary.Phase = SessionPhase(manifest.Phase)
	summary.Status = SessionStatus(manifest.Status)
	summary.Desired = SessionDesiredState(manifest.Desired)
	summary.Publication = SessionPublicationState(manifest.Publication)
	summary.Cleanup = SessionCleanupState(manifest.Cleanup)
	for index, component := range manifest.Components {
		if index >= maxResumeComponents {
			summary.Truncated = true
			break
		}
		summary.Components = append(summary.Components, ResumeComponent{
			ID: component.ID, Kind: component.Kind,
			ObservedBytes: component.ObservedBytes, CommittedBytes: component.CommittedBytes,
		})
	}
	return summary, nil
}

// ResumeDiscardHandle owns a prepared discard operation. It exposes neither
// the workspace path nor any internal/session handle.
type ResumeDiscardHandle struct {
	mu        sync.Mutex
	sessionID string
	handle    *session.DiscardHandle
}

// ResumeDiscardDisposition is the bounded result of a destructive discard
// attempt. Cleanup-pending and reconciliation-required evidence can be
// reopened with PrepareResumeDiscard after the caller records the result.
type ResumeDiscardDisposition string

const (
	ResumeDiscarded                     ResumeDiscardDisposition = "discarded"
	ResumeDiscardCleanupPending         ResumeDiscardDisposition = "cleanup_pending"
	ResumeDiscardReconciliationRequired ResumeDiscardDisposition = "reconciliation_required"
)

// ResumeDiscardResult reports only the session identity and bounded cleanup
// disposition. It contains no workspace or output paths.
type ResumeDiscardResult struct {
	SessionID   string
	Disposition ResumeDiscardDisposition
	// Discarded is retained as a convenience compatibility bit. New callers
	// should switch on Disposition so cleanup obligations cannot be mistaken
	// for a completed discard.
	Discarded bool
}

// PrepareResumeDiscard acquires the workspace lease and validates that the
// session has no reconciliation evidence before returning a destructive
// handle. Call Close to abandon the operation without deleting anything.
func PrepareResumeDiscard(ctx context.Context, root OutputRootRef, sessionID string) (*ResumeDiscardHandle, error) {
	if err := contextError(ctx); err != nil {
		return nil, err
	}
	ref, err := workspaceRef(root, sessionID)
	if err != nil {
		return nil, err
	}
	handle, err := session.PrepareDiscard(ref)
	if err != nil {
		if errors.Is(err, session.ErrLeaseContended) {
			return nil, errors.Join(ErrSessionInUse, fmt.Errorf("engine: prepare resume discard: %w", err))
		}
		return nil, fmt.Errorf("engine: prepare resume discard: %w", err)
	}
	if err := contextError(ctx); err != nil {
		_ = handle.Close()
		return nil, err
	}
	return &ResumeDiscardHandle{sessionID: sessionID, handle: handle}, nil
}

// Discard removes the prepared hidden session workspace, never a declared
// output artifact. The handle is consumed even when cleanup is pending or
// reconciliation is required; callers reopen the durable evidence with
// PrepareResumeDiscard to retry safely.
func (handle *ResumeDiscardHandle) Discard(ctx context.Context) (ResumeDiscardResult, error) {
	if err := contextError(ctx); err != nil {
		return ResumeDiscardResult{}, err
	}
	if handle == nil {
		return ResumeDiscardResult{}, errors.New("engine: invalid resume discard handle")
	}
	handle.mu.Lock()
	defer handle.mu.Unlock()
	if handle.handle == nil {
		return ResumeDiscardResult{}, errors.New("engine: invalid resume discard handle")
	}
	internal := handle.handle
	handle.handle = nil
	disposition, discardErr := internal.Discard()
	result := ResumeDiscardResult{
		SessionID:   handle.sessionID,
		Disposition: resumeDiscardDisposition(disposition),
		Discarded:   disposition == session.Discarded,
	}
	if discardErr != nil {
		return result, fmt.Errorf("engine: discard resume state: %w", discardErr)
	}
	return result, nil
}

// Close abandons a prepared discard operation without deleting the session.
func (handle *ResumeDiscardHandle) Close() error {
	if handle == nil {
		return nil
	}
	handle.mu.Lock()
	defer handle.mu.Unlock()
	if handle.handle == nil {
		return nil
	}
	err := handle.handle.Close()
	handle.handle = nil
	if err != nil {
		return fmt.Errorf("engine: close resume discard: %w", err)
	}
	return nil
}

func cloneResumeOptions(options ResumeOptions) ResumeOptions {
	if options.CommitTargets != nil {
		targets := options.CommitTargets
		options.CommitTargets = make([]CommitTarget, len(targets))
		copy(options.CommitTargets, targets)
	}
	return options
}

func resumeOptionsPresent(options ResumeOptions) bool {
	return options.SessionID != "" || options.PublicationArbiter != nil || options.CommitTargets != nil
}

// CollectionResult is a bounded, path-free orphan-collection result.
type CollectionResult struct {
	CollectedSessionIDs      []string
	CleanupPendingSessionIDs []string
	ReconciliationSessionIDs []string
	Skipped                  int
	Limited                  bool
}

// CollectResumeOrphans removes idle session workspaces absent from live and
// older than the supplied cutoff. It preserves any unsafe, contended, corrupt,
// or reconciliation-required workspace for explicit recovery.
func CollectResumeOrphans(ctx context.Context, root OutputRootRef, live map[string]struct{}, olderThan time.Time) (CollectionResult, error) {
	if err := contextError(ctx); err != nil {
		return CollectionResult{}, err
	}
	validated, err := validatedOutputRoot(root)
	if err != nil {
		return CollectionResult{}, err
	}
	result, err := session.CollectOrphans(validated.CanonicalPath, validated.Identity, live, olderThan)
	if err != nil {
		return CollectionResult{}, fmt.Errorf("engine: collect resume orphans: %w", err)
	}
	if err := contextError(ctx); err != nil {
		return CollectionResult{}, err
	}
	return CollectionResult{
		CollectedSessionIDs:      append([]string(nil), result.Collected...),
		CleanupPendingSessionIDs: append([]string(nil), result.CleanupPending...),
		ReconciliationSessionIDs: append([]string(nil), result.Reconciliation...),
		Skipped:                  result.Skipped,
		Limited:                  result.Limited,
	}, nil
}

func resumeDiscardDisposition(disposition session.DiscardDisposition) ResumeDiscardDisposition {
	switch disposition {
	case session.Discarded:
		return ResumeDiscarded
	case session.DiscardCleanupPending:
		return ResumeDiscardCleanupPending
	default:
		return ResumeDiscardReconciliationRequired
	}
}

func validateResumeOptions(request Request) error {
	resume := request.Filesystem.Resume
	if resume.SessionID == "" {
		if resume.PublicationArbiter != nil || resume.CommitTargets != nil {
			return fmt.Errorf("%w: partial resume options require a session ID", errInvalidRequestOptions)
		}
		return nil
	}
	if request.Overwrite {
		return fmt.Errorf("%w: session runs require overwrite=false", errInvalidRequestOptions)
	}
	if request.Filesystem.NoPart {
		return fmt.Errorf("%w: session runs require part files", errInvalidRequestOptions)
	}
	if resume.PublicationArbiter == nil {
		return fmt.Errorf("%w: session runs require a publication arbiter", errInvalidRequestOptions)
	}
	if len(resume.CommitTargets) == 0 || len(resume.CommitTargets) > maxResumeCommitTargets {
		return fmt.Errorf("%w: session commit targets", errInvalidRequestOptions)
	}
	root, err := ValidateOutputRoot(request.outputRoot(OutputPathHome))
	if err != nil {
		return fmt.Errorf("%w: session output root", errInvalidRequestOptions)
	}
	seen := make(map[string]struct{}, len(resume.CommitTargets))
	basenames := make(map[string]struct{}, len(resume.CommitTargets))
	for _, target := range resume.CommitTargets {
		if !validResumeIdentifier(string(target.Kind)) || !validResumeIdentifier(target.Identity) || !validPortableResumeBasename(target.Basename) {
			return fmt.Errorf("%w: invalid session commit target", errInvalidRequestOptions)
		}
		key := string(target.Kind) + "\x00" + target.Identity
		if _, duplicate := seen[key]; duplicate {
			return fmt.Errorf("%w: duplicate session commit target", errInvalidRequestOptions)
		}
		seen[key] = struct{}{}
		// Exact duplicates are universally unsafe. Case-folding is deliberately
		// not guessed: a volume can opt into case sensitivity, so a future
		// platform-specific root policy must prove it before rejecting more.
		if _, duplicate := basenames[target.Basename]; duplicate {
			return fmt.Errorf("%w: duplicate session commit basename", errInvalidRequestOptions)
		}
		basenames[target.Basename] = struct{}{}
		candidate := filepath.Join(root.CanonicalPath, target.Basename)
		relative, relErr := filepath.Rel(root.CanonicalPath, candidate)
		if relErr != nil || relative != target.Basename {
			return fmt.Errorf("%w: session commit target escapes output root", errInvalidRequestOptions)
		}
	}
	if _, err := workspaceRef(root, resume.SessionID); err != nil {
		return fmt.Errorf("%w: invalid resume session", errInvalidRequestOptions)
	}
	return nil
}

func workspaceRef(root OutputRootRef, sessionID string) (session.WorkspaceRef, error) {
	validated, err := validatedOutputRoot(root)
	if err != nil {
		return session.WorkspaceRef{}, err
	}
	ref, err := session.NewWorkspaceRefWithIdentity(validated.CanonicalPath, validated.Identity, sessionID)
	if err != nil {
		return session.WorkspaceRef{}, errors.New("engine: invalid resume session reference")
	}
	return ref, nil
}

func validatedOutputRoot(root OutputRootRef) (session.RootRef, error) {
	if root.CanonicalPath == "" || root.Identity == "" {
		return session.RootRef{}, errors.New("engine: invalid output root reference")
	}
	validated, err := session.ValidateOutputRoot(root.CanonicalPath)
	if err != nil || validated.CanonicalPath != root.CanonicalPath || validated.Identity != root.Identity {
		return session.RootRef{}, errors.New("engine: invalid output root reference")
	}
	return validated, nil
}

func validResumeIdentifier(value string) bool {
	if value == "" || len(value) > 256 || !utf8.ValidString(value) {
		return false
	}
	for _, character := range value {
		if (character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z') ||
			(character >= '0' && character <= '9') || strings.ContainsRune("._:-", character) {
			continue
		}
		return false
	}
	return value != "." && value != ".."
}

func validResumeBasename(value string) bool {
	if value == "" || len(value) > maxResumeBasenameBytes || !utf8.ValidString(value) ||
		strings.ContainsAny(value, "\x00/\\") || filepath.Base(value) != value || filepath.VolumeName(value) != "" ||
		value == "." || value == ".." {
		return false
	}
	for _, character := range value {
		if unicode.IsControl(character) {
			return false
		}
	}
	return true
}

func validPortableResumeBasename(basename string) bool {
	if !validResumeBasename(basename) {
		return false
	}
	root := filepath.Join(string(filepath.Separator), "engine-commit-target")
	info := value.NewInfo(value.NewObject(value.Field{Key: "basename", Value: value.String(basename)}))
	resolved, err := outputtemplate.ResolveWithOptions(
		root, "%(basename)s", info, outputtemplate.FilenameOptions{WindowsFilenames: true},
	)
	if err != nil {
		return false
	}
	relative, err := filepath.Rel(root, resolved)
	return err == nil && relative == basename && filepath.Dir(relative) == "."
}

func contextError(ctx context.Context) error {
	if ctx == nil {
		return nil
	}
	return ctx.Err()
}
