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

	"github.com/ytdlp-go/ytdlp/internal/events"
	"github.com/ytdlp-go/ytdlp/internal/network"
)

var (
	ErrNoSegments         = errors.New("fragment plan has no segments")
	ErrSegmentTooLarge    = errors.New("fragment exceeds size limit")
	ErrInvalidEncryption  = errors.New("invalid AES-128 fragment encryption")
	ErrUnsafeDestination  = errors.New("fragment destination escapes output root")
	ErrTooManySegments    = errors.New("fragment plan exceeds segment limit")
	ErrTooManyAttempts    = errors.New("fragment retry attempts exceed limit")
	ErrTooMuchConcurrency = errors.New("fragment concurrency exceeds limit")
)

const (
	maxFragmentSegments    = 100000
	maxFragmentConcurrency = 128
	maxFragmentSize        = 512 << 20
	maxRetryDelay          = time.Minute
)

type AES128 struct {
	Key []byte `json:"key"`
	IV  []byte `json:"iv"`
}

type Segment struct {
	URL         string  `json:"url"`
	RangeStart  int64   `json:"range_start,omitempty"`
	RangeLength int64   `json:"range_length,omitempty"`
	AES128      *AES128 `json:"aes128,omitempty"`
}

type Job struct {
	Segments           []Segment
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
}

type Result struct {
	Path       string
	Bytes      int64
	Downloaded int
	Reused     int
}

type Engine struct {
	transport network.Doer
}

func New(transport network.Doer) *Engine { return &Engine{transport: transport} }

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
	if sink == nil {
		sink = events.Nop()
	}
	if err := validateDestination(job.OutputRoot, job.Destination); err != nil {
		return Result{}, err
	}
	if err := os.MkdirAll(filepath.Dir(job.Destination), 0o755); err != nil {
		return Result{}, fmt.Errorf("create fragment output directory: %w", err)
	}
	if _, err := os.Stat(job.Destination); err == nil && !job.Overwrite {
		return Result{}, fmt.Errorf("destination exists: %s", job.Destination)
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
	if isSymlink(workDir) {
		return Result{}, ErrUnsafeDestination
	}
	hash, err := planHash(job.Segments)
	if err != nil {
		return Result{}, err
	}
	if err := prepareWorkDir(workDir, hash); err != nil {
		return Result{}, err
	}
	manifest, err := openArtifactManifest(workDir, hash)
	if err != nil {
		return Result{}, err
	}
	hosts := newHostLimiter(job.PerHostConcurrency)

	workerCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	indices := make(chan int)
	type outcome struct {
		index  int
		reused bool
		err    error
	}
	outcomes := make(chan outcome, len(job.Segments))
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
				if info, err := os.Stat(path); err == nil && info.Size() > 0 && manifest.Valid(index, path) {
					outcomes <- outcome{index: index, reused: true}
					continue
				}
				eventURL := network.RedactRawURL(job.Segments[index].URL)
				err := emit(events.Event{Kind: events.KindFragmentStarting, URL: eventURL, Path: job.Destination, Fragment: index + 1, Fragments: len(job.Segments)})
				if err == nil {
					host, hostErr := segmentHost(job.Segments[index].URL)
					if hostErr != nil {
						err = hostErr
					} else if err = hosts.Acquire(workerCtx, host); err == nil {
						err = engine.fetchWithRetry(workerCtx, job, job.Segments[index], path, attempts, maxSize)
						hosts.Release(host)
					}
					if err == nil {
						err = manifest.Record(index, path)
					}
				}
				if err == nil {
					err = emit(events.Event{Kind: events.KindFragmentCompleted, URL: eventURL, Path: job.Destination, Fragment: index + 1, Fragments: len(job.Segments)})
				}
				if err != nil {
					cancel()
				}
				outcomes <- outcome{index: index, err: err}
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
	var firstErr error
	for outcome := range outcomes {
		if outcome.reused {
			result.Reused++
		} else if outcome.err == nil {
			result.Downloaded++
		}
		if outcome.err != nil && firstErr == nil {
			firstErr = fmt.Errorf("fragment %d: %w", outcome.index+1, outcome.err)
		}
	}
	if firstErr != nil {
		return Result{}, firstErr
	}
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}

	bytesWritten, err := assemble(workDir, len(job.Segments), job.Destination, job.Overwrite)
	if err != nil {
		return Result{}, err
	}
	if err := os.RemoveAll(workDir); err != nil {
		return Result{}, fmt.Errorf("remove fragment work directory: %w", err)
	}
	result.Path = job.Destination
	result.Bytes = bytesWritten
	return result, nil
}

func (engine *Engine) fetchWithRetry(ctx context.Context, job Job, segment Segment, destination string, attempts int, maxSize int64) error {
	var lastErr error
	for attempt := 1; attempt <= attempts; attempt++ {
		lastErr = engine.fetch(ctx, segment, destination, maxSize)
		if lastErr == nil {
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if !fragmentRetryable(lastErr) {
			return lastErr
		}
		if attempt < attempts {
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

type retryableFragmentError struct{ error }

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

func (engine *Engine) fetch(ctx context.Context, segment Segment, destination string, maxSize int64) error {
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
	temporary := destination + ".tmp"
	if info, statErr := os.Lstat(temporary); statErr == nil {
		if !info.Mode().IsRegular() {
			return ErrUnsafeDestination
		}
		if err := os.Remove(temporary); err != nil {
			return err
		}
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return statErr
	}
	file, err := os.OpenFile(temporary, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	if _, err := file.Write(body); err != nil {
		file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return os.Rename(temporary, destination)
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

func prepareWorkDir(path, hash string) error {
	statePath := filepath.Join(path, "state.json")
	info, statErr := os.Lstat(statePath)
	if statErr == nil && !info.Mode().IsRegular() {
		return ErrUnsafeDestination
	}
	if statErr != nil && !errors.Is(statErr, os.ErrNotExist) {
		return statErr
	}
	file, err := os.Open(statePath)
	if err == nil {
		defer file.Close()
		encoded, readErr := io.ReadAll(io.LimitReader(file, maxManifestBytes+1))
		if readErr != nil {
			return readErr
		}
		if len(encoded) > maxManifestBytes {
			return fmt.Errorf("fragment state exceeds %d bytes", maxManifestBytes)
		}
		var state planState
		decoder := json.NewDecoder(strings.NewReader(string(encoded)))
		if decoder.Decode(&state) == nil && decoder.Decode(&struct{}{}) == io.EOF && state.Hash == hash {
			return nil
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.RemoveAll(path); err != nil {
		return err
	}
	if err := os.MkdirAll(path, 0o755); err != nil {
		return err
	}
	encoded, _ := json.Marshal(planState{Hash: hash})
	return os.WriteFile(statePath, encoded, 0o600)
}

func fragmentPath(workDir string, index int) string {
	return filepath.Join(workDir, fmt.Sprintf("%08d.frag", index))
}

func assemble(workDir string, count int, destination string, overwrite bool) (int64, error) {
	temporary := destination + ".part"
	output, err := os.OpenFile(temporary, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		return 0, err
	}
	var total int64
	for index := 0; index < count; index++ {
		input, err := os.Open(fragmentPath(workDir, index))
		if err != nil {
			output.Close()
			return 0, err
		}
		written, copyErr := io.Copy(output, input)
		closeErr := input.Close()
		total += written
		if copyErr != nil || closeErr != nil {
			output.Close()
			return 0, errors.Join(copyErr, closeErr)
		}
	}
	if err := output.Sync(); err != nil {
		output.Close()
		return 0, err
	}
	if err := output.Close(); err != nil {
		return 0, err
	}
	if err := os.Rename(temporary, destination); err == nil {
		return total, nil
	} else if !overwrite {
		return 0, err
	}
	if err := os.Remove(destination); err != nil && !errors.Is(err, os.ErrNotExist) {
		return 0, err
	}
	return total, os.Rename(temporary, destination)
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

func isSymlink(path string) bool {
	info, err := os.Lstat(path)
	return err == nil && info.Mode()&os.ModeSymlink != 0
}
