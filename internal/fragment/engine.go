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
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/ytdlp-go/ytdlp/internal/events"
	"github.com/ytdlp-go/ytdlp/internal/network"
)

var (
	ErrNoSegments        = errors.New("fragment plan has no segments")
	ErrSegmentTooLarge   = errors.New("fragment exceeds size limit")
	ErrInvalidEncryption = errors.New("invalid AES-128 fragment encryption")
	ErrUnsafeDestination = errors.New("fragment destination escapes output root")
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
	Segments       []Segment
	OutputRoot     string
	Destination    string
	Concurrency    int
	Attempts       int
	MaxSegmentSize int64
	Overwrite      bool
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
	attempts := job.Attempts
	if attempts <= 0 {
		attempts = 3
	}
	maxSize := job.MaxSegmentSize
	if maxSize <= 0 {
		maxSize = 64 << 20
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
				if info, err := os.Stat(path); err == nil && info.Size() > 0 {
					outcomes <- outcome{index: index, reused: true}
					continue
				}
				eventURL := network.RedactRawURL(job.Segments[index].URL)
				err := emit(events.Event{Kind: events.KindFragmentStarting, URL: eventURL, Path: job.Destination, Fragment: index + 1, Fragments: len(job.Segments)})
				if err == nil {
					err = engine.fetchWithRetry(workerCtx, job.Segments[index], path, attempts, maxSize)
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

func (engine *Engine) fetchWithRetry(ctx context.Context, segment Segment, destination string, attempts int, maxSize int64) error {
	var lastErr error
	for attempt := 1; attempt <= attempts; attempt++ {
		lastErr = engine.fetch(ctx, segment, destination, maxSize)
		if lastErr == nil {
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if attempt < attempts {
			timer := time.NewTimer(time.Duration(attempt) * 20 * time.Millisecond)
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

func (engine *Engine) fetch(ctx context.Context, segment Segment, destination string, maxSize int64) error {
	request, err := http.NewRequest(http.MethodGet, segment.URL, nil)
	if err != nil {
		return err
	}
	if segment.RangeLength > 0 {
		request.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", segment.RangeStart, segment.RangeStart+segment.RangeLength-1))
	}
	response, err := engine.transport.Do(ctx, request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK && response.StatusCode != http.StatusPartialContent {
		return fmt.Errorf("HTTP status %d", response.StatusCode)
	}
	limited := io.LimitReader(response.Body, maxSize+1)
	body, err := io.ReadAll(limited)
	if err != nil {
		return err
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
	file, err := os.OpenFile(temporary, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
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
	encoded, err := os.ReadFile(statePath)
	if err == nil {
		var state planState
		if json.Unmarshal(encoded, &state) == nil && state.Hash == hash {
			return nil
		}
	}
	if err := os.RemoveAll(path); err != nil {
		return err
	}
	if err := os.MkdirAll(path, 0o755); err != nil {
		return err
	}
	encoded, _ = json.Marshal(planState{Hash: hash})
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
