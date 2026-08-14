package downloader

import (
	"context"
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
