package engine

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/tejasa97/youtube_dlp/engine/value"
	"github.com/tejasa97/youtube_dlp/internal/atomicfile"
	"github.com/tejasa97/youtube_dlp/internal/downloader"
	"github.com/tejasa97/youtube_dlp/internal/events"
	mediaformat "github.com/tejasa97/youtube_dlp/internal/format"
	"github.com/tejasa97/youtube_dlp/internal/network"
	"github.com/tejasa97/youtube_dlp/internal/session"
)

const (
	directSessionComponentID     = "primary"
	directSessionCheckpoint      = session.CheckpointDirectoryName + "/direct.json"
	directSessionPublishDir      = "publish"
	directSessionStageName       = "final.staged"
	directSessionJournalName     = "journal.json"
	directSessionJournalVersion  = 1
	maxDirectSessionJournalBytes = 16 << 10
)

type directSession struct {
	operation    *operation
	workspace    *session.Workspace
	root         OutputRootRef
	target       CommitTarget
	selection    mediaformat.Selection
	identity     string
	planIdentity string
	destination  string
	payload      string
	checkpoint   string
	stage        string
	journal      string
	created      bool
}

type directPublicationJournal struct {
	Version        int       `json:"version"`
	SessionID      string    `json:"session_id"`
	State          string    `json:"state"`
	Fingerprint    string    `json:"fingerprint"`
	TargetKind     string    `json:"target_kind"`
	TargetIdentity string    `json:"target_identity"`
	TargetBasename string    `json:"target_basename"`
	UpdatedAt      time.Time `json:"updated_at"`
}

const (
	directJournalReady         = "ready"
	directJournalPublishing    = "publishing"
	directJournalPublished     = "published"
	directJournalCollision     = "collision"
	directJournalIndeterminate = "indeterminate"
)

var (
	// These seams are intentionally narrow: tests can model a crash/fault at
	// the journal boundary without weakening the atomicfile or no-follow
	// authority checks used in production.
	directSessionJournalWrite = writeDirectPublicationJournal
	directSessionNoReplace    = noReplaceDirectStage
)

type directNoReplaceResult uint8

const (
	directNoReplaceCommitted directNoReplaceResult = iota
	directNoReplaceCollision
	directNoReplacePrecommit
	directNoReplaceIndeterminate
)

type directNoReplaceError struct {
	result directNoReplaceResult
	err    error
}

func (err *directNoReplaceError) Error() string { return err.err.Error() }
func (err *directNoReplaceError) Unwrap() error { return err.err }

func sessionRequestEnabled(operation *operation) bool {
	return operation != nil && operation.request.Filesystem.Resume.SessionID != ""
}

func validateDirectSessionOutput(request Request, plans []mediaformat.OutputPlan, selectedSubtitles []subtitleTrack) error {
	if request.Simulate || request.SkipDownload || len(plans) != 1 || len(plans[0].Tracks) != 1 || len(selectedSubtitles) != 0 {
		return fmt.Errorf("%w: E2 supports one direct primary output", ErrUnsupported)
	}
	if len(request.Postprocessors) != 0 || len(request.DownloadSections) != 0 || request.SplitChapters || request.ConcatPlaylist != "" ||
		request.EmbedMetadata || request.EmbedInfoJSON != nil || request.EmbedChapters != nil || request.Thumbnails.Write || request.Thumbnails.WriteAll || request.Thumbnails.Embed ||
		request.Subtitles.WriteManual || request.Subtitles.WriteAutomatic || request.Subtitles.Embed || request.RelatedFiles.WriteInfoJSON || request.RelatedFiles.WriteDescription ||
		request.RelatedFiles.WriteLink || request.RelatedFiles.WriteURLLink || request.RelatedFiles.WriteWeblocLink || request.RelatedFiles.WriteDesktopLink ||
		request.SponsorBlock.Remove || request.Xattrs {
		return fmt.Errorf("%w: session output sidecars and processing are deferred to a later engine phase", ErrUnsupported)
	}
	return nil
}

func directSessionSupportedSelection(selection mediaformat.Selection) bool {
	if selection.URL == "" || selection.YouTubeLiveFromStart || selection.YouTubePostLive || selection.YouTubeSABR {
		return false
	}
	// The merged format planner represents direct HTTP resources with an empty
	// protocol or one of these two explicit values. Keep this allowlist closed:
	// run.download passes the selected transport to downloader.New, whose direct
	// path requires network.Doer. Unknown protocol labels must not reach that
	// assertion, even if their URL happens to use HTTP.
	switch selection.Protocol {
	case "", "http", "https":
		// allowed
	default:
		return false
	}
	parsed, err := http.NewRequest(http.MethodGet, selection.URL, nil)
	return err == nil && (parsed.URL.Scheme == "http" || parsed.URL.Scheme == "https")
}

func directSessionTrackIdentity(info value.Info, selection mediaformat.Selection) (string, error) {
	providerID, _ := info.ID()
	formatID := selection.ID
	if formatID == "" {
		return "", ErrResumeIdentityRequired
	}
	if providerID == "" {
		providerID = "unknown"
	}
	// This structure intentionally contains only provider/format identity and
	// representation attributes. In particular it never contains selection.URL
	// or any request header.
	identityInput := struct {
		ProviderID string
		FormatID   string
		Ext        string
		VCodec     string
		ACodec     string
		Protocol   string
		Itag       int64
	}{providerID, formatID, selection.Ext, selection.VCodec, selection.ACodec, selection.Protocol, selection.YouTubeItag}
	encoded, err := json.Marshal(identityInput)
	if err != nil {
		return "", ErrResumeIdentityRequired
	}
	digest := sha256.Sum256(encoded)
	return "direct-v1-" + hex.EncodeToString(digest[:]), nil
}

func directSessionPlanIdentity(identity, extension string) string {
	extension = strings.TrimPrefix(extension, ".")
	if extension == "" {
		extension = "bin"
	}
	return "direct-v1:" + identity + ":" + extension
}

func directSessionSourceID(info value.Info, identity string) string {
	if id, ok := info.ID(); ok && id != "" && validResumeIdentifier(id) {
		return id
	}
	return identity
}

func directSessionProvider(extractor string) string {
	if extractor == "" {
		return "engine"
	}
	var builder strings.Builder
	for _, character := range extractor {
		if (character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z') ||
			(character >= '0' && character <= '9') || strings.ContainsRune("._:-", character) {
			builder.WriteRune(character)
		}
	}
	if builder.Len() == 0 || !validResumeIdentifier(builder.String()) {
		return "engine"
	}
	return builder.String()
}

func (operation *operation) newDirectSession(info value.Info, extractor string, selection mediaformat.Selection, destination string) (*directSession, error) {
	if !sessionRequestEnabled(operation) {
		return nil, ErrResumeIdentityRequired
	}
	if !directSessionSupportedSelection(selection) {
		return nil, fmt.Errorf("%w: session mode supports direct HTTP tracks only", ErrUnsupported)
	}
	root, err := ValidateOutputRoot(operation.request.outputRoot(OutputPathHome))
	if err != nil {
		return nil, fmt.Errorf("%w: session output root", errInvalidRequestOptions)
	}
	resume := operation.request.Filesystem.Resume
	if len(resume.CommitTargets) != 1 || resume.CommitTargets[0].Kind != ArtifactKindPrimary || resume.CommitTargets[0].Identity != "primary" {
		return nil, fmt.Errorf("%w: direct session requires one primary commit target", ErrUnsupported)
	}
	target := resume.CommitTargets[0]
	destination = filepath.Clean(destination)
	relative, err := filepath.Rel(root.CanonicalPath, destination)
	if err != nil || relative != target.Basename || filepath.IsAbs(relative) || !validPortableResumeBasename(relative) {
		return nil, fmt.Errorf("%w: runtime destination does not match the declared commit target", errInvalidRequestOptions)
	}
	identity, err := directSessionTrackIdentity(info, selection)
	if err != nil {
		return nil, err
	}
	planIdentity := directSessionPlanIdentity(identity, selection.Ext)
	ref, err := session.NewWorkspaceRefWithIdentity(root.CanonicalPath, root.Identity, resume.SessionID)
	if err != nil {
		return nil, fmt.Errorf("%w: invalid session reference", errInvalidRequestOptions)
	}
	workspace, openErr := session.Open(ref)
	created := false
	if openErr != nil {
		if errors.Is(openErr, session.ErrLeaseContended) {
			return nil, errors.Join(ErrSessionInUse, fmt.Errorf("open resume session: %w", openErr))
		}
		if !errors.Is(openErr, session.ErrWorkspaceUnavailable) {
			return nil, fmt.Errorf("open resume session: %w", openErr)
		}
		workspace, openErr = session.CreateWithID(session.CreateOptions{
			OutputRoot: root.CanonicalPath, OutputRootIdentity: root.Identity,
			Source:              session.SourceIntent{Provider: directSessionProvider(extractor), ID: directSessionSourceID(info, identity), Kind: "video"},
			Output:              session.OutputIntent{Container: safeSessionIdentifier(selection.Ext, "bin"), Extension: safeSessionIdentifier(selection.Ext, "bin"), PlanIdentity: planIdentity},
			RelativeDestination: target.Basename,
			Components:          []session.Component{{ID: directSessionComponentID, Kind: "direct", Checkpoint: session.CheckpointMetadata{RelativePath: directSessionCheckpoint}}},
		}, resume.SessionID)
		created = openErr == nil
		if openErr != nil {
			// A concurrent creator may have won the exact-ID mkdir race. Reopen
			// only after the create attempt failed; the lease remains the authority.
			workspace, openErr = session.Open(ref)
		}
		if openErr != nil {
			if errors.Is(openErr, session.ErrLeaseContended) {
				return nil, errors.Join(ErrSessionInUse, fmt.Errorf("reopen resume session: %w", openErr))
			}
			return nil, fmt.Errorf("create resume session: %w", openErr)
		}
	}
	result := &directSession{
		operation: operation, workspace: workspace, root: root, target: target, selection: selection,
		identity: identity, planIdentity: planIdentity, destination: destination,
		payload:    filepath.Join(workspace.Path(), "payload"),
		checkpoint: filepath.Join(workspace.Path(), filepath.FromSlash(directSessionCheckpoint)),
		stage:      filepath.Join(workspace.Path(), directSessionPublishDir, directSessionStageName),
		journal:    filepath.Join(workspace.Path(), directSessionPublishDir, directSessionJournalName),
		created:    created,
	}
	if err := result.validateManifestIdentity(); err != nil {
		_ = workspace.Close()
		return nil, err
	}
	return result, nil
}

func safeSessionIdentifier(value, fallback string) string {
	value = strings.TrimPrefix(value, ".")
	if validResumeIdentifier(value) {
		return value
	}
	return fallback
}

func (run *directSession) validateManifestIdentity() error {
	manifest, err := run.workspace.Snapshot()
	if err != nil {
		return err
	}
	if manifest.SessionID != run.operation.request.Filesystem.Resume.SessionID || len(manifest.Components) != 1 || manifest.Components[0].ID != directSessionComponentID || manifest.Components[0].Kind != "direct" {
		return ErrResumeIdentityMismatch
	}
	if manifest.Output.PlanIdentity != run.planIdentity || manifest.RelativeDestination == "" && manifest.Phase != session.PhasePrepared {
		return ErrResumeIdentityMismatch
	}
	return nil
}

func (run *directSession) close() error {
	if run == nil || run.workspace == nil {
		return nil
	}
	err := run.workspace.Close()
	run.workspace = nil
	return err
}

func (run *directSession) snapshot() (session.Manifest, error) {
	if run == nil || run.workspace == nil {
		return session.Manifest{}, session.ErrWorkspaceClosed
	}
	return run.workspace.Snapshot()
}

func (run *directSession) targetForManifest(manifest session.Manifest) (string, error) {
	if manifest.RelativeDestination == "" || !validPortableResumeBasename(manifest.RelativeDestination) {
		return "", ErrSessionNeedsReconciliation
	}
	path := filepath.Join(run.root.CanonicalPath, filepath.FromSlash(manifest.RelativeDestination))
	relative, err := filepath.Rel(run.root.CanonicalPath, path)
	if err != nil || relative != filepath.FromSlash(manifest.RelativeDestination) || !validPortableResumeBasename(relative) {
		return "", ErrSessionNeedsReconciliation
	}
	return path, nil
}

func (run *directSession) validateAuthorities() error {
	root, err := ValidateOutputRoot(run.root.CanonicalPath)
	if err != nil || root.CanonicalPath != run.root.CanonicalPath || root.Identity != run.root.Identity {
		return ErrSessionNeedsReconciliation
	}
	for _, path := range []string{run.payload, run.stage, run.journal} {
		if err := validateWorkspaceFilePath(run.workspace.Path(), path, false); err != nil {
			return err
		}
	}
	return nil
}

func validateWorkspaceFilePath(workspaceRoot, target string, requireRegular bool) error {
	workspaceRoot = filepath.Clean(workspaceRoot)
	target = filepath.Clean(target)
	relative, err := filepath.Rel(workspaceRoot, target)
	if err != nil || relative == "." || filepath.IsAbs(relative) || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return ErrSessionNeedsReconciliation
	}
	current := workspaceRoot
	for index, component := range strings.Split(relative, string(filepath.Separator)) {
		if component == "" || component == "." || component == ".." {
			return ErrSessionNeedsReconciliation
		}
		current = filepath.Join(current, component)
		info, statErr := os.Lstat(current)
		if errors.Is(statErr, os.ErrNotExist) {
			if !requireRegular {
				return nil
			}
			return ErrSessionNeedsReconciliation
		}
		if statErr != nil || info.Mode()&os.ModeSymlink != 0 {
			return ErrSessionNeedsReconciliation
		}
		if index < len(strings.Split(relative, string(filepath.Separator)))-1 {
			if !info.IsDir() {
				return ErrSessionNeedsReconciliation
			}
		} else if requireRegular && !info.Mode().IsRegular() {
			return ErrSessionNeedsReconciliation
		}
	}
	return nil
}

func (run *directSession) rebindTargetIfNeeded(manifest session.Manifest) (session.Manifest, error) {
	if manifest.RelativeDestination == run.target.Basename {
		run.destination = filepath.Join(run.root.CanonicalPath, run.target.Basename)
		return manifest, nil
	}
	if manifest.Phase != session.PhaseReadyToPublish || manifest.Status == session.StatusActive || manifest.Publication == session.PublicationCommitted || manifest.Publication == session.PublicationIndeterminate {
		return manifest, ErrResumeIdentityMismatch
	}
	if err := run.workspace.RebindRelativeDestination(manifest.Revision, manifest.RunGeneration, run.target.Basename, time.Now().UTC()); err != nil {
		return manifest, fmt.Errorf("rebind ready destination: %w", err)
	}
	manifest, err := run.snapshot()
	if err != nil {
		return manifest, err
	}
	run.destination = filepath.Join(run.root.CanonicalPath, run.target.Basename)
	return manifest, nil
}

func (run *directSession) prepare(ctx context.Context) (session.Manifest, error) {
	manifest, err := run.snapshot()
	if err != nil {
		return manifest, err
	}
	if manifest.Status == session.StatusNeedsReconciliation {
		if err := run.reconcile(); err != nil {
			return manifest, err
		}
		manifest, err = run.snapshot()
		if err != nil {
			return manifest, err
		}
	}
	if manifest.Phase == session.PhaseCompleted && manifest.Publication == session.PublicationCommitted {
		destination, targetErr := run.targetForManifest(manifest)
		if targetErr != nil {
			return manifest, targetErr
		}
		if matchesFingerprint(destination, manifest.StagedFingerprint) {
			run.destination = destination
			return manifest, nil
		}
		return manifest, ErrSessionNeedsReconciliation
	}
	if manifest.Status == session.StatusCanceled {
		return manifest, ErrResumeIdentityMismatch
	}
	manifest, err = run.rebindTargetIfNeeded(manifest)
	if err != nil {
		return manifest, err
	}
	if manifest.Status == session.StatusActive && !run.created {
		// An active manifest without this process's lease is crash residue. Move
		// it through paused before Resume so the generation boundary is explicit.
		if err := run.workspace.Transition(manifest.Revision, manifest.RunGeneration, manifest.Phase, session.StatusPaused, session.DesiredPaused, time.Now().UTC()); err != nil {
			return manifest, err
		}
		manifest, err = run.snapshot()
		if err != nil {
			return manifest, err
		}
	}
	if manifest.Status == session.StatusPaused || manifest.Status == session.StatusFailed {
		if err := run.workspace.Resume(manifest.Revision, manifest.RunGeneration, time.Now().UTC()); err != nil {
			return manifest, err
		}
		manifest, err = run.snapshot()
		if err != nil {
			return manifest, err
		}
	}
	if err := ctx.Err(); err != nil {
		return manifest, err
	}
	switch manifest.Phase {
	case session.PhasePrepared:
		if err := run.workspace.Transition(manifest.Revision, manifest.RunGeneration, session.PhaseExtracting, session.StatusActive, session.DesiredRunning, time.Now().UTC()); err != nil {
			return manifest, err
		}
		manifest, err = run.snapshot()
		if err != nil {
			return manifest, err
		}
		fallthrough
	case session.PhaseExtracting:
		if err := run.workspace.Transition(manifest.Revision, manifest.RunGeneration, session.PhaseDownloading, session.StatusActive, session.DesiredRunning, time.Now().UTC()); err != nil {
			return manifest, err
		}
		manifest, err = run.snapshot()
		if err != nil {
			return manifest, err
		}
	case session.PhaseDownloading, session.PhaseProcessing, session.PhaseReadyToPublish:
	default:
		return manifest, ErrResumeIdentityMismatch
	}
	return manifest, nil
}

func (run *directSession) reconcile() error {
	manifest, err := run.snapshot()
	if err != nil {
		return err
	}
	destination, err := run.targetForManifest(manifest)
	if err != nil {
		return err
	}
	journal, err := readDirectPublicationJournal(run.journal)
	if err != nil || journal.SessionID != manifest.SessionID || journal.SessionID != run.operation.request.Filesystem.Resume.SessionID ||
		journal.TargetKind != string(run.target.Kind) || journal.TargetIdentity != run.target.Identity ||
		journal.TargetBasename != manifest.RelativeDestination || journal.TargetBasename != run.target.Basename ||
		manifest.StagedFingerprint != "" && journal.Fingerprint != manifest.StagedFingerprint ||
		manifest.Publication == session.PublicationIndeterminate && journal.State != directJournalIndeterminate {
		return ErrSessionNeedsReconciliation
	}
	fingerprint := manifest.StagedFingerprint
	if fingerprint == "" {
		fingerprint = journal.Fingerprint
	}
	if fingerprint != "" && matchesFingerprint(destination, fingerprint) {
		if manifest.Publication == session.PublicationIndeterminate {
			if err := run.workspace.ResolvePublication(manifest.Revision, manifest.RunGeneration, session.PublicationCommitted, time.Now().UTC()); err != nil {
				return err
			}
			manifest, err = run.snapshot()
			if err != nil {
				return err
			}
		}
		if manifest.Status == session.StatusNeedsReconciliation {
			if err := run.workspace.ResolveReconciliation(manifest.Revision, manifest.RunGeneration, session.StatusCompleted, session.DesiredRunning, time.Now().UTC()); err != nil {
				return err
			}
		}
		return nil
	}
	if manifest.Publication == session.PublicationIndeterminate {
		stageExists := matchesFingerprint(run.stage, fingerprint)
		if stageExists && !pathExists(destination) {
			if err := run.workspace.ResolvePublication(manifest.Revision, manifest.RunGeneration, session.PublicationPending, time.Now().UTC()); err != nil {
				return err
			}
			manifest, err = run.snapshot()
			if err != nil {
				return err
			}
			return run.workspace.ResolveReconciliation(manifest.Revision, manifest.RunGeneration, session.StatusFailed, session.DesiredRunning, time.Now().UTC())
		}
	}
	return ErrSessionNeedsReconciliation
}

func (run *directSession) run(ctx context.Context, sink events.Sink) (result Result, runErr error) {
	result.Session.SessionID = run.operation.request.Filesystem.Resume.SessionID
	result.Session.Cleanup = CleanupNotNeeded
	defer func() {
		if run.workspace != nil {
			if closeErr := run.close(); closeErr != nil && runErr == nil {
				runErr = closeErr
			}
		}
	}()
	if sink == nil {
		sink = events.Nop()
	}
	manifest, err := run.prepare(ctx)
	if err != nil {
		result.Session.Disposition = SessionRecoveryRequired
		result.Session.Publication = PublicationNotAttempted
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return run.handleInterruption(ctx, result, err)
		}
		return result, err
	}
	if manifest.Phase == session.PhaseCompleted && manifest.Publication == session.PublicationCommitted {
		result.Downloaded = true
		result.Filename = run.destination
		result.Bytes = manifest.StagedBytes
		result.Artifacts = []Artifact{{Path: run.destination, Kind: "media"}}
		result.Session.Disposition = SessionPublished
		result.Session.Publication = PublicationWon
		result.Session.Phase = SessionPhase(manifest.Phase)
		return result, nil
	}
	if manifest.Phase == session.PhaseReadyToPublish {
		return run.publishWithInterruption(ctx, result)
	}
	if manifest.Phase != session.PhaseDownloading && manifest.Phase != session.PhaseProcessing {
		return result, ErrResumeIdentityMismatch
	}
	component := manifest.Components[0]
	complete, err := run.completedPayload(component)
	if err != nil {
		return result, err
	}
	if !complete {
		boundary, reset, boundaryErr := run.resumeBoundary(component)
		if boundaryErr != nil {
			return result, boundaryErr
		}
		if reset {
			if err := run.resetDirectEvidence(manifest); err != nil {
				return result, err
			}
			manifest, err = run.snapshot()
			if err != nil {
				return result, err
			}
			boundary = nil
		}
		if manifest.Phase == session.PhaseProcessing {
			return result, ErrSessionNeedsReconciliation
		}
		if err := run.download(ctx, boundary, reset, sink); err != nil {
			if errors.Is(err, downloader.ErrCheckpointResetRequired) || errors.Is(err, downloader.ErrCheckpointReconciliation) || errors.Is(err, downloader.ErrInvalidCheckpointState) {
				if resetErr := run.resetDirectEvidence(manifest); resetErr != nil {
					return result, errors.Join(err, resetErr)
				}
				if retryErr := run.download(ctx, nil, true, sink); retryErr != nil {
					return run.handleInterruption(ctx, result, retryErr)
				}
			} else {
				return run.handleInterruption(ctx, result, err)
			}
		}
		manifest, err = run.snapshot()
		if err != nil {
			return result, err
		}
		complete, err = run.downloadedPayload(manifest.Components[0])
		if err != nil {
			return result, err
		}
		if !complete {
			return result, fmt.Errorf("%w: direct payload did not reach its committed length", session.ErrNeedsReconciliation)
		}
	}
	if manifest.Phase == session.PhaseDownloading {
		if err := run.workspace.Transition(manifest.Revision, manifest.RunGeneration, session.PhaseProcessing, session.StatusActive, session.DesiredRunning, time.Now().UTC()); err != nil {
			return result, err
		}
	}
	manifest, err = run.snapshot()
	if err != nil {
		return result, err
	}
	fingerprint, bytes, err := run.buildStagedOutput(manifest)
	if err != nil {
		return run.handleInterruption(ctx, result, err)
	}
	if err := run.workspace.SetStagedOutput(manifest.Revision, manifest.RunGeneration, fingerprint, bytes); err != nil {
		return result, err
	}
	manifest, err = run.snapshot()
	if err != nil {
		return result, err
	}
	if manifest.Phase == session.PhaseProcessing {
		if err := run.workspace.Transition(manifest.Revision, manifest.RunGeneration, session.PhaseReadyToPublish, session.StatusActive, session.DesiredRunning, time.Now().UTC()); err != nil {
			return result, err
		}
	}
	manifest, err = run.snapshot()
	if err != nil {
		return result, err
	}
	if manifest.Publication == session.PublicationNotStarted {
		if err := run.writeJournal(directJournalReady, manifest.StagedFingerprint); err != nil {
			markReadyPublicationResult(&result, manifest)
			return result, err
		}
		if err := run.workspace.SetPublicationState(manifest.Revision, manifest.RunGeneration, session.PublicationPending, time.Now().UTC()); err != nil {
			markReadyPublicationResult(&result, manifest)
			return result, err
		}
	}
	return run.publishWithInterruption(ctx, result)
}

func (run *directSession) publishWithInterruption(ctx context.Context, result Result) (Result, error) {
	result, err := run.publish(ctx, result)
	if err != nil && (errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)) {
		return run.handleInterruption(ctx, result, err)
	}
	return result, err
}

func (run *directSession) completedPayload(component session.Component) (bool, error) {
	if !strongETag(component.Checkpoint.ETag) {
		return false, nil
	}
	return run.payloadAtManifestBoundary(component)
}

// downloadedPayload is used only after the downloader has just returned
// success. A response without a strong validator may be published for this
// run because its exact byte count was durably committed; a later run must
// still reject that checkpoint for append/resume and conservatively transfer
// again.
func (run *directSession) downloadedPayload(component session.Component) (bool, error) {
	return run.payloadAtManifestBoundary(component)
}

func (run *directSession) payloadAtManifestBoundary(component session.Component) (bool, error) {
	if component.CommittedBytes <= 0 || component.Checkpoint.Total <= 0 || component.CommittedBytes != component.Checkpoint.Total {
		return false, nil
	}
	if err := validateWorkspaceFilePath(run.workspace.Path(), run.payload, true); err != nil {
		if errors.Is(err, ErrSessionNeedsReconciliation) && !pathExists(run.payload) {
			return false, nil
		}
		return false, err
	}
	info, err := os.Lstat(run.payload)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return false, ErrSessionNeedsReconciliation
	}
	return info.Size() == component.CommittedBytes, nil
}

func (run *directSession) resumeBoundary(component session.Component) (*downloader.Checkpoint, bool, error) {
	if component.CommittedBytes == 0 {
		return nil, false, nil
	}
	if component.Checkpoint.RelativePath != directSessionCheckpoint || component.Checkpoint.Total <= component.CommittedBytes || !strongETag(component.Checkpoint.ETag) {
		return nil, true, nil
	}
	return &downloader.Checkpoint{
		ResumeIdentity: run.identity, ETag: component.Checkpoint.ETag,
		LastModified: component.Checkpoint.LastModified, Total: component.Checkpoint.Total,
		CommittedBytes: component.CommittedBytes,
	}, false, nil
}

func strongETag(value string) bool {
	value = strings.TrimSpace(value)
	return value != "" && !strings.HasPrefix(strings.ToLower(value), "w/")
}

func (run *directSession) commitCheckpoint(_ context.Context, checkpoint downloader.Checkpoint) error {
	manifest, err := run.snapshot()
	if err != nil {
		return err
	}
	if checkpoint.ResumeIdentity != run.identity {
		return ErrResumeIdentityMismatch
	}
	metadata := manifest.Components[0].Checkpoint
	metadata.ETag = checkpoint.ETag
	metadata.LastModified = checkpoint.LastModified
	metadata.Total = checkpoint.Total
	return run.workspace.SetComponentCheckpoint(manifest.Revision, manifest.RunGeneration, directSessionComponentID, checkpoint.CommittedBytes, checkpoint.CommittedBytes, metadata)
}

func (run *directSession) download(ctx context.Context, boundary *downloader.Checkpoint, reset bool, sink events.Sink) error {
	mediaTransport, err := run.operation.mediaTransport(
		run.selection.CredentialIsolated, run.selection.CredentialIsolatedReferer,
		run.selection.HostPolicy, run.selection.Protocol,
	)
	if err != nil {
		return err
	}
	if run.selection.MediaPolicy != "" {
		mediaTransport = newProviderPolicyTransport(run.operation, run.selection.MediaPolicy, "media")
	}
	job := run.operation.directDownloadJob(run.selection.URL, run.selection.Headers, run.workspace.Path(), run.payload)
	job.OutputRoot = run.workspace.Path()
	job.Destination = run.payload
	job.Overwrite = true
	job.ResumeIdentity = run.identity
	job.NoContinue = reset || run.operation.request.Filesystem.NoContinue
	job.Checkpoint = &downloader.CheckpointOptions{ResumeBoundary: boundary, StateDirectory: filepath.Dir(run.checkpoint), OnCommit: run.commitCheckpoint}
	returnResult, downloadErr := downloader.New(mediaTransport.(network.Doer)).Download(ctx, job, sink)
	_ = returnResult
	return downloadErr
}

func (run *directSession) resetDirectEvidence(manifest session.Manifest) error {
	for _, path := range []string{run.payload, run.payload + ".part", run.checkpoint} {
		if err := removeOwnedRegular(path); err != nil {
			return err
		}
	}
	if err := run.workspace.ResetComponent(manifest.Revision, manifest.RunGeneration, directSessionComponentID, time.Now().UTC()); err != nil {
		return err
	}
	return nil
}

func removeOwnedRegular(path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return ErrSessionNeedsReconciliation
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

func (run *directSession) buildStagedOutput(manifest session.Manifest) (string, int64, error) {
	if err := run.validateAuthorities(); err != nil {
		return "", 0, err
	}
	if err := ensureOwnerDirectory(filepath.Join(run.workspace.Path(), directSessionPublishDir)); err != nil {
		return "", 0, err
	}
	sourceInfo, err := os.Lstat(run.payload)
	if err != nil || sourceInfo.Mode()&os.ModeSymlink != 0 || !sourceInfo.Mode().IsRegular() {
		return "", 0, ErrSessionNeedsReconciliation
	}
	if manifest.Components[0].CommittedBytes <= 0 || sourceInfo.Size() != manifest.Components[0].CommittedBytes {
		return "", 0, ErrSessionNeedsReconciliation
	}
	fingerprint, bytes, err := fingerprintFile(run.payload)
	if err != nil {
		return "", 0, err
	}
	if matchesFingerprint(run.stage, fingerprint) {
		return fingerprint, bytes, nil
	}
	if info, statErr := os.Lstat(run.stage); statErr == nil && (info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular()) {
		return "", 0, ErrSessionNeedsReconciliation
	}
	err = atomicfile.Write(run.stage, 0o600, func(writer io.Writer) error {
		reader, openErr := os.Open(run.payload)
		if openErr != nil {
			return openErr
		}
		defer reader.Close()
		_, copyErr := io.Copy(writer, reader)
		return copyErr
	})
	if err != nil {
		return "", 0, err
	}
	if !matchesFingerprint(run.stage, fingerprint) {
		return "", 0, ErrSessionNeedsReconciliation
	}
	return fingerprint, bytes, nil
}

func ensureOwnerDirectory(path string) error {
	if info, err := os.Lstat(path); err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return ErrSessionNeedsReconciliation
		}
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.Mkdir(path, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
		return err
	}
	info, err := os.Lstat(path)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return ErrSessionNeedsReconciliation
	}
	return nil
}

func fingerprintFile(path string) (string, int64, error) {
	info, err := os.Lstat(path)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return "", 0, ErrSessionNeedsReconciliation
	}
	file, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer file.Close()
	hash := sha256.New()
	bytes, err := io.Copy(hash, io.LimitReader(file, 8<<30+1))
	if err != nil {
		return "", 0, err
	}
	if bytes > 8<<30 {
		return "", 0, downloader.ErrIncomplete
	}
	return hex.EncodeToString(hash.Sum(nil)), bytes, nil
}

func matchesFingerprint(path, expected string) bool {
	if expected == "" {
		return false
	}
	observed, _, err := fingerprintFile(path)
	return err == nil && observed == expected
}

func pathExists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}

func (run *directSession) writeJournal(state, fingerprint string) error {
	return directSessionJournalWrite(run.journal, directPublicationJournal{
		Version: directSessionJournalVersion, SessionID: run.operation.request.Filesystem.Resume.SessionID,
		State: state, Fingerprint: fingerprint, TargetKind: string(run.target.Kind),
		TargetIdentity: run.target.Identity, TargetBasename: run.target.Basename, UpdatedAt: time.Now().UTC(),
	})
}

func writeDirectPublicationJournal(path string, journal directPublicationJournal) error {
	if journal.Version != directSessionJournalVersion || !validDirectJournalState(journal.State) || !validResumeIdentifier(journal.SessionID) || !validResumeIdentifier(journal.TargetKind) || !validResumeIdentifier(journal.TargetIdentity) || !validPortableResumeBasename(journal.TargetBasename) || len(journal.Fingerprint) != sha256.Size*2 || journal.Fingerprint != strings.ToLower(journal.Fingerprint) || journal.UpdatedAt.IsZero() {
		return ErrSessionNeedsReconciliation
	}
	if _, err := hex.DecodeString(journal.Fingerprint); err != nil {
		return ErrSessionNeedsReconciliation
	}
	if err := ensureOwnerDirectory(filepath.Dir(path)); err != nil {
		return err
	}
	return atomicfile.Write(path, 0o600, func(writer io.Writer) error {
		encoder := json.NewEncoder(writer)
		encoder.SetEscapeHTML(false)
		return encoder.Encode(journal)
	})
}

func validDirectJournalState(state string) bool {
	switch state {
	case directJournalReady, directJournalPublishing, directJournalPublished, directJournalCollision, directJournalIndeterminate:
		return true
	default:
		return false
	}
}

func readDirectPublicationJournal(path string) (directPublicationJournal, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return directPublicationJournal{}, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > maxDirectSessionJournalBytes {
		return directPublicationJournal{}, ErrSessionNeedsReconciliation
	}
	file, err := os.Open(path)
	if err != nil {
		return directPublicationJournal{}, err
	}
	defer file.Close()
	decoder := json.NewDecoder(io.LimitReader(file, maxDirectSessionJournalBytes+1))
	decoder.DisallowUnknownFields()
	var journal directPublicationJournal
	if err := decoder.Decode(&journal); err != nil {
		return directPublicationJournal{}, ErrSessionNeedsReconciliation
	}
	if journal.Version != directSessionJournalVersion || !validDirectJournalState(journal.State) || journal.UpdatedAt.IsZero() || len(journal.Fingerprint) != sha256.Size*2 || journal.Fingerprint != strings.ToLower(journal.Fingerprint) || !validResumeIdentifier(journal.SessionID) || !validResumeIdentifier(journal.TargetKind) || !validResumeIdentifier(journal.TargetIdentity) || !validPortableResumeBasename(journal.TargetBasename) {
		return directPublicationJournal{}, ErrSessionNeedsReconciliation
	}
	if _, err := hex.DecodeString(journal.Fingerprint); err != nil {
		return directPublicationJournal{}, ErrSessionNeedsReconciliation
	}
	return journal, nil
}

func (run *directSession) publish(ctx context.Context, result Result) (Result, error) {
	if err := run.validateAuthorities(); err != nil {
		return result, err
	}
	manifest, err := run.snapshot()
	if err != nil {
		return result, err
	}
	destination, err := run.targetForManifest(manifest)
	if err != nil {
		return result, err
	}
	run.destination = destination
	if manifest.StagedFingerprint == "" {
		return result, ErrSessionNeedsReconciliation
	}
	if manifest.Publication == session.PublicationNotStarted {
		if err := run.writeJournal(directJournalReady, manifest.StagedFingerprint); err != nil {
			return result, err
		}
		if err := run.workspace.SetPublicationState(manifest.Revision, manifest.RunGeneration, session.PublicationPending, time.Now().UTC()); err != nil {
			return result, err
		}
		manifest, err = run.snapshot()
		if err != nil {
			return result, err
		}
	}
	reservation, err := run.operation.request.Filesystem.Resume.PublicationArbiter.BeginPublication(ctx)
	if err != nil {
		markReadyPublicationResult(&result, manifest)
		return result, err
	}
	finished := false
	defer func() {
		if !finished {
			// All current paths explicitly terminalize the reservation. Keep a
			// fail-closed guard for future edits so a forgotten return cannot
			// leave the arbiter locked forever.
			reservation.MarkIndeterminate()
		}
	}()
	if err := run.writeJournal(directJournalPublishing, manifest.StagedFingerprint); err != nil {
		reservation.AbortBeforeReplace()
		finished = true
		markReadyPublicationResult(&result, manifest)
		return result, err
	}
	replaceResult, replaceErr := directSessionNoReplace(run.stage, destination, manifest.StagedFingerprint)
	if replaceErr != nil {
		switch replaceResult {
		case directNoReplaceCollision, directNoReplacePrecommit:
			reservation.AbortBeforeReplace()
			finished = true
			markReadyPublicationResult(&result, manifest)
			if replaceResult == directNoReplaceCollision {
				_ = run.writeJournal(directJournalCollision, manifest.StagedFingerprint)
				_ = run.markCollision()
				result.Session.Disposition = SessionCollision
				result.Session.Publication = PublicationCollision
				result.Session.Phase = SessionPhase(manifest.Phase)
				return result, errors.Join(ErrDestinationCollision, replaceErr)
			}
			return result, replaceErr
		default:
			reservation.MarkIndeterminate()
			finished = true
			journalErr := run.writeJournal(directJournalIndeterminate, manifest.StagedFingerprint)
			stateErr := run.markPublicationIndeterminate()
			result.Session.Disposition = SessionRecoveryRequired
			result.Session.Publication = PublicationIndeterminateOutcome
			return result, errors.Join(ErrPublicationIndeterminate, replaceErr, journalErr, stateErr)
		}
	}
	reservation.MarkDestinationReplaced()
	journalErr := run.writeJournal(directJournalPublished, manifest.StagedFingerprint)
	stagedBytes := manifest.StagedBytes
	manifest, err = run.snapshot()
	if err == nil && manifest.Publication == session.PublicationPending {
		err = run.workspace.SetPublicationState(manifest.Revision, manifest.RunGeneration, session.PublicationCommitted, time.Now().UTC())
	}
	if err == nil {
		manifest, err = run.snapshot()
	}
	if err == nil && manifest.Phase == session.PhaseReadyToPublish {
		err = run.workspace.Transition(manifest.Revision, manifest.RunGeneration, session.PhaseCompleted, session.StatusCompleted, session.DesiredRunning, time.Now().UTC())
	}
	reservation.FinishPublication()
	finished = true
	if journalErr != nil || err != nil {
		result.Session.Disposition = SessionRecoveryRequired
		result.Session.Publication = PublicationIndeterminateOutcome
		if manifest.Phase != "" {
			result.Session.Phase = SessionPhase(manifest.Phase)
		}
		return result, errors.Join(ErrSessionNeedsReconciliation, journalErr, err)
	}
	result.Downloaded = true
	result.Filename = destination
	result.Bytes = stagedBytes
	result.Artifacts = []Artifact{{Path: destination, Kind: "media"}}
	result.Session.Disposition = SessionPublished
	result.Session.Publication = PublicationWon
	result.Session.Phase = SessionPhase(session.PhaseCompleted)
	return result, nil
}

func markReadyPublicationResult(result *Result, manifest session.Manifest) {
	result.Session.Disposition = SessionRetained
	result.Session.Publication = PublicationReady
	result.Session.Phase = SessionPhase(manifest.Phase)
}

func (run *directSession) markCollision() error {
	manifest, err := run.snapshot()
	if err != nil {
		return err
	}
	if manifest.Status != session.StatusActive || manifest.Phase != session.PhaseReadyToPublish {
		return nil
	}
	return run.workspace.Transition(manifest.Revision, manifest.RunGeneration, session.PhaseReadyToPublish, session.StatusFailed, session.DesiredRunning, time.Now().UTC())
}

func (run *directSession) markPublicationIndeterminate() error {
	manifest, err := run.snapshot()
	if err != nil {
		return err
	}
	if manifest.Publication == session.PublicationIndeterminate {
		return nil
	}
	if manifest.Status != session.StatusActive || manifest.Publication != session.PublicationPending {
		return ErrSessionNeedsReconciliation
	}
	return run.workspace.SetPublicationState(manifest.Revision, manifest.RunGeneration, session.PublicationIndeterminate, time.Now().UTC())
}

func noReplaceDirectStage(stage, destination, expectedFingerprint string) (directNoReplaceResult, error) {
	stageInfo, err := os.Lstat(stage)
	if err != nil || stageInfo.Mode()&os.ModeSymlink != 0 || !stageInfo.Mode().IsRegular() {
		return directNoReplaceIndeterminate, fmt.Errorf("%w: staged output is unavailable", ErrSessionNeedsReconciliation)
	}
	if observed, _, hashErr := fingerprintFile(stage); hashErr != nil || observed != expectedFingerprint {
		return directNoReplaceIndeterminate, fmt.Errorf("%w: staged fingerprint changed", ErrSessionNeedsReconciliation)
	}
	destinationInfo, statErr := os.Lstat(destination)
	if statErr == nil {
		if destinationInfo.Mode()&os.ModeSymlink != 0 || !destinationInfo.Mode().IsRegular() {
			return directNoReplaceCollision, fmt.Errorf("%w: target is not a regular file", ErrDestinationCollision)
		}
		if matchesFingerprint(destination, expectedFingerprint) {
			return directNoReplaceCommitted, nil
		}
		return directNoReplaceCollision, fmt.Errorf("%w: target already exists", ErrDestinationCollision)
	}
	if !errors.Is(statErr, os.ErrNotExist) {
		return directNoReplaceIndeterminate, statErr
	}
	if err := os.Link(stage, destination); err == nil {
		return directNoReplaceCommitted, nil
	} else if !errors.Is(err, os.ErrExist) {
		// Recheck both names. A successful no-replace commit may have returned
		// an error only after the directory entry became visible.
		if matchesFingerprint(destination, expectedFingerprint) {
			return directNoReplaceCommitted, nil
		}
		if pathExists(stage) && !pathExists(destination) {
			return directNoReplacePrecommit, err
		}
		return directNoReplaceIndeterminate, err
	}
	if matchesFingerprint(destination, expectedFingerprint) {
		return directNoReplaceCommitted, nil
	}
	return directNoReplaceCollision, fmt.Errorf("%w: target appeared during publication", ErrDestinationCollision)
}

func (run *directSession) handleInterruption(ctx context.Context, result Result, err error) (Result, error) {
	if err == nil {
		return result, nil
	}
	if ctx.Err() == nil && !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
		result.Session.Disposition = SessionRetained
		result.Session.Publication = PublicationNotAttempted
		return result, err
	}
	cause := context.Cause(ctx)
	if errors.Is(cause, ErrPauseRequested) {
		if pauseErr := run.markPaused(); pauseErr != nil {
			result.Session.Disposition = SessionRecoveryRequired
			return result, errors.Join(err, pauseErr)
		}
		manifest, _ := run.snapshot()
		result.Session.Disposition = SessionRetained
		result.Session.Publication = publicationOutcomeFor(manifest.Publication)
		result.Session.Phase = SessionPhase(manifest.Phase)
		return result, errors.Join(ErrPauseRequested, err)
	}
	// Ordinary cancellation is the engine's destructive Cancel path. Close the
	// runner lease before opening the restartable discard authority.
	if closeErr := run.close(); closeErr != nil {
		result.Session.Disposition = SessionCleanupPending
		return result, errors.Join(err, closeErr)
	}
	root := run.root
	handle, prepareErr := PrepareResumeDiscard(context.Background(), root, run.operation.request.Filesystem.Resume.SessionID)
	if prepareErr != nil {
		result.Session.Disposition = SessionCleanupPending
		result.Session.Cleanup = CleanupPendingOutcome
		return result, errors.Join(err, prepareErr)
	}
	discardResult, discardErr := handle.Discard(context.Background())
	if discardErr != nil {
		result.Session.Disposition = SessionCleanupPending
		result.Session.Cleanup = CleanupPendingOutcome
		return result, errors.Join(err, discardErr)
	}
	result.Session.Disposition = SessionDiscarded
	result.Session.Cleanup = CleanupComplete
	_ = discardResult
	return result, err
}

func (run *directSession) markPaused() error {
	manifest, err := run.snapshot()
	if err != nil {
		return err
	}
	if manifest.Status != session.StatusActive {
		return nil
	}
	return run.workspace.Transition(manifest.Revision, manifest.RunGeneration, manifest.Phase, session.StatusPaused, session.DesiredPaused, time.Now().UTC())
}

func publicationOutcomeFor(state session.PublicationState) PublicationOutcome {
	switch state {
	case session.PublicationPending:
		return PublicationReady
	case session.PublicationCommitted:
		return PublicationWon
	case session.PublicationIndeterminate:
		return PublicationIndeterminateOutcome
	default:
		return PublicationNotAttempted
	}
}
