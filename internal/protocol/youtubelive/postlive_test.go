package youtubelive

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ytdlp-go/ytdlp/internal/events"
	"github.com/ytdlp-go/ytdlp/internal/fragment"
)

type transportFunc func(context.Context, *http.Request) (*http.Response, error)

func (function transportFunc) Do(ctx context.Context, request *http.Request) (*http.Response, error) {
	return function(ctx, request)
}

func response(status int, header http.Header, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Header:     header,
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

func TestDownloadFinitePostLiveTailAndPreservesSignedQuery(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	var requested []string
	transport := transportFunc(func(_ context.Context, request *http.Request) (*http.Response, error) {
		mu.Lock()
		requested = append(requested, request.URL.String())
		mu.Unlock()
		if request.URL.Query().Get("sq") == "" {
			if request.Header.Get("X-Test") != "fixture" {
				t.Errorf("probe header = %q", request.Header.Get("X-Test"))
			}
			return response(http.StatusOK, http.Header{"X-Head-Seqnum": []string{"4"}}, ""), nil
		}
		sequence, _ := strconv.Atoi(request.URL.Query().Get("sq"))
		return response(http.StatusOK, nil, strconv.Itoa(sequence)), nil
	})
	var eventMu sync.Mutex
	var kinds []events.Kind
	sink := events.SinkFunc(func(_ context.Context, event events.Event) error {
		eventMu.Lock()
		defer eventMu.Unlock()
		kinds = append(kinds, event.Kind)
		if event.URL != "" && event.URL != "https://media.example/videoplayback" {
			t.Errorf("event leaked signed query: %q", event.URL)
		}
		return nil
	})
	root := t.TempDir()
	destination := filepath.Join(root, "media.bin")
	result, err := NewDownloader(transport, Config{
		Headers: http.Header{"X-Test": []string{"fixture"}}, FragmentConcurrency: 3,
	}).Download(context.Background(), "https://media.example/videoplayback?pot=secret-pot&lsig=secret-lsig&n=secret-n&expire=secret-expire&foo=a%20b", root, destination, false, sink)
	if err != nil {
		t.Fatal(err)
	}
	if result.Path != destination || result.Bytes != 3 || result.Segments != 3 {
		t.Fatalf("result = %#v", result)
	}
	body, err := os.ReadFile(destination)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "012" {
		t.Fatalf("body = %q", body)
	}
	mu.Lock()
	gotRequests := append([]string(nil), requested...)
	mu.Unlock()
	if len(gotRequests) != 4 {
		t.Fatalf("requests = %v", gotRequests)
	}
	for _, raw := range gotRequests[1:] {
		if !strings.Contains(raw, "pot=secret-pot&lsig=secret-lsig&n=secret-n&expire=secret-expire&foo=a%20b") {
			t.Errorf("signed query spelling changed: %s", raw)
		}
	}
	eventMu.Lock()
	gotKinds := append([]events.Kind(nil), kinds...)
	eventMu.Unlock()
	if len(gotKinds) < 8 || gotKinds[0] != events.KindStarting || gotKinds[len(gotKinds)-1] != events.KindCompleted {
		t.Fatalf("event kinds = %v", gotKinds)
	}
}

func TestBuildPlanReplacesSQAndExcludesFinalTwoSequences(t *testing.T) {
	t.Parallel()
	plan, err := BuildPlan("https://media.example/v?foo=1&s%71=old&sq=older&bar=2", 5, 10)
	if err != nil {
		t.Fatal(err)
	}
	if plan.HeadSequence != 5 || len(plan.Segments) != 4 {
		t.Fatalf("plan = %#v", plan)
	}
	for index, segment := range plan.Segments {
		parsed, err := url.Parse(segment.URL)
		if err != nil {
			t.Fatal(err)
		}
		if segment.Sequence != int64(index) || parsed.Query().Get("sq") != strconv.Itoa(index) {
			t.Errorf("segment %d = %#v", index, segment)
		}
		if strings.Count(parsed.RawQuery, "sq=") != 1 || strings.Contains(parsed.RawQuery, "s%71=") {
			t.Errorf("sq was not replaced: %q", parsed.RawQuery)
		}
		if !strings.Contains(parsed.RawQuery, "foo=1") || !strings.Contains(parsed.RawQuery, "bar=2") {
			t.Errorf("query lost: %q", parsed.RawQuery)
		}
	}
	if _, err := BuildPlan("https://media.example/v", 1, 10); !errors.Is(err, ErrNoSegments) {
		t.Fatalf("head 1 error = %v", err)
	}
	if _, err := BuildPlan("https://media.example/v", 12, 10); !errors.Is(err, fragment.ErrTooManySegments) {
		t.Fatalf("limit error = %v", err)
	}
}

func TestBuildPlanAppliesPinned120HourClampBeforeLimit(t *testing.T) {
	t.Parallel()
	now := time.Unix(2_000_000_000, 0)
	start := now.Add(-120*time.Hour - time.Second).Unix()
	plan, err := BuildPlanWithWindow(
		"https://media.example/v?sig=fixed", 10, 5, 24*time.Hour, start, now)
	if err != nil {
		t.Fatal(err)
	}
	if !plan.Clamped || plan.BeginSequence != 4 || len(plan.Segments) != 5 {
		t.Fatalf("plan = %#v", plan)
	}
	for index, segment := range plan.Segments {
		if segment.Sequence != int64(index+4) {
			t.Errorf("segment %d sequence = %d", index, segment.Sequence)
		}
	}
	if _, err := BuildPlanWithWindow(
		"https://media.example/v", 10, 4, 24*time.Hour, start, now); !errors.Is(err, fragment.ErrTooManySegments) {
		t.Fatalf("remaining-count limit error = %v", err)
	}
	if _, err := BuildPlanWithWindow(
		"https://media.example/v", 10, 10, 0, start, now); !errors.Is(err, ErrInvalidWindow) {
		t.Fatalf("missing target duration error = %v", err)
	}
}

func TestProbeEmitsClampWarning(t *testing.T) {
	t.Parallel()
	now := time.Unix(2_000_000_000, 0)
	var warnings atomic.Int32
	downloader := NewDownloader(transportFunc(func(context.Context, *http.Request) (*http.Response, error) {
		return response(http.StatusOK, http.Header{"X-Head-Seqnum": []string{"10"}}, ""), nil
	}), Config{
		MaxSegments: 5, TargetDuration: 24 * time.Hour,
		LiveStartTimestamp: now.Add(-121 * time.Hour).Unix(),
		Now:                func() time.Time { return now },
	})
	plan, err := downloader.Probe(context.Background(), "https://media.example/v", events.SinkFunc(func(_ context.Context, event events.Event) error {
		if event.Kind == events.KindMetadataWarning {
			warnings.Add(1)
		}
		return nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	if !plan.Clamped || warnings.Load() != 1 {
		t.Fatalf("plan=%#v warnings=%d", plan, warnings.Load())
	}
}

func TestRejectsUnsafeBaseURLsBeforeNetwork(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	downloader := NewDownloader(transportFunc(func(context.Context, *http.Request) (*http.Response, error) {
		calls.Add(1)
		return nil, errors.New("unexpected")
	}), Config{})
	cases := []string{
		"", "ftp://media.example/v",
		"https://user@media.example/v", "https://media.example/v#fragment",
		"https://media.example/%00", "https://media.example/v?q=%00",
		"https:///missing-host", "https://media.example/v\x00",
	}
	for _, raw := range cases {
		if _, err := downloader.Probe(context.Background(), raw, nil); !errors.Is(err, ErrInvalidBaseURL) {
			t.Errorf("Probe(%q) error = %v", raw, err)
		}
	}
	if calls.Load() != 0 {
		t.Fatalf("network calls = %d", calls.Load())
	}
}

func TestProbeHeaderFailuresAndRetry(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		header string
	}{
		{"missing", ""},
		{"negative", "-1"},
		{"overflow", "9223372036854775808"},
		{"junk", "12x"},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			downloader := NewDownloader(transportFunc(func(context.Context, *http.Request) (*http.Response, error) {
				return response(http.StatusOK, http.Header{"X-Head-Seqnum": []string{test.header}}, ""), nil
			}), Config{})
			_, err := downloader.Probe(context.Background(), "https://media.example/v", nil)
			if !errors.Is(err, ErrProbeFailed) || !errors.Is(err, ErrHeadSequence) {
				t.Fatalf("error = %v", err)
			}
		})
	}

	var calls atomic.Int32
	var retries atomic.Int32
	downloader := NewDownloader(transportFunc(func(context.Context, *http.Request) (*http.Response, error) {
		if calls.Add(1) == 1 {
			return response(http.StatusServiceUnavailable, nil, ""), nil
		}
		return response(http.StatusOK, http.Header{"X-Head-Seqnum": []string{"3"}}, ""), nil
	}), Config{Attempts: 2, RetryBaseDelay: time.Nanosecond})
	plan, err := downloader.Probe(context.Background(), "https://media.example/v?pot=probe-secret&n=throttle-secret", events.SinkFunc(func(_ context.Context, event events.Event) error {
		if event.Kind == events.KindRetry {
			retries.Add(1)
			if event.URL != "https://media.example/v" {
				t.Errorf("retry event URL = %q", event.URL)
			}
		}
		return nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Segments) != 2 || calls.Load() != 2 || retries.Load() != 1 {
		t.Fatalf("plan=%#v calls=%d retries=%d", plan, calls.Load(), retries.Load())
	}
}

func TestSignedURLsAreRedactedFromTransportFailures(t *testing.T) {
	t.Parallel()
	const signedURL = "https://media.example/v?sig=super-secret"
	probeFailure := NewDownloader(transportFunc(func(_ context.Context, request *http.Request) (*http.Response, error) {
		return nil, errors.New(request.URL.String())
	}), Config{Attempts: 1})
	if _, err := probeFailure.Probe(context.Background(), signedURL, nil); !errors.Is(err, ErrProbeFailed) ||
		strings.Contains(err.Error(), "super-secret") {
		t.Fatalf("probe error = %v", err)
	}

	segmentFailure := NewDownloader(transportFunc(func(_ context.Context, request *http.Request) (*http.Response, error) {
		if request.URL.Query().Get("sq") == "" {
			return response(http.StatusOK, http.Header{"X-Head-Seqnum": []string{"2"}}, ""), nil
		}
		return nil, errors.New(request.URL.String())
	}), Config{Attempts: 1})
	root := t.TempDir()
	if _, err := segmentFailure.Download(
		context.Background(), signedURL, root, filepath.Join(root, "media"), false, nil,
	); !errors.Is(err, ErrDownloadFailed) || strings.Contains(err.Error(), "super-secret") {
		t.Fatalf("segment error = %v", err)
	}
}

func TestDownloadFailureCleansTemporaryStateAndPreservesDestination(t *testing.T) {
	t.Parallel()
	transport := transportFunc(func(_ context.Context, request *http.Request) (*http.Response, error) {
		if request.URL.Query().Get("sq") == "" {
			return response(http.StatusOK, http.Header{"X-Head-Seqnum": []string{"3"}}, ""), nil
		}
		if request.URL.Query().Get("sq") == "1" {
			return response(http.StatusForbidden, nil, ""), nil
		}
		return response(http.StatusOK, nil, "segment-zero"), nil
	})
	root := t.TempDir()
	destination := filepath.Join(root, "media.bin")
	if err := os.WriteFile(destination, []byte("original"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := NewDownloader(transport, Config{Attempts: 1}).Download(
		context.Background(), "https://media.example/v", root, destination, true, nil)
	if err == nil {
		t.Fatal("expected failure")
	}
	if !errors.Is(err, ErrDownloadFailed) {
		t.Fatalf("uncategorized failure: %v", err)
	}
	body, readErr := os.ReadFile(destination)
	if readErr != nil || string(body) != "original" {
		t.Fatalf("destination body=%q error=%v", body, readErr)
	}
	for _, path := range []string{destination + ".part", destination + ".fragments"} {
		if _, statErr := os.Lstat(path); !errors.Is(statErr, os.ErrNotExist) {
			t.Errorf("temporary path remains: %s (%v)", path, statErr)
		}
	}
}

func TestDownloadRetriesSegmentsAndEnforcesSizeLimit(t *testing.T) {
	t.Parallel()
	var segmentCalls atomic.Int32
	var retries atomic.Int32
	transport := transportFunc(func(_ context.Context, request *http.Request) (*http.Response, error) {
		if request.URL.Query().Get("sq") == "" {
			return response(http.StatusOK, http.Header{"X-Head-Seqnum": []string{"2"}}, ""), nil
		}
		if segmentCalls.Add(1) == 1 {
			return response(http.StatusServiceUnavailable, nil, ""), nil
		}
		return response(http.StatusOK, nil, "oversized"), nil
	})
	root := t.TempDir()
	destination := filepath.Join(root, "media.bin")
	_, err := NewDownloader(transport, Config{
		Attempts: 2, RetryBaseDelay: time.Nanosecond, MaxSegmentSize: 4,
	}).Download(context.Background(), "https://media.example/v", root, destination, false,
		events.SinkFunc(func(_ context.Context, event events.Event) error {
			if event.Kind == events.KindRetry {
				retries.Add(1)
			}
			return nil
		}))
	if !errors.Is(err, ErrDownloadFailed) || !errors.Is(err, fragment.ErrSegmentTooLarge) {
		t.Fatalf("error = %v", err)
	}
	if segmentCalls.Load() != 2 || retries.Load() != 1 {
		t.Fatalf("segment calls=%d retries=%d", segmentCalls.Load(), retries.Load())
	}
	if _, err := os.Lstat(destination + ".fragments"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("fragment state remains: %v", err)
	}
}

func TestUnsafeDestinationFailsBeforeNetwork(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	transport := transportFunc(func(context.Context, *http.Request) (*http.Response, error) {
		calls.Add(1)
		return response(http.StatusOK, http.Header{"X-Head-Seqnum": []string{"3"}}, ""), nil
	})
	root := t.TempDir()
	_, err := NewDownloader(transport, Config{}).Download(
		context.Background(), "https://media.example/v", root, filepath.Join(root, "..", "escape"), false, nil)
	if !errors.Is(err, ErrUnsafeOutput) {
		t.Fatalf("error = %v", err)
	}
	if calls.Load() != 0 {
		t.Fatalf("network calls = %d", calls.Load())
	}
}

func TestExistingDestinationFailsBeforeNetwork(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	transport := transportFunc(func(context.Context, *http.Request) (*http.Response, error) {
		calls.Add(1)
		return response(http.StatusOK, http.Header{"X-Head-Seqnum": []string{"3"}}, ""), nil
	})
	root := t.TempDir()
	destination := filepath.Join(root, "existing.bin")
	if err := os.WriteFile(destination, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := NewDownloader(transport, Config{}).Download(
		context.Background(), "https://media.example/v?pot=secret", root, destination, false, nil)
	if !errors.Is(err, ErrOutputExists) {
		t.Fatalf("error = %v", err)
	}
	if calls.Load() != 0 {
		t.Fatalf("network calls = %d", calls.Load())
	}
	body, readErr := os.ReadFile(destination)
	if readErr != nil || string(body) != "keep" {
		t.Fatalf("destination = %q, %v", body, readErr)
	}
}

func TestDownloadCancellationStopsWorkAndCleansState(t *testing.T) {
	t.Parallel()
	started := make(chan struct{})
	transport := transportFunc(func(ctx context.Context, request *http.Request) (*http.Response, error) {
		if request.URL.Query().Get("sq") == "" {
			return response(http.StatusOK, http.Header{"X-Head-Seqnum": []string{"3"}}, ""), nil
		}
		select {
		case <-started:
		default:
			close(started)
		}
		<-ctx.Done()
		return nil, ctx.Err()
	})
	var cancelled atomic.Int32
	sink := events.SinkFunc(func(_ context.Context, event events.Event) error {
		if event.Kind == events.KindCancelled {
			cancelled.Add(1)
		}
		return nil
	})
	root := t.TempDir()
	destination := filepath.Join(root, "media.bin")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := NewDownloader(transport, Config{FragmentConcurrency: 2}).Download(
			ctx, "https://media.example/v", root, destination, false, sink)
		done <- err
	}()
	<-started
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("error = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("download did not honor cancellation")
	}
	if cancelled.Load() != 1 {
		t.Fatalf("cancel events = %d", cancelled.Load())
	}
	for _, path := range []string{destination, destination + ".part", destination + ".fragments"} {
		if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("path remains: %s (%v)", path, err)
		}
	}
}

func TestConfigAndSinkErrorsAreCategorized(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	transport := transportFunc(func(context.Context, *http.Request) (*http.Response, error) {
		calls.Add(1)
		return response(http.StatusOK, http.Header{"X-Head-Seqnum": []string{"3"}}, ""), nil
	})
	if _, err := NewDownloader(transport, Config{FragmentConcurrency: 129}).Probe(
		context.Background(), "https://media.example/v", nil); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("config error = %v", err)
	}
	if _, err := NewDownloader(transport, Config{TargetDuration: 120*time.Hour + 1}).Probe(
		context.Background(), "https://media.example/v", nil); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("target duration error = %v", err)
	}
	sinkFailure := errors.New("fixture sink")
	root := t.TempDir()
	_, err := NewDownloader(transport, Config{}).Download(
		context.Background(), "https://media.example/v", root, filepath.Join(root, "out"), false,
		events.SinkFunc(func(context.Context, events.Event) error { return sinkFailure }))
	if !errors.Is(err, ErrEventSink) || !errors.Is(err, sinkFailure) {
		t.Fatalf("sink error = %v", err)
	}
	if calls.Load() != 0 {
		t.Fatalf("network calls = %d", calls.Load())
	}
}

func TestCompletedEventCannotVetoPublishedArtifact(t *testing.T) {
	t.Parallel()
	transport := transportFunc(func(_ context.Context, request *http.Request) (*http.Response, error) {
		if request.URL.Query().Get("sq") == "" {
			return response(http.StatusOK, http.Header{"X-Head-Seqnum": []string{"2"}}, ""), nil
		}
		return response(http.StatusOK, nil, "media"), nil
	})
	root := t.TempDir()
	destination := filepath.Join(root, "media.bin")
	result, err := NewDownloader(transport, Config{}).Download(
		context.Background(), "https://media.example/v", root, destination, false,
		events.SinkFunc(func(_ context.Context, event events.Event) error {
			if event.Kind == events.KindCompleted {
				return errors.New("terminal observer failed")
			}
			return nil
		}))
	if err != nil {
		t.Fatal(err)
	}
	if result.Path != destination || result.Bytes != 5 {
		t.Fatalf("result = %#v", result)
	}
}

func FuzzParseHeadSequence(f *testing.F) {
	for _, seed := range []string{"", "0", " 42 ", "-1", "+3", "12x", "9223372036854775808"} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, value string) {
		head, err := parseHeadSequence(value)
		if err == nil {
			if head < 0 {
				t.Fatalf("accepted negative head %d", head)
			}
			canonical := strings.TrimSpace(value)
			parsed, parseErr := strconv.ParseInt(canonical, 10, 64)
			if parseErr != nil || parsed != head {
				t.Fatalf("accepted non-integer %q as %d", value, head)
			}
		} else if !errors.Is(err, ErrHeadSequence) {
			t.Fatalf("uncategorized error: %v", err)
		}
	})
}

func FuzzSequenceURLConstruction(f *testing.F) {
	f.Add("https://media.example/v?sig=a%2Bb&sq=old", int64(7))
	f.Add("http://127.0.0.1/media", int64(0))
	f.Add("https://media.example/v?foo=a%20b&bar=", int64(42))
	f.Fuzz(func(t *testing.T, raw string, sequence int64) {
		base, err := parseBaseURL(raw)
		if err != nil || sequence < 0 {
			return
		}
		result, err := sequenceURL(base, sequence)
		if err != nil {
			t.Fatalf("valid base failed: %v", err)
		}
		parsed, err := url.Parse(result)
		if err != nil {
			t.Fatalf("result parse failed: %v", err)
		}
		gotParts, gotSQ := queryPartsWithoutSQ(t, parsed.RawQuery)
		wantParts, _ := queryPartsWithoutSQ(t, base.RawQuery)
		if gotSQ != 1 || !strings.HasSuffix(parsed.RawQuery, "sq="+strconv.FormatInt(sequence, 10)) {
			t.Fatalf("sq count=%d in %q", gotSQ, result)
		}
		if strings.Join(gotParts, "&") != strings.Join(wantParts, "&") {
			t.Fatalf("query changed: before=%q after=%q", base.RawQuery, parsed.RawQuery)
		}
		if bytes.Contains([]byte(result), []byte{0}) {
			t.Fatal("result contains NUL")
		}
	})
}

func queryPartsWithoutSQ(t *testing.T, rawQuery string) ([]string, int) {
	t.Helper()
	if rawQuery == "" {
		return nil, 0
	}
	var filtered []string
	var sq int
	for _, part := range strings.Split(rawQuery, "&") {
		key := part
		if index := strings.IndexByte(key, '='); index >= 0 {
			key = key[:index]
		}
		decoded, err := url.QueryUnescape(key)
		if err != nil {
			t.Fatalf("invalid query key %q: %v", key, err)
		}
		if decoded == "sq" {
			sq++
		} else {
			filtered = append(filtered, part)
		}
	}
	return filtered, sq
}
