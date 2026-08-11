package fragment

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/tejasa97/youtube_dlp/internal/atomicfile"
)

const (
	checkpointVersion        = 1
	maxManifestBytes         = 4 << 20
	reconciliationMarker     = "reconcile.json"
	publicationMarker        = "publication.json"
	finalPublicationEvidence = "assembled.part"
)

// artifactManifest records content digests only after each fragment has been
// fully written, synced, and atomically published. Durable mode binds those
// records to caller-owned identity; legacy mode retains the previous plan-hash
// behavior for callers that do not opt in.
type artifactManifest struct {
	path             string
	workDir          string
	state            manifestState
	durable          bool
	write            func(string, os.FileMode, func(io.Writer) error) error
	poisoned         bool
	onCommit         func(context.Context, CommitSnapshot) error
	callbackTimeout  time.Duration
	notifiedSequence uint64
	callerSequence   uint64
	callbackErr      error
	mu               sync.Mutex
}

type manifestState struct {
	Version        int              `json:"version,omitempty"`
	ResumeIdentity string           `json:"resume_identity,omitempty"`
	PlanHash       string           `json:"plan_hash,omitempty"`
	Hash           string           `json:"hash,omitempty"`
	Artifacts      map[int]artifact `json:"artifacts,omitempty"`
}

type artifact struct {
	Bytes  int64  `json:"bytes"`
	SHA256 string `json:"sha256"`
}

type manifestExpectation struct {
	durable        bool
	outputRoot     string
	destination    string
	overwrite      bool
	resumeIdentity string
	planHash       string
	legacyHash     string
	segmentCount   int
}

func manifestExpectationFor(job Job) (manifestExpectation, error) {
	expectation := manifestExpectation{
		segmentCount: len(job.Segments), outputRoot: job.OutputRoot,
		destination: job.Destination, overwrite: job.Overwrite,
	}
	if job.Checkpoint == nil {
		hash, err := planHash(job.Segments)
		if err != nil {
			return manifestExpectation{}, err
		}
		expectation.legacyHash = hash
		return expectation, nil
	}
	planHash, err := checkpointPlanHash(job.Segments)
	if err != nil {
		return manifestExpectation{}, err
	}
	expectation.durable = true
	expectation.resumeIdentity = job.Checkpoint.ResumeIdentity
	expectation.planHash = planHash
	return expectation, nil
}

func openArtifactManifest(
	workDir string,
	expectation manifestExpectation,
	write func(string, os.FileMode, func(io.Writer) error) error,
) (*artifactManifest, error) {
	created, err := ensureWorkDirectory(workDir, expectation)
	if err != nil {
		return nil, err
	}
	if expectation.durable {
		if err := inspectRetainedEvidence(workDir, expectation.segmentCount); err != nil {
			return nil, err
		}
	}

	statePath := filepath.Join(workDir, "state.json")
	info, statErr := os.Lstat(statePath)
	if errors.Is(statErr, os.ErrNotExist) {
		if expectation.durable {
			entries, readErr := os.ReadDir(workDir)
			if readErr != nil {
				return nil, readErr
			}
			if len(entries) != 0 {
				return nil, checkpointFailure(ErrCheckpointReconciliation, "checkpoint ledger is missing while prior work remains", nil)
			}
			if !created && expectation.overwrite {
				if _, destinationErr := os.Lstat(expectation.destination); destinationErr == nil {
					return nil, checkpointFailure(ErrCheckpointReconciliation, "empty retained checkpoint boundary accompanies an existing final destination", nil)
				} else if !errors.Is(destinationErr, os.ErrNotExist) {
					return nil, destinationErr
				}
			}
		}
		state := initialManifestState(expectation)
		if err := writeManifestState(statePath, state, write); err != nil {
			if expectation.durable {
				return nil, classifyInitialManifestWrite(workDir, err)
			}
			return nil, err
		}
		return &artifactManifest{path: statePath, workDir: workDir, state: state, durable: expectation.durable, write: write}, nil
	}
	if statErr != nil {
		return nil, statErr
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		if expectation.durable {
			return nil, checkpointFailure(ErrInvalidCheckpoint, "checkpoint ledger is a symlink or non-regular file", nil)
		}
		return nil, ErrUnsafeDestination
	}

	state, err := readManifestState(statePath)
	if err != nil {
		if expectation.durable {
			return nil, checkpointFailure(ErrInvalidCheckpoint, "checkpoint ledger is corrupt, trailing, or oversized", err)
		}
		return resetLegacyManifest(workDir, expectation, write)
	}
	if err := validateManifestState(state, expectation); err != nil {
		if expectation.durable {
			return nil, err
		}
		return resetLegacyManifest(workDir, expectation, write)
	}
	manifest := &artifactManifest{path: statePath, workDir: workDir, state: state, durable: expectation.durable, write: write}
	if expectation.durable {
		if err := manifest.validateCommittedArtifacts(expectation.segmentCount); err != nil {
			return nil, err
		}
	}
	return manifest, nil
}

func ensureWorkDirectory(path string, expectation manifestExpectation) (bool, error) {
	if expectation.durable {
		return ensureProtectedCheckpointDirectory(expectation.outputRoot, path)
	}
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		if err := os.MkdirAll(path, 0o755); err != nil {
			return false, err
		}
		return true, nil
	}
	if err != nil {
		return false, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return false, ErrUnsafeDestination
	}
	return false, nil
}

func ensureProtectedCheckpointDirectory(root, path string) (bool, error) {
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return false, checkpointFailure(ErrInvalidCheckpoint, "resolve checkpoint output root", err)
	}
	pathAbs, err := filepath.Abs(path)
	if err != nil {
		return false, checkpointFailure(ErrInvalidCheckpoint, "resolve checkpoint directory", err)
	}
	relative, err := filepath.Rel(rootAbs, pathAbs)
	if err != nil || relative == "." || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return false, checkpointFailure(ErrInvalidCheckpoint, "checkpoint directory is not a dedicated child of output root", err)
	}
	rootInfo, err := os.Lstat(rootAbs)
	if err != nil {
		return false, checkpointFailure(ErrInvalidCheckpoint, "inspect checkpoint output root", err)
	}
	if rootInfo.Mode()&os.ModeSymlink != 0 || !rootInfo.IsDir() {
		return false, checkpointFailure(ErrInvalidCheckpoint, "checkpoint output root is a symlink or non-directory", nil)
	}
	current := rootAbs
	createdFinal := false
	parts := strings.Split(relative, string(filepath.Separator))
	for index, part := range parts {
		if part == "" || part == "." || part == ".." {
			return false, checkpointFailure(ErrInvalidCheckpoint, "checkpoint directory chain is invalid", nil)
		}
		current = filepath.Join(current, part)
		info, statErr := os.Lstat(current)
		if errors.Is(statErr, os.ErrNotExist) {
			if mkdirErr := createProtectedCheckpointDirectory(current); mkdirErr != nil {
				if !errors.Is(mkdirErr, os.ErrExist) {
					return false, mkdirErr
				}
				info, statErr = os.Lstat(current)
			} else {
				info, statErr = os.Lstat(current)
				if index == len(parts)-1 {
					createdFinal = true
				}
			}
		}
		if statErr != nil {
			return false, statErr
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return false, checkpointFailure(ErrInvalidCheckpoint, "checkpoint directory chain contains a symlink or non-directory", nil)
		}
		protected, protectErr := checkpointDirectoryProtected(current, info)
		if protectErr != nil {
			return false, protectErr
		}
		if !protected {
			return false, checkpointFailure(ErrInvalidCheckpoint, "checkpoint directory chain is not owner-only", nil)
		}
	}
	return createdFinal, nil
}

func inspectRetainedEvidence(workDir string, segmentCount int) error {
	entries, err := os.ReadDir(workDir)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		name := entry.Name()
		if name == reconciliationMarker || name == publicationMarker || name == finalPublicationEvidence || strings.HasPrefix(name, ".atomic-") {
			return checkpointFailure(ErrCheckpointReconciliation, "retained atomic checkpoint evidence requires reconciliation", nil)
		}
		if name == "state.json" {
			continue
		}
		index, valid := fragmentIndexFromName(name)
		if !valid || index >= segmentCount {
			return checkpointFailure(ErrCheckpointReconciliation, "checkpoint directory contains unknown prior work", nil)
		}
		info, infoErr := entry.Info()
		if infoErr != nil {
			return infoErr
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return checkpointFailure(ErrInvalidCheckpoint, "checkpoint fragment evidence is a symlink or non-regular file", nil)
		}
	}
	return nil
}

func fragmentIndexFromName(name string) (int, bool) {
	if len(name) != len("00000000.frag") || !strings.HasSuffix(name, ".frag") {
		return 0, false
	}
	index, err := strconv.Atoi(strings.TrimSuffix(name, ".frag"))
	return index, err == nil && index >= 0
}

func initialManifestState(expectation manifestExpectation) manifestState {
	if expectation.durable {
		return manifestState{Version: checkpointVersion, ResumeIdentity: expectation.resumeIdentity, PlanHash: expectation.planHash}
	}
	return manifestState{Hash: expectation.legacyHash}
}

func readManifestState(path string) (manifestState, error) {
	file, err := os.Open(path)
	if err != nil {
		return manifestState{}, err
	}
	defer file.Close()
	encoded, err := io.ReadAll(io.LimitReader(file, maxManifestBytes+1))
	if err != nil {
		return manifestState{}, err
	}
	if len(encoded) > maxManifestBytes {
		return manifestState{}, fmt.Errorf("fragment artifact manifest exceeds %d bytes", maxManifestBytes)
	}
	var state manifestState
	decoder := json.NewDecoder(strings.NewReader(string(encoded)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&state); err != nil {
		return manifestState{}, err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return manifestState{}, fmt.Errorf("trailing checkpoint data")
	}
	return state, nil
}

func validateManifestState(state manifestState, expectation manifestExpectation) error {
	if len(state.Artifacts) > maxFragmentSegments {
		return checkpointFailure(ErrInvalidCheckpoint, "checkpoint artifact count exceeds limit", nil)
	}
	for index, artifact := range state.Artifacts {
		if index < 0 || index >= expectation.segmentCount || artifact.Bytes <= 0 || len(artifact.SHA256) != sha256.Size*2 {
			return checkpointFailure(ErrInvalidCheckpoint, "checkpoint artifact metadata is invalid", nil)
		}
		if _, err := hex.DecodeString(artifact.SHA256); err != nil {
			return checkpointFailure(ErrInvalidCheckpoint, "checkpoint artifact digest is invalid", err)
		}
	}
	if expectation.durable {
		if state.Version != checkpointVersion || state.Hash != "" || state.ResumeIdentity == "" || state.PlanHash == "" {
			return checkpointFailure(ErrInvalidCheckpoint, "checkpoint ledger schema is invalid or missing durable identity", nil)
		}
		if state.ResumeIdentity != expectation.resumeIdentity {
			return checkpointFailure(ErrCheckpointReconciliation, "resume identity changed", nil)
		}
		if state.PlanHash != expectation.planHash {
			return checkpointFailure(ErrCheckpointReconciliation, "fragment plan changed for the resume identity", nil)
		}
		return nil
	}
	if state.Hash != expectation.legacyHash || state.Version != 0 || state.ResumeIdentity != "" || state.PlanHash != "" {
		return fmt.Errorf("legacy fragment plan changed")
	}
	return nil
}

func resetLegacyManifest(workDir string, expectation manifestExpectation, write func(string, os.FileMode, func(io.Writer) error) error) (*artifactManifest, error) {
	if err := os.RemoveAll(workDir); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		return nil, err
	}
	state := initialManifestState(expectation)
	path := filepath.Join(workDir, "state.json")
	if err := writeManifestState(path, state, write); err != nil {
		return nil, err
	}
	return &artifactManifest{path: path, workDir: workDir, state: state, write: write}, nil
}

func (manifest *artifactManifest) validateCommittedArtifacts(segmentCount int) error {
	for index := range manifest.state.Artifacts {
		if index < 0 || index >= segmentCount {
			return checkpointFailure(ErrInvalidCheckpoint, "checkpoint artifact index is outside the current plan", nil)
		}
		if valid, err := artifactMatches(manifest.state.Artifacts[index], fragmentPath(manifest.workDir, index)); err != nil || !valid {
			return checkpointFailure(ErrCheckpointReconciliation, "committed fragment evidence is missing or does not match its digest", err)
		}
	}
	return nil
}

func (manifest *artifactManifest) Reusable(index int, path string) (bool, error) {
	manifest.mu.Lock()
	known, present := manifest.state.Artifacts[index]
	durable := manifest.durable
	poisoned := manifest.poisoned
	callbackErr := manifest.callbackErr
	manifest.mu.Unlock()
	if callbackErr != nil {
		return false, callbackErr
	}
	if poisoned {
		return false, checkpointFailure(ErrCheckpointReconciliation, "checkpoint ledger authority is uncertain", nil)
	}
	if !present {
		return false, nil
	}
	valid, err := artifactMatches(known, path)
	if valid {
		return true, nil
	}
	if durable {
		return false, checkpointFailure(ErrCheckpointReconciliation, "committed fragment no longer matches its ledger digest", err)
	}
	return false, nil
}

// Valid retains the legacy test and package-internal compatibility surface.
func (manifest *artifactManifest) Valid(index int, path string) bool {
	valid, _ := manifest.Reusable(index, path)
	return valid
}

func artifactMatches(known artifact, path string) (bool, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return false, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return false, ErrUnsafeDestination
	}
	bytes, digest, err := digestFile(path)
	return err == nil && bytes == known.Bytes && digest == known.SHA256, err
}

func (manifest *artifactManifest) Record(index int, path string) error {
	bytes, digest, err := digestFile(path)
	if err != nil {
		return err
	}
	manifest.mu.Lock()
	defer manifest.mu.Unlock()
	if manifest.callbackErr != nil {
		return manifest.callbackErr
	}
	if manifest.poisoned {
		return checkpointFailure(ErrCheckpointReconciliation, "checkpoint ledger authority is uncertain", nil)
	}
	candidate := manifest.state
	candidate.Artifacts = cloneArtifacts(manifest.state.Artifacts)
	candidate.Artifacts[index] = artifact{Bytes: bytes, SHA256: digest}
	sequence := manifest.notifiedSequence
	var snapshot CommitSnapshot
	if manifest.durable {
		sequence = contiguousSequence(candidate)
		if sequence < manifest.notifiedSequence {
			return checkpointFailure(ErrCheckpointReconciliation, "checkpoint callback sequence regressed", nil)
		}
		if sequence > manifest.notifiedSequence {
			if sequence <= manifest.callerSequence {
				return checkpointFailure(ErrCheckpointReconciliation, "checkpoint callback would fall below caller authority", nil)
			}
			var snapshotErr error
			snapshot, snapshotErr = snapshotForPrefix(candidate, sequence)
			if snapshotErr != nil {
				return snapshotErr
			}
		}
	}
	if err := writeManifestState(manifest.path, candidate, manifest.write); err != nil {
		var commitErr atomicfile.CommitError
		if manifest.durable && errors.As(err, &commitErr) && (commitErr.Committed() || commitErr.Indeterminate()) {
			if commitErr.Committed() {
				manifest.state = candidate
			}
			manifest.poisoned = true
			markerErr := writeReconciliationMarker(manifest.workDir, "checkpoint ledger authority is uncertain")
			return checkpointFailure(ErrCheckpointReconciliation, "checkpoint ledger commit did not settle durably", errors.Join(err, markerErr))
		}
		return err
	}
	manifest.state = candidate
	if manifest.durable && sequence > manifest.notifiedSequence {
		if manifest.onCommit != nil {
			timeout := manifest.callbackTimeout
			if timeout <= 0 || timeout > maxCheckpointCallbackDuration {
				timeout = maxCheckpointCallbackDuration
			}
			localCtx, cancel := context.WithTimeout(context.Background(), timeout)
			err := manifest.onCommit(localCtx, snapshot)
			cancel()
			if err != nil {
				manifest.callbackErr = &checkpointCallbackError{cause: err}
				return manifest.callbackErr
			}
		}
		manifest.notifiedSequence = sequence
	}
	return nil
}

func (manifest *artifactManifest) configureCallback(checkpoint *Checkpoint, timeout time.Duration) error {
	manifest.mu.Lock()
	defer manifest.mu.Unlock()
	manifest.onCommit = checkpoint.OnCommit
	manifest.callbackTimeout = timeout
	manifest.notifiedSequence = contiguousSequence(manifest.state)
	if checkpoint.ResumeBoundary != nil {
		manifest.callerSequence = checkpoint.ResumeBoundary.Sequence
		manifest.notifiedSequence = checkpoint.ResumeBoundary.Sequence
		if contiguous := contiguousSequence(manifest.state); contiguous < manifest.callerSequence {
			return checkpointFailure(ErrCheckpointReconciliation, "local checkpoint fell below caller authority", nil)
		}
	}
	return nil
}

func cloneArtifacts(input map[int]artifact) map[int]artifact {
	output := make(map[int]artifact, len(input)+1)
	for index, artifact := range input {
		output[index] = artifact
	}
	return output
}

func writeManifestState(path string, state manifestState, write func(string, os.FileMode, func(io.Writer) error) error) error {
	encoded, err := json.Marshal(state)
	if err != nil {
		return err
	}
	if len(encoded) > maxManifestBytes {
		return fmt.Errorf("fragment artifact manifest exceeds %d bytes", maxManifestBytes)
	}
	return write(path, 0o600, func(writer io.Writer) error {
		_, err := writer.Write(encoded)
		return err
	})
}

func classifyInitialManifestWrite(workDir string, err error) error {
	var commitErr atomicfile.CommitError
	if errors.As(err, &commitErr) && (commitErr.Committed() || commitErr.Indeterminate()) {
		markerErr := writeReconciliationMarker(workDir, "initial checkpoint ledger authority is uncertain")
		return checkpointFailure(ErrCheckpointReconciliation, "initial checkpoint ledger commit did not settle durably", errors.Join(err, markerErr))
	}
	return err
}

func writeReconciliationMarker(workDir, reason string) error {
	return atomicfile.Write(filepath.Join(workDir, reconciliationMarker), 0o600, func(writer io.Writer) error {
		return json.NewEncoder(writer).Encode(struct {
			Version int    `json:"version"`
			Reason  string `json:"reason"`
		}{Version: checkpointVersion, Reason: reason})
	})
}

func digestFile(path string) (int64, string, error) {
	file, err := os.Open(path)
	if err != nil {
		return 0, "", err
	}
	defer file.Close()
	hasher := sha256.New()
	bytes, err := io.Copy(hasher, file)
	if err != nil {
		return 0, "", err
	}
	return bytes, hex.EncodeToString(hasher.Sum(nil)), nil
}
