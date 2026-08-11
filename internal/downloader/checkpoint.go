package downloader

import (
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/tejasa97/youtube_dlp/internal/network"
)

var (
	// ErrInvalidCheckpoint reports an invalid opt-in checkpoint contract or a
	// boundary that cannot safely authorize a partial payload.
	ErrInvalidCheckpoint = errors.New("invalid download checkpoint")
	// ErrCheckpointCommit reports a local checkpoint image that could not be
	// committed. The wrapped error retains the atomicfile commit outcome.
	ErrCheckpointCommit = errors.New("download checkpoint commit failed")
	// ErrCheckpointCallback reports a caller callback that rejected a locally
	// committed checkpoint. The local state remains the last committed image;
	// caller authority is never advanced after this error.
	ErrCheckpointCallback = errors.New("download checkpoint callback failed")
	// ErrCheckpointReconciliation reports retained atomic replacement evidence
	// whose commit authority cannot be safely inferred by this downloader.
	ErrCheckpointReconciliation = errors.New("download checkpoint reconciliation required")
)

type checkpointCallbackError struct{ cause error }

func (err *checkpointCallbackError) Error() string { return ErrCheckpointCallback.Error() }

func (err *checkpointCallbackError) Unwrap() error { return err.cause }

func (err *checkpointCallbackError) Is(target error) bool {
	return target == ErrCheckpointCallback || errors.Is(err.cause, target)
}

type checkpointSafeError struct {
	message string
	cause   error
}

func (err *checkpointSafeError) Error() string { return err.message }

func (err *checkpointSafeError) Unwrap() error { return err.cause }

func redactCheckpointError(rawURL string, cause error) error {
	if cause == nil {
		return nil
	}
	message := strings.ReplaceAll(cause.Error(), rawURL, network.RedactRawURL(rawURL))
	if message == cause.Error() {
		return cause
	}
	return &checkpointSafeError{message: message, cause: cause}
}

const (
	// Checkpoint cadence is intentionally bounded. A small lower bound keeps a
	// caller from turning every response read into an fsync, while the upper
	// bound prevents a single unobserved tail from becoming needlessly large.
	minDirectCheckpointBytes         int64 = 64 << 10
	defaultDirectCheckpointBytes     int64 = 1 << 20
	maxDirectCheckpointBytes         int64 = 64 << 20
	minDirectCheckpointInterval            = 10 * time.Millisecond
	defaultDirectCheckpointInterval        = time.Second
	maxDirectCheckpointInterval            = 10 * time.Minute
	maxDirectCheckpointLocalDuration       = 5 * time.Second
	maxCheckpointIdentityBytes             = 512
	maxCheckpointValidatorBytes            = 1024
	maxCheckpointStateBytes                = 8 << 10
)

// Checkpoint is the safe, durable progress image exchanged with a caller.
//
// It deliberately contains no URL, headers, cookies, credentials, or other
// request material. CommittedBytes is the payload prefix that the local state
// file has durably committed and that a caller may make authoritative in its
// own manifest. Bytes observed by the HTTP reader are intentionally not part
// of this contract.
type Checkpoint struct {
	ResumeIdentity string
	ETag           string
	LastModified   string
	Total          int64
	CommittedBytes int64
}

// CheckpointOptions opts a direct HTTP Job into durable partial progress.
//
// ResumeBoundary, when present, is caller authority read from a durable
// session manifest. Local payload bytes beyond that boundary are discarded
// before they can be used for a Range request. OnCommit is synchronous and is
// called only after the payload has been synced and the local state image has
// been atomically committed. The callback receives only a Checkpoint value.
type CheckpointOptions struct {
	ResumeBoundary *Checkpoint
	EveryBytes     int64
	EveryDuration  time.Duration
	OnCommit       func(Checkpoint) error
}

type checkpointPlan struct {
	enabled       bool
	boundary      *Checkpoint
	everyBytes    int64
	everyDuration time.Duration
	onCommit      func(Checkpoint) error
}

func checkpointPlanForJob(job Job) (checkpointPlan, error) {
	if job.Checkpoint == nil {
		return checkpointPlan{}, nil
	}
	if job.NoPart {
		return checkpointPlan{}, fmt.Errorf("%w: NoPart cannot be combined with durable checkpoints", ErrInvalidCheckpoint)
	}
	options := job.Checkpoint
	if job.ResumeIdentity != "" {
		if err := validateCheckpointText(job.ResumeIdentity, maxCheckpointIdentityBytes, "resume identity"); err != nil {
			return checkpointPlan{}, err
		}
	}

	plan := checkpointPlan{
		enabled:       true,
		everyBytes:    options.EveryBytes,
		everyDuration: options.EveryDuration,
		onCommit:      options.OnCommit,
	}
	if plan.everyBytes == 0 {
		plan.everyBytes = defaultDirectCheckpointBytes
	}
	if plan.everyDuration == 0 {
		plan.everyDuration = defaultDirectCheckpointInterval
	}
	if plan.everyBytes < minDirectCheckpointBytes || plan.everyBytes > maxDirectCheckpointBytes ||
		plan.everyDuration < minDirectCheckpointInterval || plan.everyDuration > maxDirectCheckpointInterval {
		return checkpointPlan{}, fmt.Errorf("%w: checkpoint cadence is outside bounded limits", ErrInvalidCheckpoint)
	}

	if options.ResumeBoundary != nil {
		boundary := *options.ResumeBoundary
		if err := validateCheckpoint(boundary); err != nil {
			return checkpointPlan{}, err
		}
		if boundary.ResumeIdentity != job.ResumeIdentity {
			return checkpointPlan{}, fmt.Errorf("%w: resume identity does not match job", ErrInvalidCheckpoint)
		}
		plan.boundary = &boundary
	}
	return plan, nil
}

func validateCheckpoint(checkpoint Checkpoint) error {
	if err := validateCheckpointText(checkpoint.ResumeIdentity, maxCheckpointIdentityBytes, "resume identity"); err != nil {
		return err
	}
	if err := validateCheckpointText(checkpoint.ETag, maxCheckpointValidatorBytes, "etag"); err != nil {
		return err
	}
	if err := validateCheckpointText(checkpoint.LastModified, maxCheckpointValidatorBytes, "last-modified"); err != nil {
		return err
	}
	if checkpoint.Total < 0 || checkpoint.Total > maxDirectBytes || checkpoint.CommittedBytes < 0 || checkpoint.CommittedBytes > maxDirectBytes {
		return fmt.Errorf("%w: checkpoint byte bounds", ErrInvalidCheckpoint)
	}
	if checkpoint.Total > 0 && checkpoint.CommittedBytes > checkpoint.Total {
		return fmt.Errorf("%w: committed bytes exceed total", ErrInvalidCheckpoint)
	}
	return nil
}

func validateCheckpointText(value string, limit int, name string) error {
	if len(value) > limit || strings.ContainsAny(value, "\x00\r\n") {
		return fmt.Errorf("%w: invalid %s", ErrInvalidCheckpoint, name)
	}
	return nil
}

func (plan checkpointPlan) matchesBoundary(state partialState, job Job) bool {
	if plan.boundary == nil {
		return true
	}
	boundary := plan.boundary
	if boundary.ResumeIdentity != "" && state.ResumeIdentity != boundary.ResumeIdentity {
		return false
	}
	if boundary.ResumeIdentity == "" && job.ResumeIdentity != "" && state.ResumeIdentity != job.ResumeIdentity {
		return false
	}
	if boundary.ETag != "" && state.ETag != "" && state.ETag != boundary.ETag {
		return false
	}
	if boundary.LastModified != "" && state.LastModified != "" && state.LastModified != boundary.LastModified {
		return false
	}
	if boundary.Total > 0 && state.Total > 0 && state.Total != boundary.Total {
		return false
	}
	return true
}

func (plan checkpointPlan) responseMatchesBoundary(response *http.Response) bool {
	if plan.boundary == nil {
		return true
	}
	if plan.boundary.ETag != "" && response.Header.Get("ETag") != plan.boundary.ETag {
		return false
	}
	if plan.boundary.LastModified != "" && response.Header.Get("Last-Modified") != plan.boundary.LastModified {
		return false
	}
	return true
}

func (plan checkpointPlan) due(observed, lastCommitted int64, lastAt, now time.Time) bool {
	if !plan.enabled {
		return false
	}
	if observed-lastCommitted >= plan.everyBytes {
		return true
	}
	return !now.Before(lastAt) && now.Sub(lastAt) >= plan.everyDuration
}

func checkpointFromPartial(state partialState) Checkpoint {
	return Checkpoint{
		ResumeIdentity: state.ResumeIdentity,
		ETag:           state.ETag,
		LastModified:   state.LastModified,
		Total:          state.Total,
		CommittedBytes: state.CommittedBytes,
	}
}

func validatePartialCheckpointState(state partialState) error {
	if state.ResumeIdentity != "" && state.URL != "" {
		return fmt.Errorf("%w: stable identity state contains a URL", ErrInvalidCheckpoint)
	}
	if err := validateCheckpoint(checkpointFromPartial(state)); err != nil {
		return err
	}
	return nil
}

func checkPartialStateEvidence(path string) error {
	entries, err := os.ReadDir(filepath.Dir(path))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("%w: inspect atomic state evidence", ErrCheckpointReconciliation)
	}
	for _, entry := range entries {
		name := entry.Name()
		// atomicfile intentionally keeps an indeterminate replacement candidate
		// under these names. Its generic prefix cannot be associated with a
		// destination without reopening the evidence, so opt-in resume fails
		// closed for any retained candidate in the state directory.
		if strings.HasPrefix(name, ".atomic-") || strings.HasPrefix(name, ".atomic-backup-") {
			return ErrCheckpointReconciliation
		}
	}
	return nil
}
