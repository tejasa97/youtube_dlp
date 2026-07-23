package youtubelive

import (
	"context"
	"errors"
	"io"
	"net/http"
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

func TestLiveDownloadRefreshesAndFinalProbesEndedStream(t *testing.T) {
	t.Parallel()
	var nowMu sync.Mutex
	now := time.Unix(2_000_000_000, 0)
	clock := func() time.Time {
		nowMu.Lock()
		defer nowMu.Unlock()
		return now
	}
	wait := func(context.Context, time.Duration) error {
		nowMu.Lock()
		now = now.Add(6 * time.Hour)
		nowMu.Unlock()
		return nil
	}
	var requestsMu sync.Mutex
	var requested []string
	var oldProbes, newProbes atomic.Int32
	transport := transportFunc(func(_ context.Context, request *http.Request) (*http.Response, error) {
		requestsMu.Lock()
		requested = append(requested, request.URL.String())
		requestsMu.Unlock()
		if request.URL.Query().Get("sq") != "" {
			return response(http.StatusOK, nil, request.URL.Query().Get("sq")), nil
		}
		if request.URL.Query().Get("token") == "old-secret" {
			oldProbes.Add(1)
			return response(http.StatusOK, http.Header{"X-Head-Seqnum": []string{"1"}}, ""), nil
		}
		newProbes.Add(1)
		return response(http.StatusOK, http.Header{"X-Head-Seqnum": []string{"3"}}, ""), nil
	})
	var refreshes atomic.Int32
	refresh := func(_ context.Context, request LiveRefreshRequest) (LiveRefreshResult, error) {
		if request.URL != "https://media.example/live?token=old-secret&foo=a%20b" {
			t.Errorf("refresh URL = %q", request.URL)
		}
		refreshes.Add(1)
		return LiveRefreshResult{
			URL: "https://media.example/live?token=new-secret&foo=a%20b",
			Headers: http.Header{
				"X-Refreshed": []string{"yes"},
			},
			StillLive: false,
		}, nil
	}
	var eventURLs []string
	var eventMu sync.Mutex
	sink := events.SinkFunc(func(_ context.Context, event events.Event) error {
		eventMu.Lock()
		defer eventMu.Unlock()
		eventURLs = append(eventURLs, event.URL)
		if strings.Contains(event.URL, "secret") || strings.Contains(event.URL, "?") {
			t.Errorf("event leaked query: %q", event.URL)
		}
		return nil
	})
	root := t.TempDir()
	destination := filepath.Join(root, "live.bin")
	result, err := NewLiveDownloader(transport, LiveConfig{
		Headers:            http.Header{"X-Initial": []string{"yes"}},
		Refresh:            refresh,
		Now:                clock,
		Wait:               wait,
		TargetDuration:     time.Second,
		MaxSegments:        10,
		MaxPolls:           4,
		MaxNoProgressPolls: 3,
	}).Download(context.Background(), "https://media.example/live?token=old-secret&foo=a%20b", root, destination, false, sink)
	if err != nil {
		t.Fatal(err)
	}
	if result.Segments != 4 || result.Last != 3 || result.Bytes != 4 {
		t.Fatalf("result = %#v", result)
	}
	body, err := os.ReadFile(destination)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "0123" {
		t.Fatalf("body = %q", body)
	}
	if refreshes.Load() != 1 || oldProbes.Load() != 1 || newProbes.Load() != 1 {
		t.Fatalf("refreshes=%d old probes=%d new probes=%d", refreshes.Load(), oldProbes.Load(), newProbes.Load())
	}
	requestsMu.Lock()
	defer requestsMu.Unlock()
	for _, raw := range requested {
		if strings.Contains(raw, "sq=") && !strings.Contains(raw, "foo=a%20b") {
			t.Errorf("signed query spelling changed: %q", raw)
		}
	}
	eventMu.Lock()
	defer eventMu.Unlock()
	if len(eventURLs) == 0 {
		t.Fatal("no events")
	}
}

func TestLiveDownloadAggressivelyRefreshesAfterMisses(t *testing.T) {
	t.Parallel()
	var probes atomic.Int32
	transport := transportFunc(func(_ context.Context, request *http.Request) (*http.Response, error) {
		if sequence := request.URL.Query().Get("sq"); sequence != "" {
			return response(http.StatusOK, nil, sequence), nil
		}
		call := probes.Add(1)
		head := "0"
		if call >= 4 {
			head = "1"
		}
		return response(http.StatusOK, http.Header{"X-Head-Seqnum": []string{head}}, ""), nil
	})
	var refreshes atomic.Int32
	refresh := func(context.Context, LiveRefreshRequest) (LiveRefreshResult, error) {
		call := refreshes.Add(1)
		return LiveRefreshResult{
			URL:       "https://media.example/live?renewed=secret",
			StillLive: call < 2,
		}, nil
	}
	root := t.TempDir()
	result, err := NewLiveDownloader(transport, LiveConfig{
		Refresh: refresh, Wait: noWait, TargetDuration: time.Second,
		MaxPolls: 8, MaxSegments: 4, MaxNoProgressPolls: 4, AggressiveRefresh: 2,
	}).Download(context.Background(), "https://media.example/live?initial=secret", root, filepath.Join(root, "live.bin"), false, nil)
	if err != nil {
		t.Fatal(err)
	}
	if refreshes.Load() != 2 || result.Segments != 2 || result.Last != 1 {
		t.Fatalf("refreshes=%d result=%#v probes=%d", refreshes.Load(), result, probes.Load())
	}
}

func TestLiveProbeFailuresTriggerAggressiveRefreshBeforeFailureLimit(t *testing.T) {
	t.Parallel()
	var probes atomic.Int32
	transport := transportFunc(func(_ context.Context, request *http.Request) (*http.Response, error) {
		if sequence := request.URL.Query().Get("sq"); sequence != "" {
			return response(http.StatusOK, nil, sequence), nil
		}
		if probes.Add(1) <= 2 {
			return nil, errors.New("signed transport failure")
		}
		return response(http.StatusOK, http.Header{"X-Head-Seqnum": []string{"0"}}, ""), nil
	})
	var refreshes atomic.Int32
	root := t.TempDir()
	result, err := NewLiveDownloader(transport, LiveConfig{
		TargetDuration: time.Second, Wait: noWait, MaxPolls: 4,
		MaxProbeFailures: 2, AggressiveRefresh: 2, MaxNoProgressPolls: 3,
		Refresh: func(context.Context, LiveRefreshRequest) (LiveRefreshResult, error) {
			refreshes.Add(1)
			return LiveRefreshResult{URL: "https://media.example/refreshed?sig=secret", StillLive: false}, nil
		},
	}).Download(context.Background(), "https://media.example/live?sig=secret", root, filepath.Join(root, "live.bin"), false, nil)
	if err != nil {
		t.Fatal(err)
	}
	if refreshes.Load() != 1 || probes.Load() != 3 || result.Segments != 1 {
		t.Fatalf("refreshes=%d probes=%d result=%#v", refreshes.Load(), probes.Load(), result)
	}
}

func TestLiveMalformedProbeIsCategorizedAndRedacted(t *testing.T) {
	t.Parallel()
	transport := transportFunc(func(context.Context, *http.Request) (*http.Response, error) {
		return response(http.StatusOK, http.Header{"X-Head-Seqnum": []string{"not-a-sequence"}}, ""), nil
	})
	root := t.TempDir()
	_, err := NewLiveDownloader(transport, LiveConfig{
		TargetDuration: time.Second, MaxProbeFailures: 1,
	}).Download(context.Background(), "https://media.example/live?sig=do-not-leak", root, filepath.Join(root, "live.bin"), false, nil)
	if !errors.Is(err, ErrLiveDownloadFailed) || !errors.Is(err, ErrLiveProbeFailed) || !errors.Is(err, ErrLiveHeadSequence) {
		t.Fatalf("error = %v", err)
	}
	if strings.Contains(err.Error(), "do-not-leak") {
		t.Fatalf("error leaked URL: %v", err)
	}
}

func TestLiveTransportAndRefreshFailuresDoNotLeakSignedURL(t *testing.T) {
	t.Parallel()
	signed := "https://media.example/live?sig=super-secret"
	root := t.TempDir()
	_, err := NewLiveDownloader(transportFunc(func(context.Context, *http.Request) (*http.Response, error) {
		return nil, errors.New("GET " + signed + ": fixture failed")
	}), LiveConfig{
		TargetDuration: time.Second, MaxProbeFailures: 1,
	}).Download(context.Background(), signed, root, filepath.Join(root, "probe.bin"), false, nil)
	if !errors.Is(err, ErrLiveProbeFailed) || strings.Contains(err.Error(), "super-secret") {
		t.Fatalf("probe error = %v", err)
	}

	var probes atomic.Int32
	_, err = NewLiveDownloader(transportFunc(func(_ context.Context, request *http.Request) (*http.Response, error) {
		if request.URL.Query().Get("sq") != "" {
			return response(http.StatusOK, nil, "segment"), nil
		}
		probes.Add(1)
		return response(http.StatusOK, http.Header{"X-Head-Seqnum": []string{"0"}}, ""), nil
	}), LiveConfig{
		TargetDuration: time.Second, Wait: noWait, MaxPolls: 3,
		MaxNoProgressPolls: 1, AggressiveRefresh: 1, MaxRefreshFailures: 1,
		Refresh: func(context.Context, LiveRefreshRequest) (LiveRefreshResult, error) {
			return LiveRefreshResult{}, errors.New("refresh " + signed)
		},
	}).Download(context.Background(), signed, root, filepath.Join(root, "refresh.bin"), false, nil)
	if !errors.Is(err, ErrLiveRefreshFailed) || strings.Contains(err.Error(), "super-secret") {
		t.Fatalf("refresh error = %v", err)
	}
}

func TestLiveCancellationCleansTemporaryArtifacts(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	var cancelled atomic.Int32
	transport := transportFunc(func(_ context.Context, request *http.Request) (*http.Response, error) {
		if request.URL.Query().Get("sq") != "" {
			return response(http.StatusOK, nil, "zero"), nil
		}
		return response(http.StatusOK, http.Header{"X-Head-Seqnum": []string{"0"}}, ""), nil
	})
	wait := func(context.Context, time.Duration) error {
		cancel()
		return context.Canceled
	}
	root := t.TempDir()
	destination := filepath.Join(root, "live.bin")
	_, err := NewLiveDownloader(transport, LiveConfig{
		TargetDuration: time.Second, Wait: wait,
	}).Download(ctx, "https://media.example/live", root, destination, false, events.SinkFunc(func(_ context.Context, event events.Event) error {
		if event.Kind == events.KindCancelled {
			cancelled.Add(1)
		}
		return nil
	}))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v", err)
	}
	if cancelled.Load() != 1 {
		t.Fatalf("cancel events = %d", cancelled.Load())
	}
	for _, path := range []string{destination, destination + ".part", destination + ".live.fragments"} {
		if _, statErr := os.Lstat(path); !errors.Is(statErr, os.ErrNotExist) {
			t.Errorf("%s remains: %v", path, statErr)
		}
	}
}

func TestLiveEventSinkFailureCleansAndCompletionCannotVeto(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	transport := transportFunc(func(_ context.Context, request *http.Request) (*http.Response, error) {
		calls.Add(1)
		if request.URL.Query().Get("sq") == "" {
			return response(http.StatusOK, http.Header{"X-Head-Seqnum": []string{"0"}}, ""), nil
		}
		return response(http.StatusOK, nil, "media"), nil
	})
	refresh := func(context.Context, LiveRefreshRequest) (LiveRefreshResult, error) {
		return LiveRefreshResult{StillLive: false}, nil
	}
	sinkFailure := errors.New("fixture observer")
	root := t.TempDir()
	failedDestination := filepath.Join(root, "failed.bin")
	_, err := NewLiveDownloader(transport, LiveConfig{
		TargetDuration: time.Second, RefreshInterval: time.Nanosecond, Refresh: refresh,
	}).Download(context.Background(), "https://media.example/live?pot=secret", root, failedDestination, false,
		events.SinkFunc(func(context.Context, events.Event) error { return sinkFailure }))
	if !errors.Is(err, ErrEventSink) || !errors.Is(err, sinkFailure) || calls.Load() != 0 {
		t.Fatalf("starting sink error=%v calls=%d", err, calls.Load())
	}
	for _, path := range []string{failedDestination, failedDestination + ".part", failedDestination + ".live.fragments"} {
		if _, statErr := os.Lstat(path); !errors.Is(statErr, os.ErrNotExist) {
			t.Fatalf("failed sink path remains: %s (%v)", path, statErr)
		}
	}

	destination := filepath.Join(root, "completed.bin")
	result, err := NewLiveDownloader(transport, LiveConfig{
		TargetDuration: time.Second, RefreshInterval: time.Nanosecond, Refresh: refresh,
	}).Download(context.Background(), "https://media.example/live?pot=secret", root, destination, false,
		events.SinkFunc(func(_ context.Context, event events.Event) error {
			if event.Kind == events.KindCompleted {
				return sinkFailure
			}
			return nil
		}))
	if err != nil || result.Path != destination || result.Bytes != 5 {
		t.Fatalf("completed result=%#v error=%v", result, err)
	}
}

func TestLiveLimitsAndExistingOutputPreflight(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	transport := transportFunc(func(_ context.Context, request *http.Request) (*http.Response, error) {
		calls.Add(1)
		if request.URL.Query().Get("sq") == "" {
			return response(http.StatusOK, http.Header{"X-Head-Seqnum": []string{"2"}}, ""), nil
		}
		return response(http.StatusOK, nil, "x"), nil
	})
	root := t.TempDir()
	existing := filepath.Join(root, "existing.bin")
	if err := os.WriteFile(existing, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := NewLiveDownloader(transport, LiveConfig{TargetDuration: time.Second}).Download(
		context.Background(), "https://media.example/live", root, existing, false, nil)
	if !errors.Is(err, ErrOutputExists) || calls.Load() != 0 {
		t.Fatalf("preflight error=%v calls=%d", err, calls.Load())
	}
	if err := os.Remove(existing); err != nil {
		t.Fatal(err)
	}
	for _, reserved := range []string{existing + ".part", existing + ".live.fragments"} {
		if strings.HasSuffix(reserved, ".fragments") {
			if err := os.MkdirAll(reserved, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(reserved, "keep"), []byte("keep"), 0o600); err != nil {
				t.Fatal(err)
			}
		} else if err := os.WriteFile(reserved, []byte("keep"), 0o600); err != nil {
			t.Fatal(err)
		}
		_, err := NewLiveDownloader(transport, LiveConfig{TargetDuration: time.Second}).Download(
			context.Background(), "https://media.example/live", root, existing, true, nil)
		if !errors.Is(err, ErrOutputExists) {
			t.Fatalf("reserved %s error=%v", reserved, err)
		}
		if calls.Load() != 0 {
			t.Fatalf("reserved %s made %d calls", reserved, calls.Load())
		}
		if _, statErr := os.Lstat(reserved); statErr != nil {
			t.Fatalf("reserved %s was removed: %v", reserved, statErr)
		}
		if strings.HasSuffix(reserved, ".part") {
			if err := os.Remove(reserved); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := os.RemoveAll(existing + ".live.fragments"); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("target", existing+".part"); err == nil {
		_, err := NewLiveDownloader(transport, LiveConfig{TargetDuration: time.Second}).Download(
			context.Background(), "https://media.example/live", root, existing, true, nil)
		if !errors.Is(err, ErrUnsafeOutput) || calls.Load() != 0 {
			t.Fatalf("reserved symlink error=%v calls=%d", err, calls.Load())
		}
		if err := os.Remove(existing + ".part"); err != nil {
			t.Fatal(err)
		}
	}
	destination := filepath.Join(root, "limited.bin")
	_, err = NewLiveDownloader(transport, LiveConfig{
		TargetDuration: time.Second, MaxSegments: 2,
	}).Download(context.Background(), "https://media.example/live", root, destination, false, nil)
	if !errors.Is(err, fragment.ErrTooManySegments) {
		t.Fatalf("limit error = %v", err)
	}
	if _, statErr := os.Stat(destination); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("destination exists after failure: %v", statErr)
	}
}

func TestLiveSegmentSizeLimitAndCleanup(t *testing.T) {
	t.Parallel()
	transport := transportFunc(func(_ context.Context, request *http.Request) (*http.Response, error) {
		if request.URL.Query().Get("sq") == "" {
			return response(http.StatusOK, http.Header{"X-Head-Seqnum": []string{"0"}}, ""), nil
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader("too large"))}, nil
	})
	root := t.TempDir()
	destination := filepath.Join(root, "live.bin")
	_, err := NewLiveDownloader(transport, LiveConfig{
		TargetDuration: time.Second, MaxSegmentSize: 3, Attempts: 1,
	}).Download(context.Background(), "https://media.example/live", root, destination, false, nil)
	if !errors.Is(err, fragment.ErrSegmentTooLarge) {
		t.Fatalf("error = %v", err)
	}
	if _, statErr := os.Stat(destination + ".live.fragments"); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("work directory remains: %v", statErr)
	}
}

func TestLive120HourClamp(t *testing.T) {
	t.Parallel()
	now := time.Unix(2_000_000_000, 0)
	if got := liveBeginSequence(20, 24*time.Hour, now.Add(-121*time.Hour).Unix(), now); got != 16 {
		t.Fatalf("begin = %d", got)
	}
	if got := liveBeginSequence(3, 24*time.Hour, now.Add(-121*time.Hour).Unix(), now); got != 0 {
		t.Fatalf("small begin = %d", got)
	}
	if got := liveBeginSequence(20, 24*time.Hour, now.Add(-119*time.Hour).Unix(), now); got != 0 {
		t.Fatalf("recent begin = %d", got)
	}
}

func TestLivePollLimit(t *testing.T) {
	t.Parallel()
	transport := transportFunc(func(_ context.Context, request *http.Request) (*http.Response, error) {
		if request.URL.Query().Get("sq") != "" {
			return response(http.StatusOK, nil, "x"), nil
		}
		return response(http.StatusOK, http.Header{"X-Head-Seqnum": []string{"0"}}, ""), nil
	})
	root := t.TempDir()
	_, err := NewLiveDownloader(transport, LiveConfig{
		TargetDuration: time.Second, Wait: noWait, MaxPolls: 2,
		MaxNoProgressPolls: 3,
	}).Download(context.Background(), "https://media.example/live", root, filepath.Join(root, "live.bin"), false, nil)
	if !errors.Is(err, ErrLivePollLimit) {
		t.Fatalf("error = %v", err)
	}
}

func FuzzLiveBeginSequence(f *testing.F) {
	f.Add(int64(20), int64(86_400), int64(121*60*60))
	f.Add(int64(3), int64(1), int64(0))
	f.Fuzz(func(t *testing.T, head, targetSeconds, ageSeconds int64) {
		if head < 0 || head > 1_000_000 || targetSeconds <= 0 || targetSeconds > int64(maxAvailableAge/time.Second) ||
			ageSeconds < 0 || ageSeconds > int64(1_000*time.Hour/time.Second) {
			t.Skip()
		}
		now := time.Unix(2_000_000_000, 0)
		begin := liveBeginSequence(head, time.Duration(targetSeconds)*time.Second, now.Add(-time.Duration(ageSeconds)*time.Second).Unix(), now)
		if begin < 0 || begin > head {
			t.Fatalf("head=%d target=%d age=%d begin=%d", head, targetSeconds, ageSeconds, begin)
		}
	})
}

func noWait(ctx context.Context, _ time.Duration) error {
	return ctx.Err()
}

func sequenceResponse(sequence int64) *http.Response {
	return response(http.StatusOK, nil, strconv.FormatInt(sequence, 10))
}
