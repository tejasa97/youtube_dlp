// Package fragment downloads and assembles ordered media fragments.
package fragment

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/tejasa97/youtube_dlp/internal/atomicfile"
	"github.com/tejasa97/youtube_dlp/internal/events"
	"github.com/tejasa97/youtube_dlp/internal/network"
)

var (
	ErrNoSegments               = errors.New("fragment plan has no segments")
	ErrSegmentTooLarge          = errors.New("fragment exceeds size limit")
	ErrInvalidEncryption        = errors.New("invalid AES-128 fragment encryption")
	ErrUnsafeDestination        = errors.New("fragment destination escapes output root")
	ErrTooManySegments          = errors.New("fragment plan exceeds segment limit")
	ErrTooManyAttempts          = errors.New("fragment retry attempts exceed limit")
	ErrTooMuchConcurrency       = errors.New("fragment concurrency exceeds limit")
	ErrInvalidSegmentRange      = errors.New("invalid fragment range")
	ErrInvalidCheckpoint        = errors.New("invalid fragment checkpoint")
	ErrCheckpointReconciliation = errors.New("fragment checkpoint reconciliation required")
	ErrCheckpointCleanup        = errors.New("committed fragment publication requires checkpoint cleanup")
	ErrCheckpointCallback       = errors.New("fragment checkpoint callback failed")
)

const (
	maxFragmentSegments    = 10000
	maxFragmentConcurrency = 128
	maxFragmentSize        = 512 << 20
	maxRetryDelay          = time.Minute
	maxResumeIdentityBytes = 512
)

// Checkpoint opts a finite fragment job into durable resume. Directory is an
// isolated, caller-owned directory for exactly one session component and must
// be disjoint from Destination in both directions. ResumeIdentity is an
// opaque, caller-owned, non-secret media identity. It must bind the exact
// stable output plan and format while signed URLs, request credentials, and
// encryption material refresh. The engine's credential-free structural hash
// is only a consistency check and never substitutes for this caller authority.
type Checkpoint struct {
	Directory      string
	ResumeIdentity string
	// RequireCoordinatorReset prevents the fragment package from deleting a
	// stale proof scope itself. Session coordinators set it and perform the
	// reset through their lease-bound, handle-relative workspace primitive.
	RequireCoordinatorReset bool
	// CoordinatorBoundary makes ResumeBoundary caller-authoritative even when
	// nil: nil is interpreted as the canonical zero prefix after the plan is
	// known. Session coordinators enable it so a ledger commit that crashed
	// before OnCommit can never become reusable local-ahead authority.
	CoordinatorBoundary bool
	// ResumeBoundary, when present, is external caller authority previously
	// accepted from an OnCommit snapshot. Local-ahead work is clamped to it
	// before any fragment is reused or requested.
	ResumeBoundary *ResumeBoundary
	// OnCommit runs synchronously after the ledger atomically commits a newly
	// advanced contiguous prefix. It receives a bounded, cancellation-
	// independent context and a credential-free value snapshot.
	OnCommit func(context.Context, CommitSnapshot) error
}

// CheckpointFailure reports checkpoint state that cannot be safely reused.
// Kind is ErrInvalidCheckpoint for malformed state and
// ErrCheckpointReconciliation when retained evidence or an identity mismatch
// requires an explicit caller decision.
type CheckpointFailure struct {
	Kind   error
	Detail string
	Cause  error
}

func (failure *CheckpointFailure) Error() string {
	if failure.Cause != nil {
		return fmt.Sprintf("%v: %s: %v", failure.Kind, failure.Detail, failure.Cause)
	}
	return fmt.Sprintf("%v: %s", failure.Kind, failure.Detail)
}

func (failure *CheckpointFailure) Unwrap() []error {
	if failure.Cause == nil {
		return []error{failure.Kind}
	}
	return []error{failure.Kind, failure.Cause}
}

func checkpointFailure(kind error, detail string, cause error) error {
	return &CheckpointFailure{Kind: kind, Detail: detail, Cause: cause}
}

// CheckpointPublicationError reports that final output publication committed
// durably but checkpoint cleanup failed. Callers must adopt the destination
// and reconcile the retained checkpoint evidence; retrying publication is not
// a valid recovery action.
type CheckpointPublicationError struct {
	Cause error
}

func (failure *CheckpointPublicationError) Error() string {
	return fmt.Sprintf("%v: %v", ErrCheckpointCleanup, failure.Cause)
}

func (failure *CheckpointPublicationError) Unwrap() []error {
	return []error{ErrCheckpointCleanup, failure.Cause}
}

func (*CheckpointPublicationError) Committed() bool     { return true }
func (*CheckpointPublicationError) Indeterminate() bool { return false }

type AES128 struct {
	Key []byte `json:"key"`
	IV  []byte `json:"iv"`
}

// Scale is the bounded, URL-free structural + remote-equivalence identity of a
// single finite fragment. It exists only for session-mode durable reuse: signed
// URLs, request headers, cookies, and AES material must never appear here.
// Key is a generated canonical SHA-256 of the URL-free structural identity.
// Kind is exactly one recognized proof
// contract ("provider-immutable", "content-identity", or the empty "none").
// Value is a canonical SHA-256 digest produced by extractor-owned metadata;
// arbitrary opaque callback strings are rejected so bearer-shaped material can
// never enter a ledger. Strong-validator proof is intentionally not accepted
// until an adapter checks fresh ETag/length/range evidence itself. Scope is a
// canonical SHA-256 supplied by the provider/protocol proof contract; it
// groups fragments for conservative whole-scope restart.
type Scale struct {
	Key   string `json:"key"`
	Kind  string `json:"kind"`
	Value string `json:"value"`
	Scope string `json:"scope"`
}

// RecognizedScaleKind reports whether kind is one of the supported remote
// byte-equivalence proof contracts. The empty string is "none".
func RecognizedScaleKind(kind string) bool {
	switch kind {
	case "provider-immutable", "content-identity", "":
		return true
	default:
		return false
	}
}

type Segment struct {
	URL         string  `json:"url"`
	RangeStart  int64   `json:"range_start,omitempty"`
	RangeLength int64   `json:"range_length,omitempty"`
	AES128      *AES128 `json:"aes128,omitempty"`
	// Scale carries the URL-free structural/proof identity for session-mode
	// plans. When nil, the segment contributes only its range and encryption
	// flag to the structural plan hash (legacy behavior).
	Scale *Scale `json:"scale,omitempty"`
}

type Job struct {
	Segments           []Segment
	Headers            http.Header
	OutputRoot         string
	Destination        string
	Concurrency        int
	Attempts           int
	MaxSegmentSize     int64
	MaxSegments        int
	PerHostConcurrency int
	RetryBaseDelay     time.Duration
	RetryMaxDelay      time.Duration
	Overwrite          bool
	Checkpoint         *Checkpoint
}

type Result struct {
	Path       string
	Bytes      int64
	Downloaded int
	Reused     int
}

type fragmentOutcome struct {
	index  int
	reused bool
	err    error
}

type Engine struct {
	transport         network.Doer
	writeAtomic       func(string, os.FileMode, func(io.Writer) error) error
	writeCheckpoint   func(string, os.FileMode, func(io.Writer) error) error
	replaceAtomic     func(string, string) error
	publishNoClobber  func(string, string) error
	removeAll         func(string) error
	cleanupOps        checkpointCleanupOps
	resetOps          checkpointResetOps
	checkpointTimeout time.Duration
}

func New(transport network.Doer) *Engine {
	return &Engine{
		transport: transport, writeAtomic: atomicfile.Write, writeCheckpoint: writeProtectedCheckpointArtifact, replaceAtomic: atomicfile.Replace,
		publishNoClobber: publishNoClobber,
		removeAll:        os.RemoveAll, cleanupOps: productionCheckpointCleanupOps,
		resetOps:          productionCheckpointResetOps,
		checkpointTimeout: maxCheckpointCallbackDuration,
	}
}

func (engine *Engine) checkpointWriter(durable bool) func(string, os.FileMode, func(io.Writer) error) error {
	if !durable {
		return engine.writeAtomic
	}
	if engine.writeCheckpoint != nil {
		return engine.writeCheckpoint
	}
	return engine.writeAtomic
}

type planState struct {
	Hash string `json:"hash"`
}

func (engine *Engine) Download(ctx context.Context, job Job, sink events.Sink) (Result, error) {
	if len(job.Segments) == 0 {
		return Result{}, ErrNoSegments
	}
	maxSegments := job.MaxSegments
	if maxSegments < 0 {
		return Result{}, ErrTooManySegments
	}
	if maxSegments <= 0 {
		maxSegments = maxFragmentSegments
	}
	if maxSegments > maxFragmentSegments {
		return Result{}, ErrTooManySegments
	}
	if len(job.Segments) > maxSegments {
		return Result{}, fmt.Errorf("%w: got %d, limit %d", ErrTooManySegments, len(job.Segments), maxSegments)
	}
	if err := validateFiniteFragmentPlan(job.Segments); err != nil {
		return Result{}, err
	}
	if sink == nil {
		sink = events.Nop()
	}
	if job.Checkpoint != nil {
		if err := validateResumeIdentity(job.Checkpoint.ResumeIdentity); err != nil {
			return Result{}, err
		}
		if err := validateCheckpointDirectory(job.OutputRoot, job.Destination, job.Checkpoint.Directory); err != nil {
			return Result{}, err
		}
	}
	if err := validateDestination(job.OutputRoot, job.Destination); err != nil {
		return Result{}, err
	}
	if info, err := os.Lstat(job.Destination); err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return Result{}, ErrUnsafeDestination
		}
		if !job.Overwrite {
			return Result{}, fmt.Errorf("destination exists: %s", job.Destination)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return Result{}, err
	}

	concurrency := job.Concurrency
	if concurrency <= 0 {
		concurrency = 4
	}
	if concurrency > maxFragmentConcurrency {
		return Result{}, ErrTooMuchConcurrency
	}
	if job.PerHostConcurrency < 0 || job.PerHostConcurrency > maxFragmentConcurrency {
		return Result{}, ErrTooMuchConcurrency
	}
	attempts := job.Attempts
	if attempts <= 0 {
		attempts = 3
	}
	if attempts > 100 {
		return Result{}, ErrTooManyAttempts
	}
	maxSize := job.MaxSegmentSize
	if maxSize < 0 {
		return Result{}, ErrSegmentTooLarge
	}
	if maxSize <= 0 {
		maxSize = 64 << 20
	}
	if maxSize > maxFragmentSize {
		return Result{}, ErrSegmentTooLarge
	}
	if job.RetryBaseDelay < 0 || job.RetryMaxDelay < 0 || job.RetryBaseDelay > maxRetryDelay || job.RetryMaxDelay > maxRetryDelay || (job.RetryBaseDelay > 0 && job.RetryMaxDelay > 0 && job.RetryBaseDelay > job.RetryMaxDelay) {
		return Result{}, ErrTooManyAttempts
	}
	workDir := job.Destination + ".fragments"
	if job.Checkpoint != nil {
		workDir = job.Checkpoint.Directory
	}
	if isSymlink(workDir) {
		if job.Checkpoint != nil {
			return Result{}, checkpointFailure(ErrInvalidCheckpoint, "fragment checkpoint workspace is a symlink", nil)
		}
		return Result{}, ErrUnsafeDestination
	}
	expectation, err := manifestExpectationFor(job)
	if err != nil {
		return Result{}, err
	}
	durable := job.Checkpoint != nil
	checkpointWrite := engine.checkpointWriter(durable)
	if job.Checkpoint != nil && (job.Checkpoint.ResumeBoundary != nil || job.Checkpoint.CoordinatorBoundary) {
		boundary := job.Checkpoint.ResumeBoundary
		if boundary == nil {
			zero, zeroErr := InitialResumeBoundary(job.Checkpoint.ResumeIdentity, job.Segments)
			if zeroErr != nil {
				return Result{}, zeroErr
			}
			boundary = &zero
		}
		if err := reconcileResumeBoundary(workDir, expectation, *boundary, checkpointWrite, engine.resetOps, job.Checkpoint.RequireCoordinatorReset); err != nil {
			return Result{}, err
		}
	}
	if err := os.MkdirAll(filepath.Dir(job.Destination), 0o755); err != nil {
		return Result{}, fmt.Errorf("create fragment output directory: %w", err)
	}
	manifest, err := openArtifactManifest(workDir, expectation, checkpointWrite)
	if err != nil {
		return Result{}, err
	}
	if manifest.equivalenceRestartRequired(job.Segments) {
		// A nonzero caller boundary is an external durable claim. Do not revoke
		// it behind the coordinator's back; return reconciliation so it can
		// durably reset its own component boundary before trying again.
		if job.Checkpoint != nil && (job.Checkpoint.RequireCoordinatorReset || (job.Checkpoint.ResumeBoundary != nil && job.Checkpoint.ResumeBoundary.Sequence != 0)) {
			return Result{}, checkpointFailure(ErrCheckpointReconciliation, "remote equivalence proof changed or is absent", nil)
		}
		if err := resetCheckpointWorkspace(workDir, expectation, engine.resetOps); err != nil {
			return Result{}, err
		}
		manifest, err = openArtifactManifest(workDir, expectation, checkpointWrite)
		if err != nil {
			return Result{}, err
		}
	}
	if job.Checkpoint != nil {
		if err := manifest.configureCallback(job.Checkpoint, engine.checkpointTimeout); err != nil {
			return Result{}, err
		}
	}
	hosts := newHostLimiter(job.PerHostConcurrency)

	workerCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	indices := make(chan int)
	outcomes := make(chan fragmentOutcome, len(job.Segments))
	var workers sync.WaitGroup
	var sinkMu sync.Mutex
	emit := func(event events.Event) error {
		sinkMu.Lock()
		defer sinkMu.Unlock()
		return sink.Emit(workerCtx, event)
	}
	if concurrency > len(job.Segments) {
		concurrency = len(job.Segments)
	}
	for worker := 0; worker < concurrency; worker++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for index := range indices {
				path := fragmentPath(workDir, index)
				var scale *Scale
				if index < len(job.Segments) {
					scale = job.Segments[index].Scale
				}
				reused, reuseErr := manifest.reusableScaled(index, path, scale)
				if reuseErr != nil {
					cancel()
					outcomes <- fragmentOutcome{index: index, err: reuseErr}
					continue
				}
				if reused {
					outcomes <- fragmentOutcome{index: index, reused: true}
					continue
				}
				eventURL := network.RedactRawURL(job.Segments[index].URL)
				err := emit(events.Event{Kind: events.KindFragmentStarting, URL: eventURL, Path: job.Destination, Fragment: index + 1, Fragments: len(job.Segments)})
				if err == nil {
					host, hostErr := segmentHost(job.Segments[index].URL)
					if hostErr != nil {
						err = hostErr
					} else if err = hosts.Acquire(workerCtx, host); err == nil {
						err = engine.fetchWithRetry(workerCtx, job, job.Segments[index], path, attempts, maxSize, checkpointWrite, func(nextAttempt int, retryErr error) error {
							return emit(events.Event{Kind: events.KindRetry, URL: eventURL, Path: job.Destination, Attempt: nextAttempt, Fragment: index + 1, Fragments: len(job.Segments), Message: fragmentRetryMessage(retryErr)})
						})
						hosts.Release(host)
					}
					if err == nil {
						err = manifest.RecordScaled(index, path, scale)
					}
				}
				if err == nil {
					err = emit(events.Event{Kind: events.KindFragmentCompleted, URL: eventURL, Path: job.Destination, Fragment: index + 1, Fragments: len(job.Segments)})
				}
				if err != nil {
					cancel()
				}
				outcomes <- fragmentOutcome{index: index, err: err}
			}
		}()
	}
	go func() {
		defer close(indices)
		for index := range job.Segments {
			select {
			case <-workerCtx.Done():
				return
			case indices <- index:
			}
		}
	}()
	go func() {
		workers.Wait()
		close(outcomes)
	}()

	result := Result{}
	completed := make([]fragmentOutcome, len(job.Segments))
	received := make([]bool, len(job.Segments))
	for outcome := range outcomes {
		completed[outcome.index] = outcome
		received[outcome.index] = true
		if outcome.reused {
			result.Reused++
		} else if outcome.err == nil {
			result.Downloaded++
		}
	}
	if err := deterministicOutcomeError(completed, received); err != nil {
		if errors.Is(err, ErrCheckpointCallback) && ctx.Err() != nil {
			return Result{}, errors.Join(err, ctx.Err())
		}
		return Result{}, err
	}
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}

	bytesWritten, err := engine.assemble(ctx, workDir, len(job.Segments), job.Destination, job.Overwrite, durable, checkpointWrite)
	if err != nil {
		return Result{}, err
	}
	result.Path = job.Destination
	result.Bytes = bytesWritten
	if job.Checkpoint != nil {
		if err := cleanupCommittedCheckpoint(workDir, engine.cleanupOps); err != nil {
			return result, &CheckpointPublicationError{Cause: err}
		}
	} else if err := engine.removeAll(workDir); err != nil {
		return Result{}, fmt.Errorf("remove fragment work directory: %w", err)
	}
	return result, nil
}

func deterministicOutcomeError(outcomes []fragmentOutcome, received []bool) error {
	for _, checkpointOnly := range []bool{true, false} {
		for index, outcome := range outcomes {
			if !received[index] || outcome.err == nil {
				continue
			}
			isCheckpoint := errors.Is(outcome.err, ErrInvalidCheckpoint) || errors.Is(outcome.err, ErrCheckpointReconciliation)
			isContext := errors.Is(outcome.err, context.Canceled) || errors.Is(outcome.err, context.DeadlineExceeded)
			if (checkpointOnly && isCheckpoint) || (!checkpointOnly && !isCheckpoint && !isContext) {
				return fmt.Errorf("fragment %d: %w", index+1, outcome.err)
			}
		}
	}
	for index, outcome := range outcomes {
		if received[index] && outcome.err != nil {
			return fmt.Errorf("fragment %d: %w", index+1, outcome.err)
		}
	}
	return nil
}

func (engine *Engine) fetchWithRetry(ctx context.Context, job Job, segment Segment, destination string, attempts int, maxSize int64, write func(string, os.FileMode, func(io.Writer) error) error, retryEvent func(int, error) error) error {
	var lastErr error
	for attempt := 1; attempt <= attempts; attempt++ {
		lastErr = engine.fetch(ctx, segment, destination, maxSize, job.Headers, write)
		if lastErr == nil {
			return nil
		}
		if job.Checkpoint != nil {
			var commitErr atomicfile.CommitError
			if errors.As(lastErr, &commitErr) && (commitErr.Committed() || commitErr.Indeterminate()) {
				markerErr := writeReconciliationMarker(filepath.Dir(destination), "fragment publication authority is uncertain", write)
				return checkpointFailure(ErrCheckpointReconciliation, "fragment publication did not settle durably", errors.Join(lastErr, markerErr))
			}
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if !fragmentRetryable(lastErr) {
			return lastErr
		}
		if attempt < attempts {
			if retryEvent != nil {
				if err := retryEvent(attempt+1, lastErr); err != nil {
					return err
				}
			}
			timer := time.NewTimer(fragmentRetryDelay(job, attempt))
			select {
			case <-ctx.Done():
				timer.Stop()
				return ctx.Err()
			case <-timer.C:
			}
		}
	}
	return lastErr
}

func fragmentRetryMessage(err error) string {
	switch {
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return "fragment request cancelled"
	case errors.Is(err, ErrSegmentTooLarge):
		return "fragment response exceeds size limit"
	case errors.Is(err, ErrUnsafeDestination):
		return "fragment output rejected"
	case fragmentRetryable(err):
		return "transient fragment transport failure"
	default:
		return "fragment download failed"
	}
}

type retryableFragmentError struct{ error }

func (err retryableFragmentError) Unwrap() error {
	return err.error
}

func fragmentRetryable(err error) bool {
	var target retryableFragmentError
	return errors.As(err, &target)
}

func fragmentRetryDelay(job Job, attempt int) time.Duration {
	base := job.RetryBaseDelay
	if base <= 0 {
		base = 20 * time.Millisecond
	}
	max := job.RetryMaxDelay
	if max <= 0 {
		max = time.Second
	}
	for index := 1; index < attempt; index++ {
		if base >= max || base > max/2 {
			return max
		}
		base *= 2
	}
	return base
}

type hostLimiter struct {
	perHost int
	mu      sync.Mutex
	sem     map[string]chan struct{}
}

func newHostLimiter(perHost int) *hostLimiter {
	return &hostLimiter{perHost: perHost, sem: make(map[string]chan struct{})}
}
func (limiter *hostLimiter) Acquire(ctx context.Context, host string) error {
	if limiter.perHost <= 0 {
		return nil
	}
	limiter.mu.Lock()
	semaphore := limiter.sem[host]
	if semaphore == nil {
		semaphore = make(chan struct{}, limiter.perHost)
		limiter.sem[host] = semaphore
	}
	limiter.mu.Unlock()
	select {
	case semaphore <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
func (limiter *hostLimiter) Release(host string) {
	if limiter.perHost <= 0 {
		return
	}
	limiter.mu.Lock()
	sem := limiter.sem[host]
	limiter.mu.Unlock()
	if sem != nil {
		<-sem
	}
}
func segmentHost(raw string) (string, error) {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Hostname() == "" {
		return "", fmt.Errorf("invalid fragment URL")
	}
	return strings.ToLower(parsed.Hostname()), nil
}

func (engine *Engine) fetch(ctx context.Context, segment Segment, destination string, maxSize int64, headers http.Header, write func(string, os.FileMode, func(io.Writer) error) error) error {
	if isSymlink(destination) || isSymlink(destination+".tmp") {
		return ErrUnsafeDestination
	}
	request, err := http.NewRequest(http.MethodGet, segment.URL, nil)
	if err != nil {
		return err
	}
	if segment.RangeLength > 0 {
		request.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", segment.RangeStart, segment.RangeStart+segment.RangeLength-1))
	}
	for key, values := range headers {
		for _, value := range values {
			request.Header.Add(key, value)
		}
	}
	response, err := engine.transport.Do(ctx, request)
	if err != nil {
		return retryableFragmentError{err}
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK && response.StatusCode != http.StatusPartialContent {
		err := fmt.Errorf("HTTP status %d", response.StatusCode)
		if network.RetryableStatus(response.StatusCode) {
			return retryableFragmentError{err}
		}
		return err
	}
	limited := io.LimitReader(response.Body, maxSize+1)
	body, err := io.ReadAll(limited)
	if err != nil {
		return retryableFragmentError{err}
	}
	if int64(len(body)) > maxSize {
		return ErrSegmentTooLarge
	}
	if segment.RangeLength > 0 && int64(len(body)) != segment.RangeLength {
		return fmt.Errorf("range length = %d, want %d", len(body), segment.RangeLength)
	}
	if segment.AES128 != nil {
		body, err = decryptAES128(body, segment.AES128)
		if err != nil {
			return err
		}
	}
	return write(destination, 0o644, func(writer io.Writer) error {
		_, err := writer.Write(body)
		return err
	})
}

func decryptAES128(input []byte, encryption *AES128) ([]byte, error) {
	if len(encryption.Key) != 16 || len(encryption.IV) != aes.BlockSize || len(input) == 0 || len(input)%aes.BlockSize != 0 {
		return nil, ErrInvalidEncryption
	}
	block, err := aes.NewCipher(encryption.Key)
	if err != nil {
		return nil, ErrInvalidEncryption
	}
	output := make([]byte, len(input))
	cipher.NewCBCDecrypter(block, encryption.IV).CryptBlocks(output, input)
	padding := int(output[len(output)-1])
	if padding == 0 || padding > aes.BlockSize || padding > len(output) {
		return nil, ErrInvalidEncryption
	}
	for _, value := range output[len(output)-padding:] {
		if int(value) != padding {
			return nil, ErrInvalidEncryption
		}
	}
	return output[:len(output)-padding], nil
}

func planHash(segments []Segment) (string, error) {
	encoded, err := json.Marshal(segments)
	if err != nil {
		return "", err
	}
	hash := sha256.Sum256(encoded)
	return hex.EncodeToString(hash[:]), nil
}

func checkpointPlanHash(segments []Segment) (string, error) {
	type checkpointSegment struct {
		RangeStart  int64  `json:"range_start"`
		RangeLength int64  `json:"range_length"`
		Encrypted   bool   `json:"encrypted"`
		Key         string `json:"key,omitempty"`
	}
	plan := make([]checkpointSegment, len(segments))
	for index, segment := range segments {
		var key string
		if segment.Scale != nil {
			key = segment.Scale.Key
		}
		plan[index] = checkpointSegment{
			RangeStart: segment.RangeStart, RangeLength: segment.RangeLength, Encrypted: segment.AES128 != nil, Key: key,
		}
	}
	encoded, err := json.Marshal(plan)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}

func fragmentPath(workDir string, index int) string {
	return filepath.Join(workDir, fmt.Sprintf("%08d.frag", index))
}

func (engine *Engine) assemble(ctx context.Context, workDir string, count int, destination string, overwrite, durable bool, write func(string, os.FileMode, func(io.Writer) error) error) (int64, error) {
	temporary := filepath.Join(workDir, "assembled.part")
	if info, err := os.Lstat(temporary); err == nil {
		if durable {
			return 0, checkpointFailure(ErrCheckpointReconciliation, "retained final publication candidate", nil)
		}
		if !info.Mode().IsRegular() {
			return 0, ErrUnsafeDestination
		}
		if err := os.Remove(temporary); err != nil {
			return 0, err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return 0, err
	}
	output, err := os.OpenFile(temporary, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return 0, err
	}
	if durable {
		if err := secureCheckpointFile(temporary); err != nil {
			_ = output.Close()
			_ = os.Remove(temporary)
			return 0, checkpointFailure(ErrCheckpointReconciliation, "assembled checkpoint part could not be secured", err)
		}
	}
	committed := false
	retainTemporary := false
	defer func() {
		_ = output.Close()
		if !committed && !retainTemporary {
			_ = os.Remove(temporary)
		}
	}()
	var total int64
	for index := 0; index < count; index++ {
		if err := ctx.Err(); err != nil {
			return 0, err
		}
		input, err := os.Open(fragmentPath(workDir, index))
		if err != nil {
			return 0, err
		}
		written, copyErr := copyFragment(ctx, output, input)
		closeErr := input.Close()
		total += written
		if copyErr != nil || closeErr != nil {
			return 0, errors.Join(copyErr, closeErr)
		}
	}
	if err := output.Close(); err != nil {
		return 0, err
	}
	if durable {
		if err := secureCheckpointFile(temporary); err != nil {
			retainTemporary = true
			return 0, checkpointFailure(ErrCheckpointReconciliation, "assembled checkpoint part could not be revalidated", &checkpointArtifactCommitError{cause: err, committed: true})
		}
	}
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if durable {
		bytes, digest, digestErr := digestFile(temporary)
		if digestErr != nil {
			return 0, digestErr
		}
		if bytes != total {
			return 0, fmt.Errorf("assembled fragment bytes = %d, want %d", bytes, total)
		}
		markerErr := write(filepath.Join(workDir, publicationMarker), 0o600, func(writer io.Writer) error {
			return json.NewEncoder(writer).Encode(artifact{Bytes: bytes, SHA256: digest})
		})
		if markerErr != nil {
			var commitErr atomicfile.CommitError
			if errors.As(markerErr, &commitErr) && (commitErr.Committed() || commitErr.Indeterminate()) {
				retainTemporary = true
				reconcileErr := writeReconciliationMarker(workDir, "final publication marker authority is uncertain", write)
				return 0, checkpointFailure(ErrCheckpointReconciliation, "final publication marker did not settle durably", errors.Join(markerErr, reconcileErr))
			}
			return 0, markerErr
		}
		// From this point through replacement, the durable publication marker
		// makes every interrupted or failed outcome explicitly reconcilable.
		retainTemporary = true
	}
	var publicationErr error
	if overwrite {
		publicationErr = engine.replaceAtomic(temporary, destination)
	} else {
		publicationErr = engine.publishNoClobber(temporary, destination)
	}
	if publicationErr != nil {
		if durable {
			var commitErr atomicfile.CommitError
			if errors.As(publicationErr, &commitErr) && (commitErr.Committed() || commitErr.Indeterminate()) {
				markerErr := writeReconciliationMarker(workDir, "final publication authority is uncertain", write)
				return 0, checkpointFailure(ErrCheckpointReconciliation, "final publication did not settle durably", errors.Join(publicationErr, markerErr))
			}
			return 0, checkpointFailure(ErrCheckpointReconciliation, "final publication failed before commit and retained recoverable evidence", publicationErr)
		}
		return 0, publicationErr
	}
	committed = true
	return total, nil
}

func copyFragment(ctx context.Context, destination io.Writer, source io.Reader) (int64, error) {
	buffer := make([]byte, 128<<10)
	var total int64
	for {
		if err := ctx.Err(); err != nil {
			return total, err
		}
		read, readErr := source.Read(buffer)
		if read > 0 {
			written, writeErr := destination.Write(buffer[:read])
			total += int64(written)
			if writeErr != nil {
				return total, writeErr
			}
			if written != read {
				return total, io.ErrShortWrite
			}
		}
		if errors.Is(readErr, io.EOF) {
			return total, nil
		}
		if readErr != nil {
			return total, readErr
		}
	}
}

func validateDestination(root, destination string) error {
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return err
	}
	destinationAbs, err := filepath.Abs(destination)
	if err != nil {
		return err
	}
	relative, err := filepath.Rel(rootAbs, destinationAbs)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return ErrUnsafeDestination
	}
	return nil
}

func validateCheckpointDirectory(root, destination, checkpointDirectory string) error {
	if checkpointDirectory == "" || strings.ContainsRune(checkpointDirectory, '\x00') {
		return checkpointFailure(ErrInvalidCheckpoint, "checkpoint directory is required", nil)
	}
	if err := validateDestination(root, checkpointDirectory); err != nil {
		return checkpointFailure(ErrInvalidCheckpoint, "checkpoint directory escapes output root", err)
	}
	directoryAbs, err := filepath.Abs(checkpointDirectory)
	if err != nil {
		return checkpointFailure(ErrInvalidCheckpoint, "resolve checkpoint directory", err)
	}
	destinationAbs, err := filepath.Abs(destination)
	if err != nil {
		return checkpointFailure(ErrInvalidCheckpoint, "resolve fragment destination", err)
	}
	if pathContains(directoryAbs, destinationAbs) || pathContains(destinationAbs, directoryAbs) {
		return checkpointFailure(ErrInvalidCheckpoint, "checkpoint directory and final destination must be disjoint", nil)
	}
	return nil
}

func pathContains(parent, child string) bool {
	relative, err := filepath.Rel(parent, child)
	return err == nil && (relative == "." || (relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))))
}

func isSymlink(path string) bool {
	info, err := os.Lstat(path)
	return err == nil && info.Mode()&os.ModeSymlink != 0
}
