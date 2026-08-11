package fragment

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/tejasa97/youtube_dlp/internal/atomicfile"
)

const maxCheckpointCallbackDuration = 5 * time.Second

// ResumeBoundary is caller-owned durable authority for exactly the committed
// contiguous fragment prefix [0, Sequence). It is deliberately bounded and
// contains no request material, filesystem paths, or encryption secrets.
// Digest canonically binds the identity, structural plan, sequence, and local
// artifact metadata for that exact prefix.
type ResumeBoundary struct {
	ResumeIdentity string `json:"resume_identity"`
	PlanHash       string `json:"plan_hash"`
	Sequence       uint64 `json:"sequence"`
	Digest         string `json:"digest"`
	CommittedBytes int64  `json:"committed_bytes"`
}

// CommitSnapshot is the safe value synchronously delivered to OnCommit after
// the local ledger has atomically committed a newly advanced contiguous
// prefix. Callers may durably store it and pass ResumeBoundary on a later run.
type CommitSnapshot struct {
	ResumeIdentity string `json:"resume_identity"`
	PlanHash       string `json:"plan_hash"`
	Sequence       uint64 `json:"sequence"`
	Digest         string `json:"digest"`
	CommittedBytes int64  `json:"committed_bytes"`
}

// ResumeBoundary returns the snapshot as caller authority for a later run.
func (snapshot CommitSnapshot) ResumeBoundary() ResumeBoundary {
	return ResumeBoundary(snapshot)
}

// InitialResumeBoundary returns the canonical zero boundary for an identity
// and finite segment plan. Signed URLs and AES material influence neither the
// structural plan hash nor the returned value.
func InitialResumeBoundary(resumeIdentity string, segments []Segment) (ResumeBoundary, error) {
	if err := validateResumeIdentity(resumeIdentity); err != nil {
		return ResumeBoundary{}, err
	}
	if err := validateFiniteFragmentPlan(segments); err != nil {
		return ResumeBoundary{}, checkpointFailure(ErrInvalidCheckpoint, "fragment plan is not a valid finite plan", err)
	}
	planHash, err := checkpointPlanHash(segments)
	if err != nil {
		return ResumeBoundary{}, err
	}
	state := manifestState{Version: checkpointVersion, ResumeIdentity: resumeIdentity, PlanHash: planHash}
	snapshot, err := snapshotForPrefix(state, 0)
	if err != nil {
		return ResumeBoundary{}, err
	}
	return snapshot.ResumeBoundary(), nil
}

// Validate checks the bounded canonical shape of a caller boundary. Evidence
// for a nonzero digest is intentionally verified against the local ledger and
// fragment artifacts only when a job is opened.
func (boundary ResumeBoundary) Validate() error {
	if err := validateResumeIdentity(boundary.ResumeIdentity); err != nil {
		return err
	}
	if !canonicalSHA256(boundary.PlanHash) || !canonicalSHA256(boundary.Digest) {
		return checkpointFailure(ErrInvalidCheckpoint, "resume boundary hash is not canonical SHA-256", nil)
	}
	if boundary.Sequence > maxFragmentSegments || boundary.CommittedBytes < 0 || (boundary.Sequence == 0 && boundary.CommittedBytes != 0) || (boundary.Sequence > 0 && boundary.CommittedBytes == 0) {
		return checkpointFailure(ErrInvalidCheckpoint, "resume boundary sequence or byte count is invalid", nil)
	}
	return nil
}

// Validate checks the bounded canonical shape of a commit snapshot.
func (snapshot CommitSnapshot) Validate() error {
	return snapshot.ResumeBoundary().Validate()
}

type checkpointCallbackError struct{ cause error }

func (*checkpointCallbackError) Error() string         { return ErrCheckpointCallback.Error() }
func (failure *checkpointCallbackError) Unwrap() error { return failure.cause }
func (failure *checkpointCallbackError) Is(target error) bool {
	return target == ErrCheckpointCallback || errors.Is(failure.cause, target)
}

func validateResumeIdentity(identity string) error {
	if len(identity) == 0 || len(identity) > maxResumeIdentityBytes || !utf8.ValidString(identity) || strings.IndexFunc(identity, unicode.IsControl) >= 0 {
		return checkpointFailure(ErrInvalidCheckpoint, "resume identity is missing, oversized, contains a forbidden control, or is invalid UTF-8", nil)
	}
	return nil
}

func validateFiniteFragmentPlan(segments []Segment) error {
	if len(segments) == 0 {
		return ErrNoSegments
	}
	if len(segments) > maxFragmentSegments {
		return ErrTooManySegments
	}
	for index, segment := range segments {
		if segment.RangeStart < 0 || segment.RangeLength < 0 ||
			(segment.RangeLength == 0 && segment.RangeStart != 0) ||
			(segment.RangeLength > 0 && segment.RangeStart > int64(^uint64(0)>>1)-(segment.RangeLength-1)) {
			return fmt.Errorf("%w: segment %d", ErrInvalidSegmentRange, index+1)
		}
	}
	return nil
}

func canonicalSHA256(value string) bool {
	if len(value) != sha256.Size*2 || value != strings.ToLower(value) {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

type canonicalPrefix struct {
	Version        int                 `json:"version"`
	ResumeIdentity string              `json:"resume_identity"`
	PlanHash       string              `json:"plan_hash"`
	Sequence       uint64              `json:"sequence"`
	Artifacts      []canonicalArtifact `json:"artifacts"`
}

type canonicalArtifact struct {
	Index  uint64 `json:"index"`
	Bytes  int64  `json:"bytes"`
	SHA256 string `json:"sha256"`
}

func snapshotForPrefix(state manifestState, sequence uint64) (CommitSnapshot, error) {
	if sequence > uint64(maxFragmentSegments) || sequence > uint64(len(state.Artifacts)) && sequence > 0 {
		return CommitSnapshot{}, checkpointFailure(ErrInvalidCheckpoint, "checkpoint prefix sequence is invalid", nil)
	}
	prefix := canonicalPrefix{
		Version: checkpointVersion, ResumeIdentity: state.ResumeIdentity,
		PlanHash: state.PlanHash, Sequence: sequence,
		Artifacts: make([]canonicalArtifact, 0, int(sequence)),
	}
	var committedBytes int64
	for index := uint64(0); index < sequence; index++ {
		metadata, ok := state.Artifacts[int(index)]
		if !ok || metadata.Bytes <= 0 || !canonicalSHA256(metadata.SHA256) {
			return CommitSnapshot{}, checkpointFailure(ErrCheckpointReconciliation, "checkpoint prefix metadata is missing or invalid", nil)
		}
		if metadata.Bytes > math.MaxInt64-committedBytes {
			return CommitSnapshot{}, checkpointFailure(ErrInvalidCheckpoint, "checkpoint committed byte count overflows", nil)
		}
		committedBytes += metadata.Bytes
		prefix.Artifacts = append(prefix.Artifacts, canonicalArtifact{Index: index, Bytes: metadata.Bytes, SHA256: metadata.SHA256})
	}
	encoded, err := json.Marshal(prefix)
	if err != nil {
		return CommitSnapshot{}, err
	}
	digest := sha256.Sum256(encoded)
	snapshot := CommitSnapshot{
		ResumeIdentity: state.ResumeIdentity, PlanHash: state.PlanHash,
		Sequence: sequence, Digest: hex.EncodeToString(digest[:]), CommittedBytes: committedBytes,
	}
	if err := snapshot.Validate(); err != nil {
		return CommitSnapshot{}, err
	}
	return snapshot, nil
}

func contiguousSequence(state manifestState) uint64 {
	var sequence uint64
	for sequence < uint64(maxFragmentSegments) {
		if _, ok := state.Artifacts[int(sequence)]; !ok {
			return sequence
		}
		sequence++
	}
	return sequence
}

func reconcileResumeBoundary(
	workDir string,
	expectation manifestExpectation,
	boundary ResumeBoundary,
	write func(string, os.FileMode, func(io.Writer) error) error,
	resetOps checkpointResetOps,
) error {
	if err := boundary.Validate(); err != nil {
		return err
	}
	if boundary.ResumeIdentity != expectation.resumeIdentity {
		return checkpointFailure(ErrCheckpointReconciliation, "caller resume identity does not match the job", nil)
	}
	if boundary.PlanHash != expectation.planHash {
		return checkpointFailure(ErrCheckpointReconciliation, "caller fragment plan does not match the job", nil)
	}
	if boundary.Sequence > uint64(expectation.segmentCount) {
		return checkpointFailure(ErrInvalidCheckpoint, "caller sequence exceeds the finite fragment plan", nil)
	}
	if boundary.Sequence == 0 {
		empty, err := snapshotForPrefix(initialManifestState(expectation), 0)
		if err != nil {
			return err
		}
		if boundary != empty.ResumeBoundary() {
			return checkpointFailure(ErrCheckpointReconciliation, "caller zero boundary is not canonical for the job", nil)
		}
		return resetCheckpointWorkspace(workDir, expectation, resetOps)
	}

	info, err := os.Lstat(workDir)
	if errors.Is(err, os.ErrNotExist) {
		return checkpointFailure(ErrCheckpointReconciliation, "caller boundary has no local checkpoint workspace", nil)
	}
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return checkpointFailure(ErrInvalidCheckpoint, "fragment checkpoint workspace is a symlink or non-directory", nil)
	}
	if _, err := ensureProtectedCheckpointDirectory(expectation.outputRoot, workDir); err != nil {
		return err
	}
	if err := inspectRetainedEvidence(workDir, expectation.segmentCount); err != nil {
		return err
	}
	statePath := filepath.Join(workDir, "state.json")
	stateInfo, err := os.Lstat(statePath)
	if errors.Is(err, os.ErrNotExist) {
		return checkpointFailure(ErrCheckpointReconciliation, "caller boundary has no local checkpoint ledger", nil)
	}
	if err != nil {
		return err
	}
	if stateInfo.Mode()&os.ModeSymlink != 0 || !stateInfo.Mode().IsRegular() {
		return checkpointFailure(ErrInvalidCheckpoint, "checkpoint ledger is a symlink or non-regular file", nil)
	}
	state, err := readManifestState(statePath)
	if err != nil {
		return checkpointFailure(ErrInvalidCheckpoint, "checkpoint ledger is corrupt, trailing, or oversized", err)
	}
	if err := validateManifestState(state, expectation); err != nil {
		return err
	}
	for index := uint64(0); index < boundary.Sequence; index++ {
		metadata, ok := state.Artifacts[int(index)]
		if !ok {
			return checkpointFailure(ErrCheckpointReconciliation, "caller prefix is missing from the local ledger", nil)
		}
		matches, matchErr := artifactMatches(metadata, fragmentPath(workDir, int(index)))
		if matchErr != nil || !matches {
			return checkpointFailure(ErrCheckpointReconciliation, "caller prefix fragment evidence is missing or corrupt", matchErr)
		}
	}
	local, err := snapshotForPrefix(state, boundary.Sequence)
	if err != nil {
		return err
	}
	if local.ResumeBoundary() != boundary {
		return checkpointFailure(ErrCheckpointReconciliation, "caller prefix digest, byte count, or sequence does not match local evidence", nil)
	}
	return clampCheckpointWorkspace(workDir, statePath, state, boundary.Sequence, write)
}

type checkpointResetOps struct {
	readDir       func(string) ([]os.DirEntry, error)
	stat          func(string) (os.FileInfo, error)
	remove        func(string) error
	syncDirectory func(string) error
}

var productionCheckpointResetOps = checkpointResetOps{
	readDir:       os.ReadDir,
	stat:          os.Lstat,
	remove:        os.Remove,
	syncDirectory: syncCheckpointDirectory,
}

func resetCheckpointWorkspace(workDir string, expectation manifestExpectation, ops checkpointResetOps) error {
	if _, err := ops.stat(expectation.destination); err == nil {
		return checkpointFailure(ErrCheckpointReconciliation, "zero caller boundary cannot revoke an existing final destination", nil)
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	info, err := os.Lstat(workDir)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return checkpointFailure(ErrInvalidCheckpoint, "fragment checkpoint workspace is a symlink or non-directory", nil)
	}
	if _, err := ensureProtectedCheckpointDirectory(expectation.outputRoot, workDir); err != nil {
		return err
	}
	entries, err := ops.readDir(workDir)
	if err != nil {
		return err
	}
	ordinary := make([]string, 0, len(entries))
	statePath := ""
	for _, entry := range entries {
		name := entry.Name()
		index, validFragment := fragmentIndexFromName(name)
		switch {
		case name == publicationMarker || name == reconciliationMarker || name == finalPublicationEvidence || strings.HasPrefix(name, ".atomic-"):
			return checkpointFailure(ErrCheckpointReconciliation, "zero caller boundary cannot revoke retained publication or atomic evidence", nil)
		case name == "state.json":
			statePath = filepath.Join(workDir, name)
		case validFragment && index < expectation.segmentCount:
			ordinary = append(ordinary, filepath.Join(workDir, name))
		default:
			return checkpointFailure(ErrCheckpointReconciliation, "checkpoint reset found unknown caller data", nil)
		}
		entryInfo, infoErr := entry.Info()
		if infoErr != nil {
			return infoErr
		}
		if entryInfo.Mode()&os.ModeSymlink != 0 || !entryInfo.Mode().IsRegular() {
			return checkpointFailure(ErrInvalidCheckpoint, "checkpoint reset evidence is a symlink or non-regular file", nil)
		}
	}
	sort.Strings(ordinary)
	for _, path := range ordinary {
		if err := ops.remove(path); err != nil {
			return checkpointFailure(ErrCheckpointReconciliation, "checkpoint reset could not remove fragment evidence", err)
		}
	}
	if len(ordinary) > 0 {
		if err := ops.syncDirectory(workDir); err != nil {
			return checkpointFailure(ErrCheckpointReconciliation, "checkpoint reset did not settle fragment removal durably", err)
		}
	}
	if statePath != "" {
		if err := ops.remove(statePath); err != nil {
			return checkpointFailure(ErrCheckpointReconciliation, "checkpoint reset could not remove ledger authority", err)
		}
		if err := ops.syncDirectory(workDir); err != nil {
			return checkpointFailure(ErrCheckpointReconciliation, "checkpoint reset did not settle ledger removal durably", err)
		}
	}
	return nil
}

func clampCheckpointWorkspace(
	workDir, statePath string,
	state manifestState,
	sequence uint64,
	write func(string, os.FileMode, func(io.Writer) error) error,
) error {
	entries, err := os.ReadDir(workDir)
	if err != nil {
		return err
	}
	later := make([]string, 0)
	for _, entry := range entries {
		index, valid := fragmentIndexFromName(entry.Name())
		if valid && uint64(index) >= sequence {
			later = append(later, filepath.Join(workDir, entry.Name()))
		}
	}
	localAhead := len(later) > 0
	for index := range state.Artifacts {
		if uint64(index) >= sequence {
			localAhead = true
		}
	}
	if !localAhead {
		return nil
	}
	candidate := state
	candidate.Artifacts = make(map[int]artifact, int(sequence))
	for index := uint64(0); index < sequence; index++ {
		candidate.Artifacts[int(index)] = state.Artifacts[int(index)]
	}
	if err := writeManifestState(statePath, candidate, write); err != nil {
		var commitErr atomicfile.CommitError
		if errors.As(err, &commitErr) && (commitErr.Committed() || commitErr.Indeterminate()) {
			markerErr := writeReconciliationMarker(workDir, "caller-authoritative checkpoint clamp is uncertain")
			return checkpointFailure(ErrCheckpointReconciliation, "caller-authoritative ledger clamp did not settle durably", errors.Join(err, markerErr))
		}
		return checkpointFailure(ErrCheckpointReconciliation, "caller-authoritative ledger clamp failed", err)
	}
	sort.Strings(later)
	for _, path := range later {
		if err := os.Remove(path); err != nil {
			return checkpointFailure(ErrCheckpointReconciliation, "caller-authoritative clamp could not remove later fragment evidence", err)
		}
	}
	if len(later) > 0 {
		if err := syncCheckpointDirectory(workDir); err != nil {
			return checkpointFailure(ErrCheckpointReconciliation, "caller-authoritative fragment removal did not settle durably", err)
		}
	}
	return nil
}
