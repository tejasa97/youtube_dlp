package downloader

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/tejasa97/youtube_dlp/internal/network"
)

func TestHTTPChunkRangeRandomizesWithinFivePercentWindow(t *testing.T) {
	for rangeIndex := 0; rangeIndex < 100; rangeIndex++ {
		request := httptest.NewRequest(http.MethodGet, "https://media.example/video", nil)
		if err := setHTTPChunkRange(request, Job{HTTPChunkSize: 100, ExpectedBytes: 1000}, partialState{}, 0); err != nil {
			t.Fatal(err)
		}
		end, err := strconv.ParseInt(strings.TrimPrefix(request.Header.Get("Range"), "bytes=0-"), 10, 64)
		if err != nil || end < 94 || end > 99 {
			t.Fatalf("range = %q; want randomized size in [95, 100]", request.Header.Get("Range"))
		}
	}
}

func TestHTTPChunkNoContinueReloadsCumulativeCheckpointBetweenRanges(t *testing.T) {
	data := bytes.Repeat([]byte("chunk-checkpoint-fixture"), 150_000)
	var failureMu sync.Mutex
	forbidden := 0
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		failureMu.Lock()
		reject := strings.HasPrefix(request.Header.Get("Range"), "bytes=1048576-") && forbidden < 3
		if reject {
			forbidden++
		}
		failureMu.Unlock()
		if reject {
			http.Error(writer, "retry this chunk", http.StatusForbidden)
			return
		}
		http.ServeContent(writer, request, "media.bin", time.Time{}, bytes.NewReader(data))
	}))
	defer server.Close()

	root := t.TempDir()
	destination := filepath.Join(root, "media.bin")
	var committed int64
	job := checkpointJob(destination, "direct:chunk:no-continue", &CheckpointOptions{
		EveryBytes: minDirectCheckpointBytes,
		OnCommit: func(_ context.Context, checkpoint Checkpoint) error {
			if checkpoint.CommittedBytes < committed {
				return fmt.Errorf("checkpoint regressed from %d to %d", committed, checkpoint.CommittedBytes)
			}
			committed = checkpoint.CommittedBytes
			return nil
		},
	})
	job.URL = server.URL
	job.NoContinue = true
	job.HTTPChunkSize = 1 << 20
	job.HTTPChunkFixed = true
	job.ExpectedBytes = int64(len(data))
	job.Attempts = 3

	doer := checkpointDoerFunc(func(ctx context.Context, request *http.Request) (*http.Response, error) {
		return server.Client().Do(request.WithContext(ctx))
	})
	downloader := NewWithHooks(doer, time.Now, func(context.Context, time.Duration) error { return nil })
	result, err := downloader.Download(context.Background(), job, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.Bytes != int64(len(data)) || committed != int64(len(data)) {
		t.Fatalf("result bytes/commit = %d/%d, want %d", result.Bytes, committed, len(data))
	}
	got, err := os.ReadFile(destination)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, data) {
		t.Fatal("downloaded payload mismatch")
	}
}

func TestHTTPChunkRangeAvoidsSingleWholeResourceRequest(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "https://media.example/small-video", nil)
	job := Job{HTTPChunkSize: 10 << 20, ExpectedBytes: 6 << 20}
	if err := setHTTPChunkRange(request, job, partialState{}, 0); err != nil {
		t.Fatal(err)
	}
	if got := request.Header.Get("Range"); got != "bytes=0-3145727" {
		t.Fatalf("range = %q", got)
	}
	if err := setHTTPChunkRange(request, job, partialState{}, 3<<20); err != nil {
		t.Fatal(err)
	}
	if got := request.Header.Get("Range"); got != "bytes=3145728-6291455" {
		t.Fatalf("final range = %q", got)
	}
}

func TestHTTPChunkFixedUsesStableBoundaries(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "https://media.example/audio", nil)
	job := Job{HTTPChunkSize: 1 << 20, HTTPChunkFixed: true, ExpectedBytes: 2<<20 + 1}
	if err := setHTTPChunkRange(request, job, partialState{}, 0); err != nil {
		t.Fatal(err)
	}
	if got := request.Header.Get("Range"); got != "bytes=0-1048575" {
		t.Fatalf("range = %q", got)
	}
	if err := setHTTPChunkRange(request, job, partialState{}, 1<<20); err != nil {
		t.Fatal(err)
	}
	if got := request.Header.Get("Range"); got != "bytes=1048576-2097151" {
		t.Fatalf("resumed range = %q", got)
	}
}

func TestDownloadUsesBoundedHTTPChunksAndRetriesForbiddenChunk(t *testing.T) {
	media := []byte("hello world")
	var mu sync.Mutex
	var ranges []string
	forbidden := false
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		rawRange := request.Header.Get("Range")
		mu.Lock()
		ranges = append(ranges, rawRange)
		if rawRange == "bytes=4-7" && !forbidden {
			forbidden = true
			mu.Unlock()
			writer.WriteHeader(http.StatusForbidden)
			return
		}
		mu.Unlock()

		var start, end int
		if _, err := fmt.Sscanf(rawRange, "bytes=%d-%d", &start, &end); err != nil || start < 0 || end < start || end >= len(media) {
			writer.WriteHeader(http.StatusRequestedRangeNotSatisfiable)
			return
		}
		writer.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, len(media)))
		writer.Header().Set("Content-Length", fmt.Sprint(end-start+1))
		writer.WriteHeader(http.StatusPartialContent)
		_, _ = writer.Write(media[start : end+1])
	}))
	defer server.Close()

	transport, err := network.New(network.Config{})
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	destination := filepath.Join(root, "media.bin")
	result, err := New(transport).Download(context.Background(), Job{
		URL: server.URL, OutputRoot: root, Destination: destination,
		HTTPChunkSize: 4, ExpectedBytes: int64(len(media)), Attempts: 3,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.Bytes != int64(len(media)) {
		t.Fatalf("result bytes = %d; want %d", result.Bytes, len(media))
	}
	downloaded, err := os.ReadFile(destination)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(downloaded, media) {
		t.Fatalf("downloaded = %q; want %q", downloaded, media)
	}
	mu.Lock()
	defer mu.Unlock()
	wantRanges := []string{"bytes=0-3", "bytes=4-7", "bytes=4-7", "bytes=8-10"}
	if !reflect.DeepEqual(ranges, wantRanges) {
		t.Fatalf("ranges = %#v; want %#v", ranges, wantRanges)
	}
}

func TestHTTPChunkRefreshesForbiddenURLAndResumes(t *testing.T) {
	media := []byte("refreshable-media")
	var oldCalls, refreshCalls int
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/old" {
			oldCalls++
			if request.Header.Get("Range") != "bytes=0-3" {
				writer.WriteHeader(http.StatusForbidden)
				return
			}
		}
		var start, end int
		if _, err := fmt.Sscanf(request.Header.Get("Range"), "bytes=%d-%d", &start, &end); err != nil {
			writer.WriteHeader(http.StatusBadRequest)
			return
		}
		writer.Header().Set("Last-Modified", "Mon, 17 Aug 2026 08:00:00 GMT")
		writer.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, len(media)))
		writer.Header().Set("Content-Length", fmt.Sprint(end-start+1))
		writer.WriteHeader(http.StatusPartialContent)
		_, _ = writer.Write(media[start : end+1])
	}))
	defer server.Close()
	transport, err := network.New(network.Config{})
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	result, err := New(transport).Download(context.Background(), Job{
		URL: server.URL + "/old", OutputRoot: root, Destination: filepath.Join(root, "media.bin"),
		HTTPChunkSize: 4, HTTPChunkFixed: true, ExpectedBytes: int64(len(media)),
		Refresh: func(_ context.Context, request RefreshRequest) (RefreshResult, error) {
			refreshCalls++
			if request.StatusCode != http.StatusForbidden || request.Offset != 4 || request.Total != int64(len(media)) {
				t.Fatalf("refresh request = %#v", request)
			}
			return RefreshResult{URL: server.URL + "/fresh", ExpectedBytes: int64(len(media))}, nil
		},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.Bytes != int64(len(media)) || oldCalls != 2 || refreshCalls != 1 {
		t.Fatalf("result=%#v oldCalls=%d refreshCalls=%d", result, oldCalls, refreshCalls)
	}
	got, err := os.ReadFile(filepath.Join(root, "media.bin"))
	if err != nil || !reflect.DeepEqual(got, media) {
		t.Fatalf("media=%q err=%v", got, err)
	}
}

func TestHTTPChunkRefreshBudgetIsOperationScoped(t *testing.T) {
	var refreshes int
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.WriteHeader(http.StatusForbidden)
	}))
	defer server.Close()
	transport, err := network.New(network.Config{})
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	_, err = New(transport).Download(context.Background(), Job{
		URL: server.URL + "/old", OutputRoot: root, Destination: filepath.Join(root, "media.bin"),
		HTTPChunkSize: 4, HTTPChunkFixed: true, Attempts: 3, RefreshAttempts: 2,
		Refresh: func(context.Context, RefreshRequest) (RefreshResult, error) {
			refreshes++
			return RefreshResult{URL: server.URL + fmt.Sprintf("/fresh-%d", refreshes)}, nil
		},
	}, nil)
	var status *HTTPStatusError
	if !errors.As(err, &status) || status.Code != http.StatusForbidden {
		t.Fatalf("error = %v", err)
	}
	if refreshes != 2 {
		t.Fatalf("refreshes = %d; want operation-wide budget 2", refreshes)
	}
}
