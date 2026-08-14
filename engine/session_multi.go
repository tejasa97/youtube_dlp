package engine

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/tejasa97/youtube_dlp/engine/value"
	"github.com/tejasa97/youtube_dlp/internal/downloader"
	"github.com/tejasa97/youtube_dlp/internal/events"
	mediaformat "github.com/tejasa97/youtube_dlp/internal/format"
	"github.com/tejasa97/youtube_dlp/internal/media/ffmpeg"
	"github.com/tejasa97/youtube_dlp/internal/network"
	"github.com/tejasa97/youtube_dlp/internal/session"
)

const (
	multiSessionPayloadDir      = "payload"
	multiSessionProcessingDir   = "processing"
	multiSessionStagePrefix     = "final.staged."
	multiSessionPlanIdentityTag = "direct-ntrack-v1-"
)

// These seams keep E3 crash and fault tests at the same boundary as the E2
// tests. Production execution always uses the merged FFmpeg workspace and the
// no-replace publication primitive.
var (
	multiSessionJournalWrite = writeDirectPublicationJournal
	multiSessionNoReplace    = noReplaceDirectStage
	multiSessionMergeTracks  = func(
		ctx context.Context,
		tools *ffmpeg.Toolset,
		inputs []ffmpeg.MergeInput,
		destination string,
		sink events.Sink,
		workspace ffmpeg.ProcessingWorkspace,
	) error {
		return tools.MergeTracksWithWorkspace(ctx, inputs, destination, false, sink, workspace)
	}
)

type multiTrackSessionTrack struct {
	id          string
	role        string
	identity    string
	identitySum string
	selection   mediaformat.Selection
	payload     string
	checkpoint  string
}

type multiTrackSession struct {
	operation    *operation
	workspace    *session.Workspace
	root         OutputRootRef
	target       CommitTarget
	tracks       []multiTrackSessionTrack
	planIdentity string
	provider     string
	sourceID     string
	destination  string
	stage        string
	journal      string
	created      bool
	mutationMu   sync.Mutex
}

func validateMultiTrackSessionOutput(request Request, plans []mediaformat.OutputPlan, selectedSubtitles []subtitleTrack) error {
	if request.Simulate || request.SkipDownload || len(plans) != 1 || len(plans[0].Tracks) < 2 || len(selectedSubtitles) != 0 {
		return fmt.Errorf("%w: session mode requires one multi-track primary output", ErrUnsupported)
	}
	if !mergeableTracks(plans[0].Tracks) {
		return fmt.Errorf("%w: selected session tracks are not mergeable", ErrUnsupported)
	}
	if len(request.Postprocessors) != 0 || len(request.DownloadSections) != 0 || request.SplitChapters || request.ConcatPlaylist != "" ||
		request.EmbedMetadata || request.EmbedInfoJSON != nil || request.EmbedChapters != nil || request.Thumbnails.Write || request.Thumbnails.WriteAll || request.Thumbnails.Embed ||
		request.Subtitles.WriteManual || request.Subtitles.WriteAutomatic || request.Subtitles.Embed || request.RelatedFiles.WriteInfoJSON || request.RelatedFiles.WriteDescription ||
		request.RelatedFiles.WriteLink || request.RelatedFiles.WriteURLLink || request.RelatedFiles.WriteWeblocLink || request.RelatedFiles.WriteDesktopLink ||
		request.SponsorBlock.Remove || request.Xattrs {
		return fmt.Errorf("%w: session output sidecars and postprocessors are deferred", ErrUnsupported)
	}
	for _, selection := range plans[0].Tracks {
		if !directSessionSupportedSelection(selection) {
			return fmt.Errorf("%w: session multi-track mode supports direct HTTP tracks only", ErrUnsupported)
		}
	}
	return nil
}

func multiTrackRole(selection mediaformat.Selection) string {
	switch {
	case selectionHasVideo(selection) && selectionHasAudio(selection):
		return "av"
	case selectionHasVideo(selection):
		return "video"
	case selectionHasAudio(selection):
		return "audio"
	default:
		return "track"
	}
}

func multiTrackIdentityDigest(identity string) string {
	digest := sha256.Sum256([]byte(identity))
	return hex.EncodeToString(digest[:])
}

func multiTrackPlanIdentity(tracks []multiTrackSessionTrack, destination string) string {
	hasher := sha256.New()
	for _, track := range tracks {
		_, _ = io.WriteString(hasher, track.id)
		_, _ = io.WriteString(hasher, "\x00")
		_, _ = io.WriteString(hasher, track.identitySum)
		_, _ = io.WriteString(hasher, "\x00")
		_, _ = io.WriteString(hasher, safeExtension(track.selection.Ext))
		_, _ = io.WriteString(hasher, "\x00")
		_, _ = io.WriteString(hasher, track.selection.VCodec)
		_, _ = io.WriteString(hasher, "\x00")
		_, _ = io.WriteString(hasher, track.selection.ACodec)
		_, _ = io.WriteString(hasher, "\x00")
		_, _ = io.WriteString(hasher, track.selection.Protocol)
		_, _ = io.WriteString(hasher, "\x00")
	}
	_, _ = io.WriteString(hasher, multiSessionExtension(destination))
	return multiSessionPlanIdentityTag + hex.EncodeToString(hasher.Sum(nil))
}

func multiSessionExtension(destination string) string {
	extension := strings.TrimPrefix(filepath.Ext(destination), ".")
	return safeExtension(extension)
}

func newMultiTrackSession(
	operation *operation,
	info value.Info,
	extractor string,
	selections []mediaformat.Selection,
	destination string,
) (*multiTrackSession, error) {
	if !sessionRequestEnabled(operation) || !mergeableTracks(selections) || len(selections) < 2 {
		return nil, fmt.Errorf("%w: invalid session multi-track selection", ErrUnsupported)
	}
	root, err := ValidateOutputRoot(operation.request.outputRoot(OutputPathHome))
	if err != nil {
		return nil, fmt.Errorf("%w: session output root", errInvalidRequestOptions)
	}
	resume := operation.request.Filesystem.Resume
	if len(resume.CommitTargets) != 1 || resume.CommitTargets[0].Kind != ArtifactKindPrimary || resume.CommitTargets[0].Identity != "primary" {
		return nil, fmt.Errorf("%w: multi-track session requires one primary commit target", ErrUnsupported)
	}
	target := resume.CommitTargets[0]
	destination = filepath.Clean(destination)
	declaredDestination, ok := portableResumeDestination(root.CanonicalPath, target.Basename)
	if !ok || declaredDestination != destination {
		return nil, fmt.Errorf("%w: runtime destination does not match the declared commit target", errInvalidRequestOptions)
	}

	provider := directSessionProvider(extractor)
	providerID := "unknown"
	if id, ok := info.ID(); ok && id != "" {
		providerID = id
	}
	tracks := make([]multiTrackSessionTrack, 0, len(selections))
	counts := make(map[string]int)
	for _, selection := range selections {
		identity, identityErr := directSessionTrackIdentity(info, selection)
		if identityErr != nil {
			return nil, identityErr
		}
		role := multiTrackRole(selection)
		id := role
		if counts[role] > 0 {
			id = fmt.Sprintf("%s-%d", role, counts[role])
		}
		counts[role]++
		tracks = append(tracks, multiTrackSessionTrack{
			id: id, role: role, identity: identity, identitySum: multiTrackIdentityDigest(identity), selection: isolatedSelection(selection),
			checkpoint: filepath.ToSlash(filepath.Join(session.CheckpointDirectoryName, id, "direct.json")),
		})
	}
	planIdentity := multiTrackPlanIdentity(tracks, destination)
	ref, err := session.NewWorkspaceRefWithIdentity(root.CanonicalPath, root.Identity, resume.SessionID)
	if err != nil {
		return nil, fmt.Errorf("%w: invalid session reference", errInvalidRequestOptions)
	}
	components := make([]session.Component, 0, len(tracks))
	for _, track := range tracks {
		components = append(components, session.Component{
			ID: track.id, Kind: "direct-" + track.role,
			Checkpoint: session.CheckpointMetadata{RelativePath: track.checkpoint},
		})
	}
	workspace, openErr := session.Open(ref)
	created := false
	if openErr != nil {
		if errors.Is(openErr, session.ErrLeaseContended) {
			return nil, errors.Join(ErrSessionInUse, fmt.Errorf("open multi-track resume session: %w", openErr))
		}
		if !errors.Is(openErr, session.ErrWorkspaceUnavailable) {
			return nil, fmt.Errorf("open multi-track resume session: %w", openErr)
		}
		workspace, openErr = session.CreateWithID(session.CreateOptions{
			OutputRoot: root.CanonicalPath, OutputRootIdentity: root.Identity,
			Source: session.SourceIntent{Provider: provider, ID: providerID, Kind: "video"},
			Output: session.OutputIntent{
				Container: safeSessionIdentifier(multiSessionExtension(destination), "bin"),
				Extension: safeSessionIdentifier(multiSessionExtension(destination), "bin"), PlanIdentity: planIdentity,
			},
			RelativeDestination: target.Basename, Components: components,
		}, resume.SessionID)
		created = openErr == nil
		if openErr != nil {
			workspace, openErr = session.Open(ref)
		}
		if openErr != nil {
			if errors.Is(openErr, session.ErrLeaseContended) {
				return nil, errors.Join(ErrSessionInUse, fmt.Errorf("reopen multi-track resume session: %w", openErr))
			}
			return nil, fmt.Errorf("create multi-track resume session: %w", openErr)
		}
	}
	result := &multiTrackSession{
		operation: operation, workspace: workspace, root: root, target: target, tracks: tracks,
		planIdentity: planIdentity, provider: provider, sourceID: providerID, destination: destination, created: created,
		stage:   filepath.Join(workspace.Path(), directSessionPublishDir, multiSessionStagePrefix+multiSessionExtension(destination)),
		journal: filepath.Join(workspace.Path(), directSessionPublishDir, directSessionJournalName),
	}
	for index := range result.tracks {
		result.tracks[index].payload = filepath.Join(workspace.Path(), multiSessionPayloadDir, result.tracks[index].id)
	}
	if err := result.validateManifestIdentity(); err != nil {
		_ = workspace.Close()
		return nil, err
	}
	return result, nil
}

func (run *multiTrackSession) validateManifestIdentity() error {
	manifest, err := run.workspace.Snapshot()
	if err != nil {
		return err
	}
	if manifest.SessionID != run.operation.request.Filesystem.Resume.SessionID || manifest.Output.PlanIdentity != run.planIdentity ||
		manifest.Source.Provider != run.provider || manifest.Source.ID != run.sourceID || len(manifest.Components) != len(run.tracks) {
		return ErrResumeIdentityMismatch
	}
	for _, track := range run.tracks {
		component, ok := componentByID(manifest, track.id)
		if !ok || component.Kind != "direct-"+track.role || component.Checkpoint.RelativePath != track.checkpoint {
			return ErrResumeIdentityMismatch
		}
	}
	return nil
}

func componentByID(manifest session.Manifest, id string) (session.Component, bool) {
	for _, component := range manifest.Components {
		if component.ID == id {
			return component, true
		}
	}
	return session.Component{}, false
}

func (run *multiTrackSession) close() error {
	if run == nil || run.workspace == nil {
		return nil
	}
	err := run.workspace.Close()
	run.workspace = nil
	return err
}

func (run *multiTrackSession) snapshot() (session.Manifest, error) {
	if run == nil || run.workspace == nil {
		return session.Manifest{}, session.ErrWorkspaceClosed
	}
	return run.workspace.Snapshot()
}

func (run *multiTrackSession) targetForManifest(manifest session.Manifest) (string, error) {
	if manifest.RelativeDestination == "" {
		return "", ErrSessionNeedsReconciliation
	}
	path, ok := portableResumeDestination(run.root.CanonicalPath, manifest.RelativeDestination)
	if !ok {
		return "", ErrSessionNeedsReconciliation
	}
	return path, nil
}

func (run *multiTrackSession) validateAuthorities() error {
	root, err := ValidateOutputRoot(run.root.CanonicalPath)
	if err != nil || root.CanonicalPath != run.root.CanonicalPath || root.Identity != run.root.Identity {
		return ErrSessionNeedsReconciliation
	}
	paths := []string{run.stage, run.journal, filepath.Join(run.workspace.Path(), multiSessionPayloadDir), filepath.Join(run.workspace.Path(), multiSessionProcessingDir)}
	for _, track := range run.tracks {
		checkpointDirectory := filepath.Join(run.workspace.Path(), filepath.FromSlash(filepath.Dir(track.checkpoint)))
		paths = append(paths, track.payload, track.payload+".part", checkpointDirectory)
	}
	for _, path := range paths {
		if err := validateWorkspaceFilePath(run.workspace.Path(), path, false); err != nil {
			return err
		}
	}
	return nil
}

func (run *multiTrackSession) rebindTargetIfNeeded(manifest session.Manifest) (session.Manifest, error) {
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

func (run *multiTrackSession) prepare(ctx context.Context) (session.Manifest, error) {
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
		if !run.completedSessionIdentityMatches(manifest) {
			return manifest, ErrResumeIdentityMismatch
		}
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
	if err := run.reconcileTrackIdentities(manifest); err != nil {
		return manifest, err
	}
	manifest, err = run.snapshot()
	if err != nil {
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

func (run *multiTrackSession) completedSessionIdentityMatches(manifest session.Manifest) bool {
	for _, track := range run.tracks {
		component, ok := componentByID(manifest, track.id)
		if !ok || component.Checkpoint.Digest != track.identitySum {
			return false
		}
	}
	return true
}

func (run *multiTrackSession) reconcileTrackIdentities(manifest session.Manifest) error {
	for _, track := range run.tracks {
		component, ok := componentByID(manifest, track.id)
		if !ok {
			return ErrResumeIdentityMismatch
		}
		if component.Checkpoint.Digest == track.identitySum {
			continue
		}
		if component.Checkpoint.Digest == "" && component.ObservedBytes == 0 && component.CommittedBytes == 0 {
			if err := run.setTrackIdentity(track, manifest); err != nil {
				return fmt.Errorf("set multi-track identity %s: %w", track.id, err)
			}
			manifest, _ = run.snapshot()
			continue
		}
		if manifest.Phase != session.PhaseDownloading || manifest.StagedFingerprint != "" || manifest.Publication != session.PublicationNotStarted {
			return ErrResumeIdentityMismatch
		}
		if err := run.resetTrackEvidenceAndManifest(track); err != nil {
			return fmt.Errorf("reset multi-track evidence %s: %w", track.id, err)
		}
		manifest, _ = run.snapshot()
	}
	return nil
}

func (run *multiTrackSession) setTrackIdentity(track multiTrackSessionTrack, manifest session.Manifest) error {
	component, ok := componentByID(manifest, track.id)
	if !ok || component.Checkpoint.RelativePath != track.checkpoint || component.ObservedBytes != 0 || component.CommittedBytes != 0 {
		return ErrResumeIdentityMismatch
	}
	metadata := component.Checkpoint
	metadata.Digest = track.identitySum
	return run.workspace.SetComponentCheckpoint(manifest.Revision, manifest.RunGeneration, track.id, 0, 0, metadata)
}

func (run *multiTrackSession) resetTrackEvidenceAndManifest(track multiTrackSessionTrack) error {
	run.mutationMu.Lock()
	defer run.mutationMu.Unlock()
	manifest, err := run.snapshot()
	if err != nil {
		return err
	}
	if manifest.Phase != session.PhaseDownloading || manifest.StagedFingerprint != "" || manifest.Publication != session.PublicationNotStarted {
		return ErrResumeIdentityMismatch
	}
	if err := removeMultiTrackEvidence(run.workspace.Path(), track); err != nil {
		return err
	}
	if err := run.workspace.ResetComponent(manifest.Revision, manifest.RunGeneration, track.id, time.Now().UTC()); err != nil {
		return err
	}
	manifest, err = run.snapshot()
	if err != nil {
		return err
	}
	component, ok := componentByID(manifest, track.id)
	if !ok {
		return ErrResumeIdentityMismatch
	}
	metadata := component.Checkpoint
	metadata.Digest = track.identitySum
	return run.workspace.SetComponentCheckpoint(manifest.Revision, manifest.RunGeneration, track.id, 0, 0, metadata)
}

func removeMultiTrackEvidence(workspaceRoot string, track multiTrackSessionTrack) error {
	for _, path := range []string{track.payload, track.payload + ".part"} {
		if err := removeOwnedRegular(path); err != nil {
			return err
		}
	}
	checkpointDir := filepath.Dir(filepath.Join(workspaceRoot, filepath.FromSlash(track.checkpoint)))
	if err := validateWorkspaceFilePath(workspaceRoot, checkpointDir, false); err != nil {
		return err
	}
	info, err := os.Lstat(checkpointDir)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return ErrSessionNeedsReconciliation
	}
	entries, err := os.ReadDir(checkpointDir)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		name := entry.Name()
		if name != "direct.json" && name != "owner" {
			return ErrSessionNeedsReconciliation
		}
		child := filepath.Join(checkpointDir, name)
		childInfo, statErr := os.Lstat(child)
		if statErr != nil || childInfo.Mode()&os.ModeSymlink != 0 || !childInfo.Mode().IsRegular() {
			return ErrSessionNeedsReconciliation
		}
		if err := os.Remove(child); err != nil {
			return err
		}
	}
	if err := os.Remove(checkpointDir); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

func (run *multiTrackSession) trackCheckpoint(manifest session.Manifest, track multiTrackSessionTrack) (*downloader.Checkpoint, bool, error) {
	component, ok := componentByID(manifest, track.id)
	if !ok {
		return nil, false, ErrResumeIdentityMismatch
	}
	if component.Checkpoint.Digest != track.identitySum || component.Checkpoint.RelativePath != track.checkpoint {
		return nil, true, nil
	}
	if component.CommittedBytes == 0 {
		return nil, false, nil
	}
	if component.Checkpoint.Total <= component.CommittedBytes || !strongETag(component.Checkpoint.ETag) {
		return nil, true, nil
	}
	return &downloader.Checkpoint{
		ResumeIdentity: track.identity, ETag: component.Checkpoint.ETag, LastModified: component.Checkpoint.LastModified,
		Total: component.Checkpoint.Total, CommittedBytes: component.CommittedBytes,
	}, false, nil
}

func (run *multiTrackSession) completedTrack(component session.Component, track multiTrackSessionTrack) (bool, error) {
	if component.Checkpoint.Digest != track.identitySum || !strongETag(component.Checkpoint.ETag) {
		return false, nil
	}
	return run.payloadAtManifestBoundary(component, track)
}

func (run *multiTrackSession) payloadAtManifestBoundary(component session.Component, track multiTrackSessionTrack) (bool, error) {
	if component.CommittedBytes <= 0 || component.Checkpoint.Total <= 0 || component.CommittedBytes != component.Checkpoint.Total {
		return false, nil
	}
	if err := validateWorkspaceFilePath(run.workspace.Path(), track.payload, true); err != nil {
		if errors.Is(err, ErrSessionNeedsReconciliation) && !pathExists(track.payload) {
			return false, nil
		}
		return false, err
	}
	info, err := os.Lstat(track.payload)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return false, ErrSessionNeedsReconciliation
	}
	return info.Size() == component.CommittedBytes, nil
}

func (run *multiTrackSession) downloadedTrack(component session.Component, track multiTrackSessionTrack) (bool, error) {
	return run.payloadAtManifestBoundary(component, track)
}

func (run *multiTrackSession) commitTrackCheckpoint(_ context.Context, track multiTrackSessionTrack, checkpoint downloader.Checkpoint) error {
	run.mutationMu.Lock()
	defer run.mutationMu.Unlock()
	if checkpoint.ResumeIdentity != track.identity {
		return ErrResumeIdentityMismatch
	}
	manifest, err := run.snapshot()
	if err != nil {
		return err
	}
	component, ok := componentByID(manifest, track.id)
	if !ok || component.Checkpoint.RelativePath != track.checkpoint {
		return ErrResumeIdentityMismatch
	}
	metadata := component.Checkpoint
	metadata.Digest = track.identitySum
	metadata.ETag = checkpoint.ETag
	metadata.LastModified = checkpoint.LastModified
	metadata.Total = checkpoint.Total
	return run.workspace.SetComponentCheckpoint(manifest.Revision, manifest.RunGeneration, track.id, checkpoint.CommittedBytes, checkpoint.CommittedBytes, metadata)
}

func (run *multiTrackSession) downloadTrack(ctx context.Context, track multiTrackSessionTrack, boundary *downloader.Checkpoint, reset bool, sink events.Sink) error {
	mediaTransport, err := run.operation.mediaTransport(
		track.selection.CredentialIsolated, track.selection.CredentialIsolatedReferer,
		track.selection.HostPolicy, track.selection.Protocol,
	)
	if err != nil {
		return err
	}
	if track.selection.MediaPolicy != "" {
		mediaTransport = newProviderPolicyTransport(run.operation, track.selection.MediaPolicy, "media")
	}
	job := run.operation.directDownloadJob(track.selection.URL, track.selection.Headers, run.workspace.Path(), track.payload)
	job.HTTPChunkSize = track.selection.HTTPChunkSize
	job.HTTPChunkFixed = track.selection.HTTPChunkFixed
	job.ExpectedBytes = track.selection.Filesize
	job.OutputRoot = run.workspace.Path()
	job.Destination = track.payload
	job.Overwrite = true
	job.ResumeIdentity = track.identity
	job.NoContinue = reset || run.operation.request.Filesystem.NoContinue
	job.Checkpoint = &downloader.CheckpointOptions{
		ResumeBoundary: boundary, StateDirectory: filepath.Dir(filepath.Join(run.workspace.Path(), filepath.FromSlash(track.checkpoint))),
		OnCommit: func(commitCtx context.Context, checkpoint downloader.Checkpoint) error {
			return run.commitTrackCheckpoint(commitCtx, track, checkpoint)
		},
	}
	releaseTransfer, err := run.operation.acquireGoogleVideoTransfer(ctx, track.selection.URL)
	if err != nil {
		return err
	}
	defer releaseTransfer()
	_, downloadErr := downloader.New(mediaTransport.(network.Doer)).Download(ctx, job, sink)
	return downloadErr
}

func (run *multiTrackSession) downloadTracks(ctx context.Context, sink events.Sink) error {
	if sink == nil {
		sink = events.Nop()
	}
	serializedSink := &lockedEventSink{sink: sink}
	manifest, err := run.snapshot()
	if err != nil {
		return err
	}
	type trackJob struct {
		track    multiTrackSessionTrack
		boundary *downloader.Checkpoint
		reset    bool
	}
	jobs := make([]trackJob, 0, len(run.tracks))
	for _, track := range run.tracks {
		component, ok := componentByID(manifest, track.id)
		if !ok {
			return ErrResumeIdentityMismatch
		}
		complete, completeErr := run.completedTrack(component, track)
		if completeErr != nil {
			return completeErr
		}
		if complete {
			_ = serializedSink.Emit(ctx, events.Event{Kind: events.KindCompleted, Path: track.payload, Bytes: component.CommittedBytes, Total: component.CommittedBytes, Resuming: true})
			continue
		}
		boundary, reset, boundaryErr := run.trackCheckpoint(manifest, track)
		if boundaryErr != nil {
			return boundaryErr
		}
		if reset {
			if err := run.resetTrackEvidenceAndManifest(track); err != nil {
				return err
			}
			manifest, err = run.snapshot()
			if err != nil {
				return err
			}
			boundary = nil
		}
		jobs = append(jobs, trackJob{track: track, boundary: boundary, reset: reset})
	}
	if len(jobs) == 0 {
		return nil
	}
	for _, job := range jobs {
		err := run.downloadTrack(ctx, job.track, job.boundary, job.reset, serializedSink)
		if errors.Is(err, downloader.ErrCheckpointResetRequired) || errors.Is(err, downloader.ErrCheckpointReconciliation) || errors.Is(err, downloader.ErrInvalidCheckpointState) {
			if ctx.Err() == nil {
				if resetErr := run.resetTrackEvidenceAndManifest(job.track); resetErr != nil {
					err = errors.Join(err, resetErr)
				} else {
					err = run.downloadTrack(ctx, job.track, nil, true, serializedSink)
				}
			}
		}
		if err != nil {
			return fmt.Errorf("multi-track transfer: %w", err)
		}
	}
	return nil
}

func isContextCancellation(err error) bool {
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}

func (run *multiTrackSession) processingInputFingerprint(manifest session.Manifest) (string, error) {
	hasher := sha256.New()
	for _, track := range run.tracks {
		component, ok := componentByID(manifest, track.id)
		if !ok {
			return "", ErrResumeIdentityMismatch
		}
		complete, err := run.downloadedTrack(component, track)
		if err != nil || !complete {
			if err == nil {
				err = ErrSessionNeedsReconciliation
			}
			return "", err
		}
		fingerprint, bytes, err := fingerprintFile(track.payload)
		if err != nil {
			return "", err
		}
		_, _ = fmt.Fprintf(hasher, "%s\x00%s\x00%s\x00%d\x00", track.id, track.identitySum, fingerprint, bytes)
	}
	return hex.EncodeToString(hasher.Sum(nil)), nil
}

func (run *multiTrackSession) process(ctx context.Context, sink events.Sink) error {
	if err := run.validateAuthorities(); err != nil {
		return err
	}
	manifest, err := run.snapshot()
	if err != nil {
		return err
	}
	if manifest.StagedFingerprint != "" {
		if matchesFingerprint(run.stage, manifest.StagedFingerprint) {
			return nil
		}
		return fmt.Errorf("staged output fingerprint does not match durable state: %w", ErrSessionNeedsReconciliation)
	}
	if pathExists(run.stage) {
		return fmt.Errorf("unexpected pre-existing staged output: %w", ErrSessionNeedsReconciliation)
	}
	inputFingerprint, err := run.processingInputFingerprint(manifest)
	if err != nil {
		return fmt.Errorf("fingerprint processing inputs: %w", err)
	}
	tools, err := run.operation.discoverFFmpeg()
	if err != nil {
		return fmt.Errorf("discover ffmpeg: %w", err)
	}
	if err := ensureOwnerDirectory(filepath.Join(run.workspace.Path(), directSessionPublishDir)); err != nil {
		return fmt.Errorf("prepare publication directory: %w", err)
	}
	processingWorkspace := ffmpeg.ProcessingWorkspace{
		OutputRoot: run.workspace.Path(), Directory: filepath.Join(run.workspace.Path(), multiSessionProcessingDir),
		OperationIdentity: run.planIdentity + ":processing", InputFingerprint: inputFingerprint,
	}
	inputs := make([]ffmpeg.MergeInput, 0, len(run.tracks))
	for _, track := range run.tracks {
		inputs = append(inputs, ffmpeg.MergeInput{Path: track.payload, HasAudio: selectionHasAudio(track.selection), HasVideo: selectionHasVideo(track.selection), Protocol: track.selection.Protocol})
	}
	if err := multiSessionMergeTracks(ctx, tools, inputs, run.stage, sink, processingWorkspace); err != nil {
		return fmt.Errorf("merge tracks: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	fingerprint, bytes, err := fingerprintFile(run.stage)
	if err != nil {
		return fmt.Errorf("fingerprint staged output: %w", err)
	}
	run.mutationMu.Lock()
	defer run.mutationMu.Unlock()
	manifest, err = run.snapshot()
	if err != nil {
		return err
	}
	if manifest.Phase != session.PhaseProcessing || manifest.Publication != session.PublicationNotStarted {
		return fmt.Errorf("processing state changed before staged-output commit: %w", ErrSessionNeedsReconciliation)
	}
	return run.workspace.SetStagedOutput(manifest.Revision, manifest.RunGeneration, fingerprint, bytes)
}

func (run *multiTrackSession) writeJournal(state, fingerprint string) error {
	return multiSessionJournalWrite(run.journal, directPublicationJournal{
		Version: directSessionJournalVersion, SessionID: run.operation.request.Filesystem.Resume.SessionID,
		State: state, Fingerprint: fingerprint, TargetKind: string(run.target.Kind), TargetIdentity: run.target.Identity,
		TargetBasename: run.target.Basename, UpdatedAt: time.Now().UTC(),
	})
}

func (run *multiTrackSession) reconcile() error {
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
		journal.TargetKind != string(run.target.Kind) || journal.TargetIdentity != run.target.Identity || journal.TargetBasename != manifest.RelativeDestination ||
		manifest.StagedFingerprint != "" && journal.Fingerprint != manifest.StagedFingerprint || manifest.Publication == session.PublicationIndeterminate && journal.State != directJournalIndeterminate {
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
	if manifest.Publication == session.PublicationIndeterminate && matchesFingerprint(run.stage, fingerprint) && !pathExists(destination) {
		if err := run.workspace.ResolvePublication(manifest.Revision, manifest.RunGeneration, session.PublicationPending, time.Now().UTC()); err != nil {
			return err
		}
		manifest, err = run.snapshot()
		if err != nil {
			return err
		}
		return run.workspace.ResolveReconciliation(manifest.Revision, manifest.RunGeneration, session.StatusFailed, session.DesiredRunning, time.Now().UTC())
	}
	return ErrSessionNeedsReconciliation
}

func (run *multiTrackSession) run(ctx context.Context, sink events.Sink) (result Result, runErr error) {
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
		err = fmt.Errorf("multi-track prepare: %w", err)
		result.Session.Disposition = SessionRecoveryRequired
		if isContextCancellation(err) {
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
	if manifest.Phase == session.PhaseDownloading {
		if err := run.downloadTracks(ctx, sink); err != nil {
			return run.handleInterruption(ctx, result, err)
		}
		manifest, err = run.snapshot()
		if err != nil {
			return result, err
		}
		for _, track := range run.tracks {
			component, ok := componentByID(manifest, track.id)
			if !ok {
				return result, ErrResumeIdentityMismatch
			}
			complete, completeErr := run.downloadedTrack(component, track)
			if completeErr != nil || !complete {
				if completeErr == nil {
					completeErr = ErrSessionNeedsReconciliation
				}
				return result, completeErr
			}
		}
		if err := run.workspace.Transition(manifest.Revision, manifest.RunGeneration, session.PhaseProcessing, session.StatusActive, session.DesiredRunning, time.Now().UTC()); err != nil {
			return result, err
		}
		manifest, err = run.snapshot()
		if err != nil {
			return result, err
		}
	}
	if manifest.Phase == session.PhaseProcessing {
		if err := run.process(ctx, sink); err != nil {
			err = fmt.Errorf("multi-track processing: %w", err)
			if errors.Is(err, ffmpeg.ErrProcessingCommitted) || errors.Is(err, ffmpeg.ErrProcessingReconciliation) {
				result.Session.Disposition = SessionRecoveryRequired
				result.Session.Phase = SessionPhase(session.PhaseProcessing)
				return result, err
			}
			return run.handleInterruption(ctx, result, err)
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

func (run *multiTrackSession) publishWithInterruption(ctx context.Context, result Result) (Result, error) {
	result, err := run.publish(ctx, result)
	if err != nil && (errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)) {
		return run.handleInterruption(ctx, result, err)
	}
	return result, err
}

func (run *multiTrackSession) publish(ctx context.Context, result Result) (Result, error) {
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
			reservation.MarkIndeterminate()
		}
	}()
	if err := run.writeJournal(directJournalPublishing, manifest.StagedFingerprint); err != nil {
		reservation.AbortBeforeReplace()
		finished = true
		markReadyPublicationResult(&result, manifest)
		return result, err
	}
	replaceResult, replaceErr := multiSessionNoReplace(run.stage, destination, manifest.StagedFingerprint)
	if replaceErr != nil {
		switch replaceResult {
		case directNoReplaceCollision, directNoReplacePrecommit:
			reservation.AbortBeforeReplace()
			finished = true
			markReadyPublicationResult(&result, manifest)
			if replaceResult == directNoReplaceCollision {
				journalErr := run.writeJournal(directJournalCollision, manifest.StagedFingerprint)
				stateErr := run.markCollision()
				result.Session.Disposition = SessionCollision
				result.Session.Publication = PublicationCollision
				result.Session.Phase = SessionPhase(manifest.Phase)
				return result, errors.Join(ErrDestinationCollision, replaceErr, journalErr, stateErr)
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

func (run *multiTrackSession) markCollision() error {
	manifest, err := run.snapshot()
	if err != nil {
		return err
	}
	if manifest.Status != session.StatusActive || manifest.Phase != session.PhaseReadyToPublish {
		return nil
	}
	return run.workspace.Transition(manifest.Revision, manifest.RunGeneration, session.PhaseReadyToPublish, session.StatusFailed, session.DesiredRunning, time.Now().UTC())
}

func (run *multiTrackSession) markPublicationIndeterminate() error {
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

func (run *multiTrackSession) handleInterruption(ctx context.Context, result Result, err error) (Result, error) {
	if err == nil {
		return result, nil
	}
	if ctx.Err() == nil {
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
	if closeErr := run.close(); closeErr != nil {
		result.Session.Disposition = SessionCleanupPending
		result.Session.Cleanup = CleanupPendingOutcome
		return result, errors.Join(err, closeErr)
	}
	handle, prepareErr := PrepareResumeDiscard(context.Background(), run.root, run.operation.request.Filesystem.Resume.SessionID)
	if prepareErr != nil {
		result.Session.Disposition = SessionCleanupPending
		result.Session.Cleanup = CleanupPendingOutcome
		return result, errors.Join(err, prepareErr)
	}
	_, discardErr := handle.Discard(context.Background())
	if discardErr != nil {
		result.Session.Disposition = SessionCleanupPending
		result.Session.Cleanup = CleanupPendingOutcome
		return result, errors.Join(err, discardErr)
	}
	result.Session.Disposition = SessionDiscarded
	result.Session.Cleanup = CleanupComplete
	return result, err
}

func (run *multiTrackSession) markPaused() error {
	run.mutationMu.Lock()
	defer run.mutationMu.Unlock()
	manifest, err := run.snapshot()
	if err != nil {
		return err
	}
	if manifest.Status != session.StatusActive {
		return nil
	}
	return run.workspace.Transition(manifest.Revision, manifest.RunGeneration, manifest.Phase, session.StatusPaused, session.DesiredPaused, time.Now().UTC())
}
