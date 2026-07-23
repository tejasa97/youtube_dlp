// Package youtubelive implements bounded YouTube-specific media protocols.
package youtubelive

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/ytdlp-go/ytdlp/internal/events"
	"github.com/ytdlp-go/ytdlp/internal/fragment"
	"github.com/ytdlp-go/ytdlp/internal/network"
)

var (
	ErrInvalidBaseURL = errors.New("invalid YouTube post-live base URL")
	ErrInvalidConfig  = errors.New("invalid YouTube post-live downloader configuration")
	ErrProbeFailed    = errors.New("YouTube post-live sequence probe failed")
	ErrHeadSequence   = errors.New("invalid or missing X-Head-Seqnum")
	ErrNoSegments     = errors.New("YouTube post-live stream has no finite segments")
	ErrEventSink      = errors.New("YouTube post-live event sink failed")
	ErrDownloadFailed = errors.New("YouTube post-live segment download failed")
	ErrUnsafeOutput   = errors.New("unsafe YouTube post-live destination")
	ErrOutputExists   = errors.New("YouTube post-live destination already exists")
	ErrInvalidWindow  = errors.New("invalid YouTube post-live DVR window")
)

const (
	defaultMaxSegments = 10_000
	maxSegments        = 10_000
	maxConcurrency     = 128
	maxAttempts        = 100
	maxSegmentSize     = 512 << 20
	maxRetryDelay      = time.Minute
	maxBaseURLBytes    = 16 << 10
	maxAvailableAge    = 120 * time.Hour
	maxTargetDuration  = maxAvailableAge
)

// Transport is the HTTP boundary used for both the sequence probe and media
// fragments. Implementations must bind requests to the supplied context.
type Transport interface {
	Do(context.Context, *http.Request) (*http.Response, error)
}

// Config bounds all network and storage work performed by Downloader.
type Config struct {
	Headers             http.Header
	FragmentConcurrency int
	PerHostConcurrency  int
	MaxSegments         int
	MaxSegmentSize      int64
	Attempts            int
	RetryBaseDelay      time.Duration
	RetryMaxDelay       time.Duration
	TargetDuration      time.Duration
	LiveStartTimestamp  int64
	Now                 func() time.Time
}

type Downloader struct {
	transport Transport
	config    Config
}

// Segment is one finite post-live media fragment.
type Segment struct {
	Sequence int64
	URL      string
}

// Plan records the probed head and the finite sequence range to download.
type Plan struct {
	HeadSequence  int64
	BeginSequence int64
	Clamped       bool
	Segments      []Segment
}

// Result is the published media artifact.
type Result struct {
	Path     string
	Bytes    int64
	Segments int
}

func NewDownloader(transport Transport, config Config) *Downloader {
	config.Headers = config.Headers.Clone()
	if config.MaxSegments == 0 {
		config.MaxSegments = defaultMaxSegments
	}
	return &Downloader{transport: transport, config: config}
}

// BuildPlan reproduces yt-dlp's finite post-live tail behavior: X-Head-Seqnum
// is the newest sequence, and the newest two sequences are excluded because
// they may still be incomplete. Thus head N produces sq=0 through sq=N-2.
func BuildPlan(baseURL string, headSequence int64, limit int) (Plan, error) {
	return BuildPlanWithWindow(baseURL, headSequence, limit, 0, 0, time.Time{})
}

// BuildPlanWithWindow applies YouTube's 120-hour availability clamp when the
// original live start is older than that window. now.IsZero disables the age
// calculation, which is useful when the start timestamp is not known.
func BuildPlanWithWindow(baseURL string, headSequence int64, limit int, targetDuration time.Duration, liveStartTimestamp int64, now time.Time) (Plan, error) {
	parsed, err := parseBaseURL(baseURL)
	if err != nil {
		return Plan{}, err
	}
	if headSequence < 0 {
		return Plan{}, fmt.Errorf("%w: %d", ErrHeadSequence, headSequence)
	}
	end := headSequence - 1
	if end <= 0 {
		return Plan{}, ErrNoSegments
	}
	if limit <= 0 || limit > maxSegments {
		return Plan{}, fmt.Errorf("%w: max segments %d", ErrInvalidConfig, limit)
	}
	begin := int64(0)
	clamped := false
	if liveStartTimestamp != 0 && !now.IsZero() && now.Sub(time.Unix(liveStartTimestamp, 0)) > maxAvailableAge {
		if targetDuration <= 0 {
			return Plan{}, ErrInvalidWindow
		}
		available := int64(maxAvailableAge / targetDuration)
		if available < 0 {
			return Plan{}, ErrInvalidWindow
		}
		if begin = end - available; begin < 0 {
			begin = 0
		}
		clamped = begin > 0
	}
	count := end - begin
	if count <= 0 {
		return Plan{}, ErrNoSegments
	}
	if count > int64(limit) {
		return Plan{}, fmt.Errorf("%w: got %d, limit %d", fragment.ErrTooManySegments, count, limit)
	}
	segments := make([]Segment, int(count))
	for offset := range count {
		sequence := begin + offset
		raw, buildErr := sequenceURL(parsed, sequence)
		if buildErr != nil {
			return Plan{}, buildErr
		}
		segments[offset] = Segment{Sequence: sequence, URL: raw}
	}
	return Plan{HeadSequence: headSequence, BeginSequence: begin, Clamped: clamped, Segments: segments}, nil
}

// Probe obtains X-Head-Seqnum from the bare adaptive URL and returns a finite
// plan. The response body is never used and is closed promptly.
func (downloader *Downloader) Probe(ctx context.Context, baseURL string, sink events.Sink) (Plan, error) {
	if err := downloader.validate(); err != nil {
		return Plan{}, err
	}
	if _, err := parseBaseURL(baseURL); err != nil {
		return Plan{}, err
	}
	if sink == nil {
		sink = events.Nop()
	}
	sink = categorizeSink(sink)
	head, err := downloader.probeHead(ctx, baseURL, sink)
	if err != nil {
		return Plan{}, err
	}
	now := time.Now()
	if downloader.config.Now != nil {
		now = downloader.config.Now()
	}
	plan, err := BuildPlanWithWindow(
		baseURL, head, downloader.config.MaxSegments, downloader.config.TargetDuration,
		downloader.config.LiveStartTimestamp, now)
	if err != nil {
		return Plan{}, err
	}
	if plan.Clamped {
		if err := sink.Emit(ctx, events.Event{
			Kind: events.KindMetadataWarning, URL: network.RedactRawURL(baseURL),
			Message:   "starting from YouTube's last 120 hours of available live media",
			Fragments: len(plan.Segments),
		}); err != nil {
			return Plan{}, err
		}
	}
	return plan, nil
}

// Download probes, downloads, and atomically publishes a finite post-live
// adaptive stream. Failed work removes temporary fragment state while leaving
// any pre-existing destination untouched.
func (downloader *Downloader) Download(ctx context.Context, baseURL, outputRoot, destination string, overwrite bool, sink events.Sink) (result Result, err error) {
	if err := downloader.validate(); err != nil {
		return Result{}, err
	}
	if _, err := parseBaseURL(baseURL); err != nil {
		return Result{}, err
	}
	if err := validateDestination(outputRoot, destination); err != nil {
		return Result{}, err
	}
	if _, statErr := os.Stat(destination); statErr == nil && !overwrite {
		return Result{}, ErrOutputExists
	} else if statErr != nil && !errors.Is(statErr, os.ErrNotExist) {
		return Result{}, fmt.Errorf("%w: inspect destination", ErrUnsafeOutput)
	}
	if sink == nil {
		sink = events.Nop()
	}
	sink = categorizeSink(sink)
	eventURL := network.RedactRawURL(baseURL)
	if err := sink.Emit(ctx, events.Event{Kind: events.KindStarting, URL: eventURL, Path: destination}); err != nil {
		return Result{}, err
	}
	plan, err := downloader.Probe(ctx, baseURL, sink)
	if err != nil {
		downloader.emitCancellation(ctx, sink, eventURL, destination, err)
		return Result{}, err
	}
	segments := make([]fragment.Segment, len(plan.Segments))
	for index, segment := range plan.Segments {
		segments[index] = fragment.Segment{URL: segment.URL}
	}

	published := false
	defer func() {
		if !published {
			_ = os.Remove(destination + ".part")
			_ = os.RemoveAll(destination + ".fragments")
		}
	}()
	downloaded, err := fragment.New(downloader.transport).Download(ctx, fragment.Job{
		Segments: segments, Headers: downloader.config.Headers, OutputRoot: outputRoot, Destination: destination,
		Concurrency: downloader.config.FragmentConcurrency, PerHostConcurrency: downloader.config.PerHostConcurrency,
		MaxSegments: downloader.config.MaxSegments, MaxSegmentSize: downloader.config.MaxSegmentSize,
		Attempts: downloader.config.Attempts, RetryBaseDelay: downloader.config.RetryBaseDelay,
		RetryMaxDelay: downloader.config.RetryMaxDelay, Overwrite: overwrite,
	}, sink)
	if err != nil {
		downloader.emitCancellation(ctx, sink, eventURL, destination, err)
		return Result{}, safeDownloadError(err)
	}
	published = true
	result = Result{Path: downloaded.Path, Bytes: downloaded.Bytes, Segments: len(plan.Segments)}
	// Publication is the terminal state. A completion observer cannot veto an
	// artifact after it has atomically replaced the destination.
	_ = sink.Emit(ctx, events.Event{Kind: events.KindCompleted, URL: eventURL, Path: result.Path, Bytes: result.Bytes, Fragments: result.Segments})
	return result, nil
}

func (downloader *Downloader) validate() error {
	if downloader == nil || downloader.transport == nil {
		return fmt.Errorf("%w: nil transport", ErrInvalidConfig)
	}
	config := downloader.config
	if config.MaxSegments <= 0 || config.MaxSegments > maxSegments ||
		config.FragmentConcurrency < 0 || config.FragmentConcurrency > maxConcurrency ||
		config.PerHostConcurrency < 0 || config.PerHostConcurrency > maxConcurrency ||
		config.MaxSegmentSize < 0 || config.MaxSegmentSize > maxSegmentSize ||
		config.Attempts < 0 || config.Attempts > maxAttempts ||
		config.RetryBaseDelay < 0 || config.RetryMaxDelay < 0 ||
		config.TargetDuration < 0 || config.TargetDuration > maxTargetDuration ||
		config.RetryBaseDelay > maxRetryDelay || config.RetryMaxDelay > maxRetryDelay ||
		(config.RetryBaseDelay > 0 && config.RetryMaxDelay > 0 && config.RetryBaseDelay > config.RetryMaxDelay) {
		return ErrInvalidConfig
	}
	return nil
}

func (downloader *Downloader) probeHead(ctx context.Context, baseURL string, sink events.Sink) (int64, error) {
	attempts := downloader.config.Attempts
	if attempts <= 0 {
		attempts = 3
	}
	var lastErr error
	for attempt := 1; attempt <= attempts; attempt++ {
		head, retryable, err := downloader.probeOnce(ctx, baseURL)
		if err == nil {
			return head, nil
		}
		lastErr = err
		if ctx.Err() != nil {
			return 0, ctx.Err()
		}
		if !retryable || attempt == attempts {
			break
		}
		if err := sink.Emit(ctx, events.Event{
			Kind: events.KindRetry, URL: network.RedactRawURL(baseURL),
			Attempt: attempt + 1, Message: "transient post-live sequence probe failure",
		}); err != nil {
			return 0, err
		}
		timer := time.NewTimer(retryDelay(downloader.config, attempt))
		select {
		case <-ctx.Done():
			timer.Stop()
			return 0, ctx.Err()
		case <-timer.C:
		}
	}
	if errors.Is(lastErr, ErrHeadSequence) {
		return 0, errors.Join(ErrProbeFailed, ErrHeadSequence)
	}
	return 0, ErrProbeFailed
}

func (downloader *Downloader) probeOnce(ctx context.Context, baseURL string) (int64, bool, error) {
	request, err := http.NewRequest(http.MethodGet, baseURL, nil)
	if err != nil {
		return 0, false, fmt.Errorf("%w: %v", ErrInvalidBaseURL, err)
	}
	for key, values := range downloader.config.Headers {
		for _, value := range values {
			request.Header.Add(key, value)
		}
	}
	response, err := downloader.transport.Do(ctx, request)
	if err != nil {
		return 0, true, err
	}
	if response == nil || response.Body == nil {
		return 0, true, errors.New("transport returned an empty response")
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return 0, network.RetryableStatus(response.StatusCode), fmt.Errorf("HTTP status %d", response.StatusCode)
	}
	head, err := parseHeadSequence(response.Header.Get("X-Head-Seqnum"))
	if err != nil {
		return 0, false, err
	}
	return head, false, nil
}

func validateDestination(root, destination string) error {
	if root == "" || destination == "" || strings.ContainsRune(root, '\x00') || strings.ContainsRune(destination, '\x00') {
		return ErrUnsafeOutput
	}
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrUnsafeOutput, err)
	}
	destinationAbs, err := filepath.Abs(destination)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrUnsafeOutput, err)
	}
	relative, err := filepath.Rel(rootAbs, destinationAbs)
	if err != nil || relative == "." || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return ErrUnsafeOutput
	}
	return nil
}

func parseHeadSequence(value string) (int64, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0, ErrHeadSequence
	}
	head, err := strconv.ParseInt(value, 10, 64)
	if err != nil || head < 0 {
		return 0, ErrHeadSequence
	}
	return head, nil
}

func safeDownloadError(err error) error {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	for _, sentinel := range []error{
		fragment.ErrNoSegments,
		fragment.ErrSegmentTooLarge,
		fragment.ErrInvalidEncryption,
		fragment.ErrUnsafeDestination,
		fragment.ErrTooManySegments,
		fragment.ErrTooManyAttempts,
		fragment.ErrTooMuchConcurrency,
		ErrEventSink,
	} {
		if errors.Is(err, sentinel) {
			return errors.Join(ErrDownloadFailed, sentinel)
		}
	}
	// Transport errors may embed the signed media URL. Preserve the stable
	// category without reflecting the underlying diagnostic.
	return ErrDownloadFailed
}

func parseBaseURL(raw string) (*url.URL, error) {
	if raw == "" || len(raw) > maxBaseURLBytes || strings.ContainsRune(raw, '\x00') {
		return nil, ErrInvalidBaseURL
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Opaque != "" || (parsed.Scheme != "http" && parsed.Scheme != "https") ||
		parsed.Hostname() == "" || parsed.User != nil || parsed.Fragment != "" {
		return nil, ErrInvalidBaseURL
	}
	decodedPath, pathErr := url.PathUnescape(parsed.EscapedPath())
	decodedQuery, queryErr := url.QueryUnescape(parsed.RawQuery)
	if pathErr != nil || queryErr != nil || strings.ContainsRune(decodedPath, '\x00') || strings.ContainsRune(decodedQuery, '\x00') {
		return nil, ErrInvalidBaseURL
	}
	return parsed, nil
}

func sequenceURL(base *url.URL, sequence int64) (string, error) {
	if base == nil || sequence < 0 {
		return "", ErrInvalidBaseURL
	}
	cloned := *base
	parts := strings.Split(cloned.RawQuery, "&")
	filtered := make([]string, 0, len(parts)+1)
	for _, part := range parts {
		if part == "" {
			if cloned.RawQuery != "" {
				filtered = append(filtered, part)
			}
			continue
		}
		key := part
		if index := strings.IndexByte(key, '='); index >= 0 {
			key = key[:index]
		}
		decoded, err := url.QueryUnescape(key)
		if err != nil {
			return "", ErrInvalidBaseURL
		}
		if decoded != "sq" {
			filtered = append(filtered, part)
		}
	}
	filtered = append(filtered, "sq="+strconv.FormatInt(sequence, 10))
	cloned.RawQuery = strings.Join(filtered, "&")
	return cloned.String(), nil
}

func retryDelay(config Config, attempt int) time.Duration {
	delay := config.RetryBaseDelay
	if delay <= 0 {
		delay = 20 * time.Millisecond
	}
	maximum := config.RetryMaxDelay
	if maximum <= 0 {
		maximum = time.Second
	}
	for index := 1; index < attempt; index++ {
		if delay >= maximum || delay > maximum/2 {
			return maximum
		}
		delay *= 2
	}
	return delay
}

func (downloader *Downloader) emitCancellation(ctx context.Context, sink events.Sink, rawURL, destination string, cause error) {
	if errors.Is(cause, context.Canceled) || errors.Is(cause, context.DeadlineExceeded) || ctx.Err() != nil {
		_ = sink.Emit(context.WithoutCancel(ctx), events.Event{
			Kind: events.KindCancelled, URL: rawURL, Path: destination, Message: "post-live download cancelled",
		})
	}
}

type categorizedSink struct{ events.Sink }

func categorizeSink(sink events.Sink) events.Sink {
	if _, ok := sink.(categorizedSink); ok {
		return sink
	}
	return categorizedSink{Sink: sink}
}

func (sink categorizedSink) Emit(ctx context.Context, event events.Event) error {
	if event.URL != "" {
		event.URL = redactMediaEventURL(event.URL)
	}
	if err := sink.Sink.Emit(ctx, event); err != nil {
		return errors.Join(ErrEventSink, err)
	}
	return nil
}

func redactMediaEventURL(raw string) string {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return "[redacted YouTube media URL]"
	}
	parsed.RawQuery = ""
	parsed.ForceQuery = false
	parsed.Fragment = ""
	parsed.User = nil
	return parsed.String()
}
