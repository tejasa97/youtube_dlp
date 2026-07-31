package downloader

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ytdlp-go/ytdlp/internal/events"
	"github.com/ytdlp-go/ytdlp/internal/network"
	"github.com/ytdlp-go/ytdlp/internal/testserver"
)

func sizedServer(body []byte, contentLength *int, contentEncoding string) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		if contentEncoding != "" {
			writer.Header().Set("Content-Encoding", contentEncoding)
		}
		if contentLength != nil {
			writer.Header().Set("Content-Length", fmt.Sprint(*contentLength))
		}
		_, _ = writer.Write(body)
	}))
}

func TestDownloadMaxFilesizeAbort(t *testing.T) {
	body := make([]byte, 2000)
	length := len(body)
	server := sizedServer(body, &length, "")
	defer server.Close()
	transport, _ := network.New(network.Config{})
	destination := filepath.Join(t.TempDir(), "media.bin")
	_, err := New(transport).Download(context.Background(), Job{
		URL: server.URL, OutputRoot: filepath.Dir(destination), Destination: destination,
		MaxFilesize: 1000,
	}, nil)
	var abort *FileSizeAbortError
	if !errors.As(err, &abort) {
		t.Fatalf("want FileSizeAbortError, got %v", err)
	}
	if abort.Message != "File is larger than max-filesize (2000 bytes > 1000 bytes)" {
		t.Fatalf("message = %q", abort.Message)
	}
	if !errors.Is(err, ErrFileSizeAbort) {
		t.Fatalf("errors.Is(ErrFileSizeAbort) failed: %v", err)
	}
	if _, statErr := os.Stat(destination); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("aborted download wrote a destination: %v", statErr)
	}
}

func TestDownloadMinFilesizeAbort(t *testing.T) {
	body := make([]byte, 200)
	length := len(body)
	server := sizedServer(body, &length, "")
	defer server.Close()
	transport, _ := network.New(network.Config{})
	destination := filepath.Join(t.TempDir(), "media.bin")
	_, err := New(transport).Download(context.Background(), Job{
		URL: server.URL, OutputRoot: filepath.Dir(destination), Destination: destination,
		MinFilesize: 1000,
	}, nil)
	var abort *FileSizeAbortError
	if !errors.As(err, &abort) {
		t.Fatalf("want FileSizeAbortError, got %v", err)
	}
	if abort.Message != "File is smaller than min-filesize (200 bytes < 1000 bytes)" {
		t.Fatalf("message = %q", abort.Message)
	}
}

func TestDownloadFilesizeWithinBoundsSucceeds(t *testing.T) {
	body := make([]byte, 2000)
	length := len(body)
	server := sizedServer(body, &length, "")
	defer server.Close()
	transport, _ := network.New(network.Config{})
	destination := filepath.Join(t.TempDir(), "media.bin")
	result, err := New(transport).Download(context.Background(), Job{
		URL: server.URL, OutputRoot: filepath.Dir(destination), Destination: destination,
		MinFilesize: 1000, MaxFilesize: 3000,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.Bytes != 2000 {
		t.Fatalf("bytes = %d", result.Bytes)
	}
}

func TestDownloadFilesizeSkipsContentEncoding(t *testing.T) {
	// Content-Encoding responses must skip the min/max size decision even
	// when the raw payload would violate the bound (reference http.py sets
	// data_len to None). A real gzip body keeps the transfer valid.
	var gzipped bytes.Buffer
	gzipWriter := gzip.NewWriter(&gzipped)
	if _, err := gzipWriter.Write(make([]byte, 2000)); err != nil {
		t.Fatal(err)
	}
	if err := gzipWriter.Close(); err != nil {
		t.Fatal(err)
	}
	body := gzipped.Bytes()
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Encoding", "gzip")
		writer.Header().Set("Content-Length", fmt.Sprint(len(body)))
		_, _ = writer.Write(body)
	}))
	defer server.Close()
	transport, _ := network.New(network.Config{})
	destination := filepath.Join(t.TempDir(), "media.bin")
	if _, err := New(transport).Download(context.Background(), Job{
		URL: server.URL, OutputRoot: filepath.Dir(destination), Destination: destination,
		MaxFilesize: 1000,
	}, nil); err != nil {
		t.Fatalf("Content-Encoding responses must skip the size check: %v", err)
	}
}

func TestDownloadFilesizeSkipsUnknownLength(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Transfer-Encoding", "chunked")
		_, _ = writer.Write(make([]byte, 2000))
	}))
	defer server.Close()
	transport, _ := network.New(network.Config{})
	destination := filepath.Join(t.TempDir(), "media.bin")
	if _, err := New(transport).Download(context.Background(), Job{
		URL: server.URL, OutputRoot: filepath.Dir(destination), Destination: destination,
		MinFilesize: 10_000,
	}, nil); err != nil {
		t.Fatalf("unknown-length responses must skip the size check: %v", err)
	}
}

func TestDownloadFilesizeUsesResumeOffset(t *testing.T) {
	server := testserver.New()
	defer server.Close()
	transport, _ := network.New(network.Config{})
	destination := filepath.Join(t.TempDir(), "media.bin")
	part := destination + ".part"
	offset := len(server.Media()) / 2
	if err := os.WriteFile(part, server.Media()[:offset], 0o644); err != nil {
		t.Fatal(err)
	}
	state, _ := json.Marshal(partialState{URL: server.URL + "/media", ETag: server.MediaETag(), Total: int64(len(server.Media()))})
	if err := os.WriteFile(part+".json", state, 0o600); err != nil {
		t.Fatal(err)
	}
	// Content-Length of the resumed response is the remaining half; the
	// advertised size must include the resume offset (reference +resume_len).
	_, err := New(transport).Download(context.Background(), Job{
		URL: server.URL + "/media", OutputRoot: filepath.Dir(destination), Destination: destination,
		MaxFilesize: int64(len(server.Media())) - 1,
	}, nil)
	var abort *FileSizeAbortError
	if !errors.As(err, &abort) {
		t.Fatalf("want FileSizeAbortError on resumed size, got %v", err)
	}
	if !strings.Contains(abort.Message, fmt.Sprintf("(%d bytes > %d bytes)", len(server.Media()), len(server.Media())-1)) {
		t.Fatalf("resume message = %q", abort.Message)
	}
}

func TestDownloadFilesizeAbortIsNotRetryable(t *testing.T) {
	requests := 0
	body := make([]byte, 2000)
	length := len(body)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		requests++
		writer.Header().Set("Content-Length", fmt.Sprint(length))
		_, _ = writer.Write(body)
	}))
	defer server.Close()
	transport, _ := network.New(network.Config{})
	destination := filepath.Join(t.TempDir(), "media.bin")
	var sinkCalls []string
	sink := events.SinkFunc(func(_ context.Context, event events.Event) error {
		sinkCalls = append(sinkCalls, string(event.Kind))
		return nil
	})
	_, err := New(transport).Download(context.Background(), Job{
		URL: server.URL, OutputRoot: filepath.Dir(destination), Destination: destination,
		Attempts: 5, MaxFilesize: 1000,
	}, sink)
	var abort *FileSizeAbortError
	if !errors.As(err, &abort) {
		t.Fatalf("want FileSizeAbortError, got %v", err)
	}
	if requests != 1 {
		t.Fatalf("abort retried: requests = %d", requests)
	}
	for _, call := range sinkCalls {
		if call == "retry" {
			t.Fatalf("abort triggered a retry event: %v", sinkCalls)
		}
	}
}
