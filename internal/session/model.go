// Package session contains the durable, network-free state boundary for a
// resumable download session. It intentionally owns no downloader or engine
// execution paths; later integrations consume the portable WorkspaceRef and
// the validated manifest model.
package session

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/url"
	pathpkg "path"
	"sort"
	"strings"
	"time"
)

const (
	// ManifestSchemaVersion is the only manifest version understood by this
	// package. A future reader must reject versions it does not understand.
	ManifestSchemaVersion = 1

	SessionsDirectoryName   = ".ytdlp-sessions"
	ManifestFileName        = "manifest.json"
	LeaseFileName           = ".lease"
	CheckpointDirectoryName = "checkpoints"

	maxManifestBytes       = 1 << 20
	maxComponents          = 4096
	maxComponentIdentifier = 256
	maxCheckpointPath      = 1024
	maxSourceField         = 4096
	maxCheckpointValidator = 1024
)

var (
	ErrInvalidReference       = errors.New("invalid session reference")
	ErrUnsafePath             = errors.New("unsafe session path")
	ErrWorkspaceUnavailable   = errors.New("session workspace unavailable")
	ErrCorruptManifest        = errors.New("corrupt session manifest")
	ErrUnknownManifestVersion = errors.New("unknown session manifest version")
	ErrInvalidManifest        = errors.New("invalid session manifest")
	ErrLeaseContended         = errors.New("session lease is contended")
	ErrLeaseUnavailable       = errors.New("session lease unavailable")
	ErrMissingLease           = errors.New("session lease is missing")
	ErrPermissionUnavailable  = errors.New("session owner-only permissions unavailable")
	ErrInvalidTransition      = errors.New("invalid session transition")
	ErrStaleMutation          = errors.New("stale session mutation")
	ErrNeedsReconciliation    = errors.New("session needs reconciliation")
	ErrWorkspaceClosed        = errors.New("session workspace is closed")
	ErrMutationRejected       = errors.New("session mutation rejected")
	ErrManifestCommit         = errors.New("session manifest commit failed")
)

// Phase is the durable execution phase of a session. Dispositions such as
// paused and failed are deliberately not phases: retaining this value is what
// lets a paused processing run resume in processing.
type Phase string

const (
	PhasePrepared       Phase = "prepared"
	PhaseExtracting     Phase = "extracting"
	PhaseDownloading    Phase = "downloading"
	PhaseProcessing     Phase = "processing"
	PhaseReadyToPublish Phase = "ready_to_publish"
	PhaseCompleted      Phase = "completed"
)

// Status is the orthogonal durable disposition of the current Phase.
type Status string

const (
	StatusActive              Status = "active"
	StatusPaused              Status = "paused"
	StatusFailed              Status = "failed"
	StatusCanceled            Status = "canceled"
	StatusNeedsReconciliation Status = "needs_reconciliation"
	StatusCompleted           Status = "completed"
)

// DesiredState is the latest durable caller intent. It may briefly disagree
// with an active phase while a worker observes a pause or cancellation request.
type DesiredState string

const (
	DesiredRunning  DesiredState = "running"
	DesiredPaused   DesiredState = "paused"
	DesiredCanceled DesiredState = "canceled"
)

// PublicationState describes only durable publication authority. It does not
// contain a destination URL or any request material.
type PublicationState string

const (
	PublicationNotStarted    PublicationState = "not_started"
	PublicationPending       PublicationState = "pending"
	PublicationCommitted     PublicationState = "committed"
	PublicationIndeterminate PublicationState = "indeterminate"
)

// CleanupState records whether the session's local cleanup obligation is
// complete. Cleanup is deliberately represented as state, not as an implicit
// RemoveAll operation during inspection.
type CleanupState string

const (
	CleanupPending       CleanupState = "pending"
	CleanupComplete      CleanupState = "complete"
	CleanupIndeterminate CleanupState = "indeterminate"
)

// SourceIntent is the safe, canonical source identity retained for a future
// run. CanonicalURL is stripped of query, fragment, and user-info material;
// expiring media URLs and credentials therefore have no field in this model.
type SourceIntent struct {
	Provider     string `json:"provider,omitempty"`
	ID           string `json:"id,omitempty"`
	Kind         string `json:"kind,omitempty"`
	CanonicalURL string `json:"canonical_url,omitempty"`
}

// OutputIntent is the safe identity of the selected output plan. It contains
// no rendered path, URL, request field, or credential. PlanFingerprint must be
// a stable SHA-256 of the canonical output/selection plan.
type OutputIntent struct {
	PlanFingerprint string `json:"plan_fingerprint"`
	Container       string `json:"container,omitempty"`
	Extension       string `json:"extension,omitempty"`
	PlanIdentity    string `json:"plan_identity,omitempty"`
}

// CheckpointMetadata identifies a durable checkpoint without embedding its
// contents, request headers, or protocol URLs.
type CheckpointMetadata struct {
	RelativePath string `json:"relative_path,omitempty"`
	Digest       string `json:"digest,omitempty"`
	PlanHash     string `json:"plan_hash,omitempty"`
	Sequence     uint64 `json:"sequence,omitempty"`
	// ETag and LastModified are bounded remote representation validators. They
	// are safe checkpoint metadata, not request material; URLs, headers,
	// cookies, and credentials must never be added here.
	ETag         string `json:"etag,omitempty"`
	LastModified string `json:"last_modified,omitempty"`
	Total        int64  `json:"total,omitempty"`
}

// Component records both observed progress and the last progress known to be
// durable. ObservedBytes is advisory; resume must use CommittedBytes.
type Component struct {
	ID             string             `json:"id"`
	Kind           string             `json:"kind,omitempty"`
	ObservedBytes  int64              `json:"observed_bytes"`
	CommittedBytes int64              `json:"committed_bytes"`
	Checkpoint     CheckpointMetadata `json:"checkpoint,omitempty"`
}

// TransitionRecord is intentionally bounded to typed lifecycle values. It
// has no free-form reason field that could accidentally carry secrets.
type TransitionRecord struct {
	FromPhase  Phase     `json:"from_phase"`
	FromStatus Status    `json:"from_status"`
	ToPhase    Phase     `json:"to_phase"`
	ToStatus   Status    `json:"to_status"`
	At         time.Time `json:"at"`
}

// Manifest is the complete durable session image. The fields are limited to
// safe source/output intent, lifecycle state, component/checkpoint metadata,
// timestamps, and cleanup/publication state.
type Manifest struct {
	Version             int              `json:"version"`
	SessionID           string           `json:"session_id"`
	Revision            uint64           `json:"revision"`
	RunGeneration       uint64           `json:"run_generation"`
	Source              SourceIntent     `json:"source"`
	Output              OutputIntent     `json:"output"`
	RelativeDestination string           `json:"relative_destination"`
	Phase               Phase            `json:"phase"`
	Status              Status           `json:"status"`
	Desired             DesiredState     `json:"desired"`
	Components          []Component      `json:"components,omitempty"`
	CreatedAt           time.Time        `json:"created_at"`
	UpdatedAt           time.Time        `json:"updated_at"`
	LastTransition      TransitionRecord `json:"last_transition,omitempty"`
	Publication         PublicationState `json:"publication_state"`
	StagedFingerprint   string           `json:"staged_fingerprint,omitempty"`
	StagedBytes         int64            `json:"staged_bytes,omitempty"`
	Cleanup             CleanupState     `json:"cleanup_state"`
}

// WorkspaceRef is portable data for a future public engine API. It carries
// no open file, process, or lease handle.
type WorkspaceRef struct {
	OutputRoot         string `json:"output_root"`
	OutputRootIdentity string `json:"-"`
	SessionID          string `json:"session_id"`
}

// CreateOptions contains only safe session intent. RelativeDestination is a
// slash-separated path below OutputRoot and is validated before persistence.
type CreateOptions struct {
	OutputRoot string
	// OutputRootIdentity optionally binds the workspace to a previously
	// validated root. It is intentionally opaque and is never serialized.
	OutputRootIdentity  string
	Source              SourceIntent
	Output              OutputIntent
	RelativeDestination string
	Components          []Component
	Now                 func() time.Time
}

// ResumeBoundary tells a later transfer worker where it may safely resume.
// Data beyond DurableBytes must be truncated or ignored by that worker.
type ResumeBoundary struct {
	ComponentID   string
	ObservedBytes int64
	DurableBytes  int64
	DiscardBytes  int64
}

// Clone returns a deep copy suitable for handing to a caller or worker.
func (manifest Manifest) Clone() Manifest {
	manifest.Components = append([]Component(nil), manifest.Components...)
	return manifest
}

// ResumeBoundaries deliberately uses CommittedBytes rather than
// ObservedBytes. It is safe to call before a resume mutation.
func (manifest Manifest) ResumeBoundaries() []ResumeBoundary {
	boundaries := make([]ResumeBoundary, 0, len(manifest.Components))
	for _, component := range manifest.Components {
		discard := component.ObservedBytes - component.CommittedBytes
		if discard < 0 {
			discard = 0
		}
		boundaries = append(boundaries, ResumeBoundary{
			ComponentID:   component.ID,
			ObservedBytes: component.ObservedBytes,
			DurableBytes:  component.CommittedBytes,
			DiscardBytes:  discard,
		})
	}
	return boundaries
}

// PrepareResume makes the durable manifest agree with the only safe resume
// boundary. It does not touch component files; the next transfer integration
// uses the resulting CommittedBytes to truncate or ignore any extra bytes.
func (manifest *Manifest) PrepareResume() error {
	if manifest == nil {
		return ErrInvalidManifest
	}
	for index := range manifest.Components {
		manifest.Components[index].ObservedBytes = manifest.Components[index].CommittedBytes
	}
	return nil
}

// SetComponentProgress updates one component's progress. Callers must pass a
// CommittedBytes value only after the corresponding checkpoint is durable.
func (manifest *Manifest) SetComponentProgress(id string, observedBytes, committedBytes int64) error {
	if manifest == nil || !validIdentifier(id, maxComponentIdentifier) || observedBytes < 0 || committedBytes < 0 || committedBytes > observedBytes {
		return ErrInvalidManifest
	}
	for index := range manifest.Components {
		if manifest.Components[index].ID == id {
			if observedBytes < manifest.Components[index].ObservedBytes || committedBytes < manifest.Components[index].CommittedBytes || (committedBytes > 0 && manifest.Components[index].Checkpoint.RelativePath == "") {
				return ErrInvalidManifest
			}
			manifest.Components[index].ObservedBytes = observedBytes
			manifest.Components[index].CommittedBytes = committedBytes
			return nil
		}
	}
	return ErrInvalidManifest
}

// LifecycleState is the cross-product key used by the authoritative
// transition table. Phase and Status are never collapsed into one field.
type LifecycleState struct {
	Phase  Phase
	Status Status
}

var phaseTransitions = map[Phase]map[Phase]bool{
	PhasePrepared: {
		PhaseExtracting: true,
	},
	PhaseExtracting: {
		PhaseDownloading: true,
	},
	PhaseDownloading: {
		PhaseProcessing: true, PhaseReadyToPublish: true,
	},
	PhaseProcessing: {
		PhaseReadyToPublish: true,
	},
	PhaseReadyToPublish: {
		PhaseCompleted: true,
	},
}

var samePhaseStatusTransitions = map[Status]map[Status]bool{
	StatusActive: {
		StatusPaused: true, StatusFailed: true, StatusCanceled: true,
		StatusNeedsReconciliation: true,
	},
	StatusPaused: {
		StatusActive: true, StatusFailed: true, StatusCanceled: true,
		StatusNeedsReconciliation: true,
	},
	StatusFailed: {
		StatusActive: true, StatusPaused: true, StatusCanceled: true,
		StatusNeedsReconciliation: true,
	},
	StatusNeedsReconciliation: {
		StatusPaused: true, StatusFailed: true, StatusCanceled: true,
	},
	StatusCanceled: {
		StatusNeedsReconciliation: true,
	},
}

// CanTransition reports whether the lifecycle table permits one complete
// phase/status state to become another. Pause, failure, and reconciliation
// retain the original execution phase.
func CanTransition(from, to LifecycleState) bool {
	if !validLifecycleState(from) || !validLifecycleState(to) {
		return false
	}
	if from == to {
		return true
	}
	if from.Phase == PhaseCompleted || to.Phase == PhaseCompleted {
		switch {
		case from.Phase == PhaseReadyToPublish && (from.Status == StatusActive || from.Status == StatusNeedsReconciliation) && to == (LifecycleState{Phase: PhaseCompleted, Status: StatusCompleted}):
			return true
		case from == (LifecycleState{Phase: PhaseCompleted, Status: StatusCompleted}) && to == (LifecycleState{Phase: PhaseCompleted, Status: StatusNeedsReconciliation}):
			return true
		case from == (LifecycleState{Phase: PhaseCompleted, Status: StatusNeedsReconciliation}) && to == (LifecycleState{Phase: PhaseCompleted, Status: StatusCompleted}):
			return true
		default:
			return false
		}
	}
	if from.Phase == to.Phase {
		return samePhaseStatusTransitions[from.Status][to.Status]
	}
	return from.Status == StatusActive && to.Status == StatusActive && phaseTransitions[from.Phase][to.Phase]
}

// ValidateTransition validates the single authoritative cross-product table.
func ValidateTransition(from, to LifecycleState) error {
	if !CanTransition(from, to) {
		return ErrInvalidTransition
	}
	return nil
}

// Transition applies a complete phase/status/desired-state pair without
// changing the manifest revision. Workspace mutations add the revision only
// after the candidate image has passed validation and has been committed.
func (manifest *Manifest) Transition(nextPhase Phase, nextStatus Status, desired DesiredState, now time.Time) error {
	return manifest.transition(nextPhase, nextStatus, desired, now, false, false)
}

func (manifest *Manifest) transition(nextPhase Phase, nextStatus Status, desired DesiredState, now time.Time, allowReconciliationResult, allowTerminalReconciliation bool) error {
	if manifest == nil || now.IsZero() {
		return ErrInvalidTransition
	}
	if manifest.Status == StatusNeedsReconciliation && !allowReconciliationResult {
		return ErrInvalidTransition
	}
	if manifest.Status == StatusCompleted && !allowTerminalReconciliation {
		return ErrInvalidTransition
	}
	if nextStatus == StatusActive && manifest.Status != StatusActive {
		// Restart authority belongs exclusively to Resume so generation and
		// durable checkpoint normalization cannot be skipped.
		return ErrInvalidTransition
	}
	if manifest.Status == StatusActive && nextStatus == StatusActive && manifest.Desired != DesiredRunning {
		// A worker that observes a pending pause/cancel intent may not progress
		// the phase and overwrite that intent.
		return ErrInvalidTransition
	}
	if manifest.Status == StatusActive && nextStatus == StatusActive && desired != manifest.Desired {
		return ErrInvalidTransition
	}
	if !allowReconciliationResult && manifest.Desired != DesiredRunning && desired != manifest.Desired {
		return ErrInvalidTransition
	}
	from := LifecycleState{Phase: manifest.Phase, Status: manifest.Status}
	to := LifecycleState{Phase: nextPhase, Status: nextStatus}
	if err := ValidateTransition(from, to); err != nil {
		return err
	}
	if phaseRequiresDestination(nextPhase) && !isSafeRelativePath(manifest.RelativeDestination) {
		return ErrInvalidTransition
	}
	if manifest.RelativeDestination != "" && !isSafeRelativePath(manifest.RelativeDestination) {
		return ErrInvalidTransition
	}
	if !validDesiredForStatus(nextStatus, desired) {
		return ErrInvalidTransition
	}
	manifest.LastTransition = TransitionRecord{
		FromPhase: manifest.Phase, FromStatus: manifest.Status,
		ToPhase: nextPhase, ToStatus: nextStatus, At: now.UTC(),
	}
	manifest.Phase = nextPhase
	manifest.Status = nextStatus
	manifest.Desired = desired
	manifest.UpdatedAt = now.UTC()
	return nil
}

// TransitionTo is a descriptive alias for callers that prefer an explicit
// method name when applying a validated lifecycle state.
func (manifest *Manifest) TransitionTo(nextPhase Phase, nextStatus Status, desired DesiredState, now time.Time) error {
	return manifest.transition(nextPhase, nextStatus, desired, now, false, false)
}

// Resume normalizes advisory progress to durable checkpoint boundaries,
// retains the current execution phase, and starts a new run generation.
func (manifest *Manifest) Resume(now time.Time) error {
	if manifest == nil || now.IsZero() || (manifest.Status != StatusPaused && manifest.Status != StatusFailed) || manifest.Cleanup != CleanupPending {
		return ErrInvalidTransition
	}
	to := LifecycleState{Phase: manifest.Phase, Status: StatusActive}
	from := LifecycleState{Phase: manifest.Phase, Status: manifest.Status}
	if err := ValidateTransition(from, to); err != nil {
		return err
	}
	if manifest.RunGeneration == ^uint64(0) {
		return ErrInvalidManifest
	}
	if err := manifest.PrepareResume(); err != nil {
		return err
	}
	manifest.LastTransition = TransitionRecord{
		FromPhase: manifest.Phase, FromStatus: manifest.Status,
		ToPhase: manifest.Phase, ToStatus: StatusActive, At: now.UTC(),
	}
	manifest.Status = StatusActive
	manifest.Desired = DesiredRunning
	manifest.UpdatedAt = now.UTC()
	manifest.RunGeneration++
	return nil
}

// ResolveReconciliation is the only transition out of a reconciliation
// disposition. It deliberately cannot produce active; callers must provide a
// concrete paused, failed, canceled, or completed result.
func (manifest *Manifest) ResolveReconciliation(nextStatus Status, desired DesiredState, now time.Time) error {
	if manifest == nil || manifest.Status != StatusNeedsReconciliation || manifest.Publication == PublicationIndeterminate || manifest.Cleanup == CleanupIndeterminate || now.IsZero() {
		return ErrInvalidTransition
	}
	nextPhase := manifest.Phase
	if nextStatus == StatusCompleted {
		if manifest.Publication != PublicationCommitted || (manifest.Phase != PhaseReadyToPublish && manifest.Phase != PhaseCompleted) {
			return ErrInvalidTransition
		}
		nextPhase = PhaseCompleted
	} else if nextStatus != StatusPaused && nextStatus != StatusFailed && nextStatus != StatusCanceled {
		return ErrInvalidTransition
	}
	return manifest.transition(nextPhase, nextStatus, desired, now, true, false)
}

func validPhase(phase Phase) bool {
	switch phase {
	case PhasePrepared, PhaseExtracting, PhaseDownloading, PhaseProcessing, PhaseReadyToPublish, PhaseCompleted:
		return true
	default:
		return false
	}
}

func validStatus(status Status) bool {
	switch status {
	case StatusActive, StatusPaused, StatusFailed, StatusCanceled, StatusNeedsReconciliation, StatusCompleted:
		return true
	default:
		return false
	}
}

func validLifecycleState(state LifecycleState) bool {
	if !validPhase(state.Phase) || !validStatus(state.Status) {
		return false
	}
	if state.Phase == PhaseCompleted {
		return state.Status == StatusCompleted || state.Status == StatusNeedsReconciliation
	}
	return state.Status != StatusCompleted
}

func validDesired(desired DesiredState) bool {
	return desired == DesiredRunning || desired == DesiredPaused || desired == DesiredCanceled
}

func validDesiredForStatus(status Status, desired DesiredState) bool {
	if !validDesired(desired) {
		return false
	}
	switch status {
	case StatusPaused:
		return desired == DesiredPaused
	case StatusCanceled:
		return desired == DesiredCanceled
	case StatusCompleted:
		return desired == DesiredRunning
	default:
		return true
	}
}

func phaseRequiresDestination(phase Phase) bool {
	switch phase {
	case PhaseDownloading, PhaseProcessing, PhaseReadyToPublish, PhaseCompleted:
		return true
	default:
		return false
	}
}

func validPublicationTransition(current, next PublicationState, phase Phase) bool {
	if current == next {
		return true
	}
	switch current {
	case PublicationNotStarted:
		return next == PublicationPending && phase == PhaseReadyToPublish
	case PublicationPending:
		return phase == PhaseReadyToPublish && (next == PublicationCommitted || next == PublicationIndeterminate)
	default:
		return false
	}
}

func validCleanupTransition(current, next CleanupState) bool {
	if current == next {
		return true
	}
	if current != CleanupPending {
		return false
	}
	return next == CleanupComplete || next == CleanupIndeterminate
}

func terminalCleanupStatus(status Status) bool {
	return status == StatusFailed || status == StatusCanceled || status == StatusCompleted
}

func normalizeSource(source SourceIntent) (SourceIntent, error) {
	if !validIdentifier(source.Provider, maxSourceField) || source.Provider == "" || !validIdentifier(source.Kind, maxSourceField) || !validSafeText(source.ID, maxSourceField) {
		return SourceIntent{}, ErrInvalidManifest
	}
	if source.CanonicalURL == "" {
		if source.ID == "" {
			return SourceIntent{}, ErrInvalidManifest
		}
		return source, nil
	}
	if len(source.CanonicalURL) > maxSourceField {
		return SourceIntent{}, ErrInvalidManifest
	}
	parsed, err := url.Parse(source.CanonicalURL)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" || parsed.User != nil || parsed.Opaque != "" {
		return SourceIntent{}, ErrInvalidManifest
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return SourceIntent{}, ErrInvalidManifest
	}
	if strings.IndexByte(parsed.Host, 0) >= 0 || strings.ContainsAny(parsed.Host, "\r\n") {
		return SourceIntent{}, ErrInvalidManifest
	}
	parsed.Scheme = strings.ToLower(parsed.Scheme)
	parsed.Host = strings.ToLower(parsed.Host)
	parsed.RawQuery = ""
	parsed.Fragment = ""
	parsed.ForceQuery = false
	parsed.RawPath = ""
	parsed.Path = pathpkg.Clean("/" + strings.TrimPrefix(parsed.Path, "/"))
	if parsed.Path == "." {
		parsed.Path = "/"
	}
	source.CanonicalURL = parsed.String()
	return source, nil
}

// OutputPlanFingerprint returns the stable identity of a canonical output
// plan. Callers should canonicalize their plan before hashing it.
func OutputPlanFingerprint(plan string) string {
	digest := sha256.Sum256([]byte(plan))
	return hex.EncodeToString(digest[:])
}

func normalizeOutputIntent(output OutputIntent) (OutputIntent, error) {
	if output.PlanFingerprint == "" && output.PlanIdentity != "" {
		output.PlanFingerprint = OutputPlanFingerprint(output.PlanIdentity)
	}
	if len(output.PlanFingerprint) != sha256.Size*2 || output.PlanFingerprint != strings.ToLower(output.PlanFingerprint) {
		return OutputIntent{}, ErrInvalidManifest
	}
	if _, err := hex.DecodeString(output.PlanFingerprint); err != nil {
		return OutputIntent{}, ErrInvalidManifest
	}
	if !validIdentifier(output.Container, maxComponentIdentifier) || output.Container == "" || !validIdentifier(output.Extension, maxComponentIdentifier) || output.Extension == "" || !validPlanText(output.PlanIdentity) || output.PlanIdentity == "" {
		return OutputIntent{}, ErrInvalidManifest
	}
	if output.PlanFingerprint != OutputPlanFingerprint(output.PlanIdentity) {
		return OutputIntent{}, ErrInvalidManifest
	}
	return output, nil
}

func validPlanText(value string) bool {
	if value == "" || len(value) > maxSourceField || strings.IndexByte(value, 0) >= 0 {
		return false
	}
	for _, character := range value {
		if (character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z') || (character >= '0' && character <= '9') || strings.ContainsRune("._:-+@,[]()", character) {
			continue
		}
		return false
	}
	return true
}

func validSessionID(value string) bool {
	if len(value) != 32 {
		return false
	}
	for _, character := range value {
		if (character >= '0' && character <= '9') || (character >= 'a' && character <= 'f') {
			continue
		}
		return false
	}
	return true
}

func normalizeComponents(components []Component) ([]Component, error) {
	if len(components) > maxComponents {
		return nil, ErrInvalidManifest
	}
	result := append([]Component(nil), components...)
	for index := range result {
		component := &result[index]
		if !validIdentifier(component.ID, maxComponentIdentifier) || component.ID == "" || !validIdentifier(component.Kind, maxComponentIdentifier) || component.ObservedBytes < 0 || component.CommittedBytes < 0 || component.CommittedBytes > component.ObservedBytes {
			return nil, ErrInvalidManifest
		}
		if err := validateCheckpoint(component.Checkpoint); err != nil {
			return nil, err
		}
		if component.CommittedBytes > 0 && component.Checkpoint.RelativePath == "" {
			return nil, ErrInvalidManifest
		}
	}
	sort.Slice(result, func(left, right int) bool { return result[left].ID < result[right].ID })
	for index := 1; index < len(result); index++ {
		if result[index-1].ID == result[index].ID {
			return nil, ErrInvalidManifest
		}
	}
	checkpointPaths := make([]string, 0, len(result))
	for _, component := range result {
		if component.Checkpoint.RelativePath != "" {
			checkpointPaths = append(checkpointPaths, component.Checkpoint.RelativePath)
		}
	}
	sort.Strings(checkpointPaths)
	for index := 1; index < len(checkpointPaths); index++ {
		previous := checkpointPaths[index-1]
		current := checkpointPaths[index]
		if current == previous || strings.HasPrefix(current, previous+"/") {
			return nil, ErrUnsafePath
		}
	}
	return result, nil
}

func validateCheckpoint(checkpoint CheckpointMetadata) error {
	if checkpoint.RelativePath != "" {
		if len(checkpoint.RelativePath) > maxCheckpointPath || !isCheckpointRelativePath(checkpoint.RelativePath) {
			return ErrUnsafePath
		}
	} else if checkpoint.Digest != "" || checkpoint.PlanHash != "" || checkpoint.Sequence != 0 || checkpoint.ETag != "" || checkpoint.LastModified != "" || checkpoint.Total != 0 {
		return ErrInvalidManifest
	}
	if checkpoint.Digest != "" {
		if len(checkpoint.Digest) != sha256.Size*2 {
			return ErrInvalidManifest
		}
		if _, err := hex.DecodeString(checkpoint.Digest); err != nil || checkpoint.Digest != strings.ToLower(checkpoint.Digest) {
			return ErrInvalidManifest
		}
	}
	if checkpoint.PlanHash != "" {
		if len(checkpoint.PlanHash) != sha256.Size*2 || checkpoint.PlanHash != strings.ToLower(checkpoint.PlanHash) {
			return ErrInvalidManifest
		}
		if _, err := hex.DecodeString(checkpoint.PlanHash); err != nil {
			return ErrInvalidManifest
		}
	}
	if checkpoint.Sequence > 0 && (checkpoint.Digest == "" || checkpoint.PlanHash == "") {
		return ErrInvalidManifest
	}
	if len(checkpoint.ETag) > maxCheckpointValidator || strings.ContainsAny(checkpoint.ETag, "\x00\r\n") ||
		len(checkpoint.LastModified) > maxCheckpointValidator || strings.ContainsAny(checkpoint.LastModified, "\x00\r\n") ||
		checkpoint.Total < 0 || checkpoint.Total > 8<<30 {
		return ErrInvalidManifest
	}
	return nil
}

func isCheckpointRelativePath(value string) bool {
	if !isSafeRelativePath(value) || !strings.HasPrefix(value, CheckpointDirectoryName+"/") {
		return false
	}
	for _, component := range strings.Split(value, "/") {
		if component == ManifestFileName || component == LeaseFileName || strings.HasPrefix(component, ".atomic-") {
			return false
		}
	}
	return true
}

func validIdentifier(value string, limit int) bool {
	if value == "" {
		return true
	}
	if len(value) > limit || strings.IndexByte(value, 0) >= 0 {
		return false
	}
	for _, character := range value {
		if (character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z') || (character >= '0' && character <= '9') || strings.ContainsRune("._:-", character) {
			continue
		}
		return false
	}
	return value != "." && value != ".."
}

func validSafeText(value string, limit int) bool {
	if value == "" {
		return true
	}
	if len(value) > limit || strings.IndexByte(value, 0) >= 0 || strings.ContainsAny(value, "\r\n\t") {
		return false
	}
	return !strings.Contains(value, "://") && !strings.ContainsAny(value, "?#\\")
}

// Validate checks a decoded or caller-built manifest and rejects noncanonical
// forms before they can be persisted.
func (manifest Manifest) Validate() error {
	if manifest.Version != ManifestSchemaVersion {
		if manifest.Version > ManifestSchemaVersion {
			return ErrUnknownManifestVersion
		}
		return ErrInvalidManifest
	}
	if !validSessionID(manifest.SessionID) || manifest.Revision == 0 || manifest.RunGeneration == 0 || manifest.CreatedAt.IsZero() || manifest.UpdatedAt.IsZero() || manifest.CreatedAt.After(manifest.UpdatedAt) || manifest.CreatedAt.Location() != time.UTC || manifest.UpdatedAt.Location() != time.UTC {
		return ErrInvalidManifest
	}
	source, err := normalizeSource(manifest.Source)
	if err != nil || source != manifest.Source {
		return ErrInvalidManifest
	}
	output, err := normalizeOutputIntent(manifest.Output)
	if err != nil || output != manifest.Output {
		return ErrInvalidManifest
	}
	if manifest.RelativeDestination != "" && !isSafeRelativePath(manifest.RelativeDestination) {
		return ErrUnsafePath
	}
	if phaseRequiresDestination(manifest.Phase) && manifest.RelativeDestination == "" {
		return ErrInvalidManifest
	}
	if !validLifecycleState(LifecycleState{Phase: manifest.Phase, Status: manifest.Status}) || !validDesiredForStatus(manifest.Status, manifest.Desired) {
		return ErrInvalidManifest
	}
	if manifest.Publication != PublicationNotStarted && manifest.Publication != PublicationPending && manifest.Publication != PublicationCommitted && manifest.Publication != PublicationIndeterminate {
		return ErrInvalidManifest
	}
	if manifest.StagedFingerprint != "" {
		if len(manifest.StagedFingerprint) != sha256.Size*2 || manifest.StagedFingerprint != strings.ToLower(manifest.StagedFingerprint) {
			return ErrInvalidManifest
		}
		if _, err := hex.DecodeString(manifest.StagedFingerprint); err != nil {
			return ErrInvalidManifest
		}
	}
	if manifest.StagedBytes < 0 || manifest.StagedBytes > 8<<30 {
		return ErrInvalidManifest
	}
	if manifest.Cleanup != CleanupPending && manifest.Cleanup != CleanupComplete && manifest.Cleanup != CleanupIndeterminate {
		return ErrInvalidManifest
	}
	if manifest.Phase == PhaseCompleted && manifest.Publication != PublicationCommitted {
		return ErrInvalidManifest
	}
	if manifest.Publication == PublicationPending && manifest.Phase != PhaseReadyToPublish {
		return ErrInvalidManifest
	}
	if manifest.Publication == PublicationCommitted && manifest.Phase != PhaseReadyToPublish && manifest.Phase != PhaseCompleted {
		return ErrInvalidManifest
	}
	if manifest.Publication == PublicationIndeterminate && manifest.Status != StatusNeedsReconciliation {
		return ErrInvalidManifest
	}
	if manifest.Cleanup == CleanupComplete && manifest.Status != StatusCompleted && manifest.Status != StatusFailed && manifest.Status != StatusCanceled && manifest.Status != StatusNeedsReconciliation {
		return ErrInvalidManifest
	}
	if manifest.Cleanup == CleanupIndeterminate && manifest.Status != StatusNeedsReconciliation {
		return ErrInvalidManifest
	}
	if manifest.Cleanup != CleanupPending && manifest.Status == StatusNeedsReconciliation && !terminalCleanupStatus(manifest.LastTransition.FromStatus) {
		return ErrInvalidManifest
	}
	if manifest.LastTransition.At.IsZero() {
		if manifest.LastTransition.FromPhase != "" || manifest.LastTransition.FromStatus != "" || manifest.LastTransition.ToPhase != "" || manifest.LastTransition.ToStatus != "" || manifest.Phase != PhasePrepared || manifest.Status != StatusActive {
			return ErrInvalidManifest
		}
	} else if manifest.LastTransition.At.Location() != time.UTC || !validLifecycleState(LifecycleState{Phase: manifest.LastTransition.FromPhase, Status: manifest.LastTransition.FromStatus}) || !validLifecycleState(LifecycleState{Phase: manifest.LastTransition.ToPhase, Status: manifest.LastTransition.ToStatus}) || manifest.LastTransition.ToPhase != manifest.Phase || manifest.LastTransition.ToStatus != manifest.Status || manifest.LastTransition.At.Before(manifest.CreatedAt) || manifest.LastTransition.At.After(manifest.UpdatedAt) {
		return ErrInvalidManifest
	}
	components, err := normalizeComponents(manifest.Components)
	if err != nil || len(components) != len(manifest.Components) {
		return err
	}
	for index := range components {
		if components[index] != manifest.Components[index] {
			return ErrInvalidManifest
		}
	}
	return nil
}

func isSafeRelativePath(value string) bool {
	if value == "" || strings.IndexByte(value, 0) >= 0 || strings.Contains(value, "\\") || strings.Contains(value, ":") || strings.HasPrefix(value, "/") || strings.Contains(value, "//") {
		return false
	}
	clean := pathpkg.Clean(value)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") || clean != value {
		return false
	}
	return true
}
