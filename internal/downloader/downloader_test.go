package downloader

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ytdlp-go/ytdlp/internal/events"
	"github.com/ytdlp-go/ytdlp/internal/network"
	"github.com/ytdlp-go/ytdlp/internal/testserver"
)

func TestDownloadCompleteAndResume(t *testing.T) {
	server := testserver.New()
	defer server.Close()
	transport, _ := network.New(network.Config{})
	destination := filepath.Join(t.TempDir(), "media.bin")
	media := server.Media()
	part := destination + ".part"
	if err := os.WriteFile(part, media[:len(media)/2], 0o644); err != nil {
		t.Fatal(err)
	}
	state, _ := json.Marshal(partialState{URL: server.URL + "/media", ETag: server.MediaETag(), Total: int64(len(media))})
	if err := os.WriteFile(part+".json", state, 0o600); err != nil {
		t.Fatal(err)
	}

	var sawResume bool
	sink := events.SinkFunc(func(_ context.Context, event events.Event) error {
		sawResume = sawResume || event.Resuming
		return nil
	})
	result, err := New(transport).Download(context.Background(), Job{
		URL: server.URL + "/media", OutputRoot: filepath.Dir(destination), Destination: destination,
	}, sink)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Resumed || !sawResume {
		t.Fatalf("resume result = %#v, event = %v", result, sawResume)
	}
	downloaded, _ := os.ReadFile(destination)
	if string(downloaded) != string(media) {
		t.Fatal("downloaded bytes do not match fixture")
	}
	if _, err := os.Stat(part + ".json"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("partial state remains: %v", err)
	}
}

func TestDownloadRestartsWhenServerIgnoresRange(t *testing.T) {
	body := []byte("complete body")
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Length", fmt.Sprint(len(body)))
		_, _ = writer.Write(body)
	}))
	defer server.Close()
	transport, _ := network.New(network.Config{})
	destination := filepath.Join(t.TempDir(), "media.bin")
	_ = os.WriteFile(destination+".part", []byte("stale"), 0o644)
	state, _ := json.Marshal(partialState{URL: server.URL, ETag: `"old"`, Total: 99})
	_ = os.WriteFile(destination+".part.json", state, 0o600)
	result, err := New(transport).Download(context.Background(), Job{URL: server.URL, OutputRoot: filepath.Dir(destination), Destination: destination}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.Resumed {
		t.Fatal("ignored range was treated as resumed")
	}
	downloaded, _ := os.ReadFile(destination)
	if string(downloaded) != string(body) {
		t.Fatalf("downloaded = %q", downloaded)
	}
}

func TestDownloadUnknownLength(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		flusher := writer.(http.Flusher)
		_, _ = io.WriteString(writer, "first")
		flusher.Flush()
		_, _ = io.WriteString(writer, "second")
	}))
	defer server.Close()
	transport, _ := network.New(network.Config{})
	destination := filepath.Join(t.TempDir(), "unknown.bin")
	result, err := New(transport).Download(context.Background(), Job{URL: server.URL, OutputRoot: filepath.Dir(destination), Destination: destination}, nil)
	if err != nil || result.Bytes != int64(len("firstsecond")) {
		t.Fatalf("result = %#v, error = %v", result, err)
	}
}

func TestDownloadForwardsSelectedHTTPHeaders(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Referer") != "https://page.example/video" || request.Header.Get("X-Extractor") != "fixture" {
			http.Error(writer, "headers required", http.StatusForbidden)
			return
		}
		_, _ = writer.Write([]byte("media"))
	}))
	defer server.Close()
	transport, _ := network.New(network.Config{})
	root := t.TempDir()
	_, err := New(transport).Download(context.Background(), Job{
		URL: server.URL, Headers: http.Header{"Referer": {"https://page.example/video"}, "X-Extractor": {"fixture"}},
		OutputRoot: root, Destination: filepath.Join(root, "media.bin"),
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
}

func TestDownloadCancellationLeavesPartialState(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("ETag", `"slow"`)
		flusher := writer.(http.Flusher)
		for index := 0; index < 100; index++ {
			select {
			case <-request.Context().Done():
				return
			default:
			}
			_, _ = writer.Write(make([]byte, 1024))
			flusher.Flush()
			time.Sleep(5 * time.Millisecond)
		}
	}))
	defer server.Close()
	transport, _ := network.New(network.Config{})
	destination := filepath.Join(t.TempDir(), "cancel.bin")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sink := events.SinkFunc(func(_ context.Context, event events.Event) error {
		if event.Kind == events.KindProgress {
			cancel()
		}
		return nil
	})
	_, err := New(transport).Download(ctx, Job{URL: server.URL, OutputRoot: filepath.Dir(destination), Destination: destination}, sink)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Download() error = %v", err)
	}
	if info, err := os.Stat(destination + ".part"); err != nil || info.Size() == 0 {
		t.Fatalf("partial file missing or empty: %v", err)
	}
	if _, err := os.Stat(destination + ".part.json"); err != nil {
		t.Fatalf("partial state missing: %v", err)
	}
}

func TestDownloadSafetyAndOverwrite(t *testing.T) {
	root := t.TempDir()
	transport, _ := network.New(network.Config{})
	client := New(transport)
	if _, err := client.Download(context.Background(), Job{URL: "http://example.invalid", OutputRoot: root, Destination: filepath.Join(root, "..", "escape")}, nil); !errors.Is(err, ErrUnsafeDestination) {
		t.Fatalf("unsafe destination error = %v", err)
	}
	destination := filepath.Join(root, "exists")
	_ = os.WriteFile(destination, []byte("old"), 0o644)
	if _, err := client.Download(context.Background(), Job{URL: "http://example.invalid", OutputRoot: root, Destination: destination}, nil); !errors.Is(err, ErrDestinationExists) {
		t.Fatalf("existing destination error = %v", err)
	}

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = writer.Write([]byte("new"))
	}))
	defer server.Close()
	result, err := client.Download(context.Background(), Job{URL: server.URL, OutputRoot: root, Destination: destination, Overwrite: true}, nil)
	if err != nil || result.Bytes != 3 {
		t.Fatalf("overwrite result = %#v, error = %v", result, err)
	}
	if contents, _ := os.ReadFile(destination); string(contents) != "new" {
		t.Fatalf("overwrite contents = %q", contents)
	}
}

func TestDownloadRejectsSymlinkEscape(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	link := filepath.Join(root, "link")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	transport, _ := network.New(network.Config{})
	_, err := New(transport).Download(context.Background(), Job{
		URL: "http://example.invalid", OutputRoot: root, Destination: filepath.Join(link, "escape.bin"),
	}, nil)
	if !errors.Is(err, ErrUnsafeDestination) {
		t.Fatalf("symlink destination error = %v", err)
	}
}

func TestDownloadRejectsSymlinkPartialFile(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(t.TempDir(), "target")
	if err := os.WriteFile(target, []byte("outside"), 0o644); err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(root, "media.bin")
	if err := os.Symlink(target, destination+".part"); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	transport, _ := network.New(network.Config{})
	_, err := New(transport).Download(context.Background(), Job{
		URL: "http://example.invalid", OutputRoot: root, Destination: destination,
	}, nil)
	if !errors.Is(err, ErrUnsafeDestination) {
		t.Fatalf("symlink partial error = %v", err)
	}
	if contents, _ := os.ReadFile(target); string(contents) != "outside" {
		t.Fatalf("symlink target was modified: %q", contents)
	}
}

func TestDownloadRetriesRetryableFailure(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		if requests.Add(1) == 1 {
			http.Error(writer, "temporary", http.StatusServiceUnavailable)
			return
		}
		_, _ = writer.Write([]byte("recovered"))
	}))
	defer server.Close()
	transport, _ := network.New(network.Config{})
	destination := filepath.Join(t.TempDir(), "retry.bin")
	var retry events.Event
	sink := events.SinkFunc(func(_ context.Context, event events.Event) error {
		if event.Kind == events.KindRetry {
			retry = event
		}
		return nil
	})
	result, err := New(transport).Download(context.Background(), Job{URL: server.URL, OutputRoot: filepath.Dir(destination), Destination: destination}, sink)
	if err != nil {
		t.Fatal(err)
	}
	if requests.Load() != 2 || retry.Attempt != 2 || result.Bytes != int64(len("recovered")) {
		t.Fatalf("requests = %d, retry = %#v, result = %#v", requests.Load(), retry, result)
	}
}

func TestDownloadEventsRedactSignedURL(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = writer.Write([]byte("media"))
	}))
	defer server.Close()
	transport, _ := network.New(network.Config{})
	var captured []events.Event
	sink := events.SinkFunc(func(_ context.Context, event events.Event) error {
		captured = append(captured, event)
		return nil
	})
	root := t.TempDir()
	rawURL := server.URL + "/media?token=playback-secret&sig=signature-secret&visible=yes"
	if _, err := New(transport).Download(context.Background(), Job{
		URL: rawURL, OutputRoot: root, Destination: filepath.Join(root, "media.bin"),
	}, sink); err != nil {
		t.Fatal(err)
	}
	if len(captured) == 0 {
		t.Fatal("no download events captured")
	}
	for _, event := range captured {
		if strings.Contains(event.URL, "secret") || !strings.Contains(event.URL, "visible=yes") {
			t.Fatalf("event URL was not safely redacted: %#v", event)
		}
	}
}

func TestDownloadRestartsOnChangedETag(t *testing.T) {
	server := testserver.New()
	defer server.Close()
	transport, _ := network.New(network.Config{})
	destination := filepath.Join(t.TempDir(), "changed.bin")
	media := server.Media()
	_ = os.WriteFile(destination+".part", media[:100], 0o644)
	state, _ := json.Marshal(partialState{URL: server.URL + "/media", ETag: `"outdated"`, Total: int64(len(media))})
	_ = os.WriteFile(destination+".part.json", state, 0o600)
	result, err := New(transport).Download(context.Background(), Job{URL: server.URL + "/media", OutputRoot: filepath.Dir(destination), Destination: destination}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.Resumed {
		t.Fatal("changed ETag was incorrectly resumed")
	}
	contents, _ := os.ReadFile(destination)
	if string(contents) != string(media) {
		t.Fatal("changed-ETag restart produced wrong bytes")
	}
}

func TestRetryDelayIsDeterministicAndBounded(t *testing.T) {
	job := Job{RetryBaseDelay: 5 * time.Millisecond, RetryMaxDelay: 12 * time.Millisecond}
	if got := retryDelay(job, 1); got != 5*time.Millisecond {
		t.Fatalf("first=%s", got)
	}
	if got := retryDelay(job, 2); got != 10*time.Millisecond {
		t.Fatalf("second=%s", got)
	}
	if got := retryDelay(job, 3); got != 12*time.Millisecond {
		t.Fatalf("third=%s", got)
	}
}

func FuzzContentRange(f *testing.F) {
	f.Add("bytes 10-20/30", int64(10))
	f.Fuzz(func(t *testing.T, header string, offset int64) { _ = validContentRange(header, offset) })
}

func TestThrottleUsesInjectedClockAndSleeper(t *testing.T) {
	now := time.Unix(0, 0)
	var delays []time.Duration
	limiter := newThrottleWithClock(10, func() time.Time { return now }, func(_ context.Context, delay time.Duration) error { delays = append(delays, delay); return nil })
	if err := limiter.Wait(context.Background(), 10); err != nil {
		t.Fatal(err)
	}
	if err := limiter.Wait(context.Background(), 10); err != nil {
		t.Fatal(err)
	}
	if len(delays) != 1 || delays[0] != time.Second {
		t.Fatalf("delays=%v", delays)
	}
}

func TestDownloadRejectsUnboundedRetryConfiguration(t *testing.T) {
	root := t.TempDir()
	_, err := New(nil).Download(context.Background(), Job{URL: "https://example.test/a", OutputRoot: root, Destination: filepath.Join(root, "a"), Attempts: 101}, nil)
	if !errors.Is(err, ErrTooManyAttempts) {
		t.Fatalf("error=%v", err)
	}
}
