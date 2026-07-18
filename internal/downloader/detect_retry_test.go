package downloader

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/ytdlp-go/ytdlp/internal/network"
)

func TestThrottleDetectorUsesInjectedClock(t *testing.T) {
	now := time.Unix(0, 0)
	detector := newThrottleDetector(10, time.Second, func() time.Time { return now })
	now = now.Add(time.Second)
	if !detector.Observe(1) {
		t.Fatal("slow response was not detected")
	}
}

func TestDownloadRestartsAfterThrottleAndExhaustsBound(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) { requests.Add(1); _, _ = writer.Write([]byte("x")) }))
	defer server.Close()
	transport, _ := network.New(network.Config{})
	calls := atomic.Int32{}
	now := func() time.Time {
		if calls.Add(1)%2 == 1 {
			return time.Unix(0, 0)
		}
		return time.Unix(2, 0)
	}
	client := NewWithHooks(transport, now, func(context.Context, time.Duration) error { return nil })
	root := t.TempDir()
	_, err := client.Download(context.Background(), Job{URL: server.URL, OutputRoot: root, Destination: filepath.Join(root, "out"), Attempts: 4, ThrottleRate: 10, ThrottleWindow: time.Second, ThrottleRestarts: 1}, nil)
	if !errors.Is(err, ErrThrottleExhausted) || requests.Load() != 2 {
		t.Fatalf("err=%v requests=%d", err, requests.Load())
	}
}

func TestDownloadRestartsAfterThrottleAndRecovers(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		if requests.Add(1) == 1 {
			_, _ = writer.Write([]byte("x"))
			return
		}
		_, _ = writer.Write([]byte("recovered"))
	}))
	defer server.Close()
	transport, _ := network.New(network.Config{})
	calls := atomic.Int32{}
	now := func() time.Time {
		switch calls.Add(1) {
		case 1:
			return time.Unix(0, 0)
		case 2:
			return time.Unix(2, 0)
		default:
			return time.Unix(2, 0)
		}
	}
	client := NewWithHooks(transport, now, func(context.Context, time.Duration) error { return nil })
	root := t.TempDir()
	result, err := client.Download(context.Background(), Job{URL: server.URL, OutputRoot: root, Destination: filepath.Join(root, "out"), Attempts: 3, ThrottleRate: 10, ThrottleWindow: time.Second, ThrottleRestarts: 1}, nil)
	if err != nil || result.Bytes != int64(len("recovered")) || requests.Load() != 2 {
		t.Fatalf("result=%#v err=%v requests=%d", result, err, requests.Load())
	}
}

func TestFileRetryIsBoundedAndCancellable(t *testing.T) {
	var calls, sleeps int
	client := NewWithHooks(nil, time.Now, func(_ context.Context, _ time.Duration) error { sleeps++; return nil })
	err := client.retryFile(context.Background(), Job{FileAttempts: 3}, func() error {
		calls++
		if calls < 3 {
			return syscall.EBUSY
		}
		return nil
	})
	if err != nil || calls != 3 || sleeps != 2 {
		t.Fatalf("err=%v calls=%d sleeps=%d", err, calls, sleeps)
	}
	cancelled := errors.New("cancelled sleeper")
	client = NewWithHooks(nil, time.Now, func(context.Context, time.Duration) error { return cancelled })
	err = client.retryFile(context.Background(), Job{FileAttempts: 3}, func() error { return syscall.EBUSY })
	if !errors.Is(err, cancelled) {
		t.Fatalf("cancellation err=%v", err)
	}
}
