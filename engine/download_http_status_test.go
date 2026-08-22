package engine

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/tejasa97/ytdlp-go/internal/downloader"
	"github.com/tejasa97/ytdlp-go/internal/events"
	"github.com/tejasa97/ytdlp-go/internal/network"
)

func TestDownloadHTTPStatusErrorDistinctFromExtractorHTTPStatusError(t *testing.T) {
	download := &DownloadHTTPStatusError{Code: http.StatusForbidden}
	extractor := &HTTPStatusError{Code: http.StatusForbidden}

	var asDownload *DownloadHTTPStatusError
	if !errors.As(download, &asDownload) || asDownload.Code != http.StatusForbidden {
		t.Fatalf("DownloadHTTPStatusError did not match itself: %#v", asDownload)
	}
	if errors.As(extractor, &asDownload) {
		t.Fatal("extractor HTTPStatusError matched DownloadHTTPStatusError")
	}

	var asExtractor *HTTPStatusError
	if errors.As(download, &asExtractor) {
		t.Fatal("DownloadHTTPStatusError matched extractor HTTPStatusError")
	}

	if code, ok := DownloadHTTPStatusCode(download); !ok || code != http.StatusForbidden {
		t.Fatalf("DownloadHTTPStatusCode(download) = %d, %t; want 403, true", code, ok)
	}
	if code, ok := DownloadHTTPStatusCode(extractor); ok {
		t.Fatalf("DownloadHTTPStatusCode(extractor) = %d, true; want not ok", code)
	}
}

func TestDownloadHTTPStatusCodeSurvivesEngineWrapping(t *testing.T) {
	inner := &DownloadHTTPStatusError{Code: http.StatusForbidden}
	wrapped := categorized("download", fmt.Errorf("multi-track transfer: %w", inner))
	code, ok := DownloadHTTPStatusCode(wrapped)
	if !ok || code != http.StatusForbidden {
		t.Fatalf("DownloadHTTPStatusCode() = %d, %t; want 403, true (err=%v)", code, ok, wrapped)
	}
	if !IsCategory(wrapped, ErrorNetwork) {
		t.Fatalf("category=%v want %s", wrapped, ErrorNetwork)
	}
	if code, ok := DownloadHTTPStatusCode(categorized("extract", &HTTPStatusError{Code: http.StatusForbidden})); ok {
		t.Fatalf("extractor 403 matched download helper: %d", code)
	}
}

func TestDirectDownloadForbiddenIsDownloadHTTPStatusError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		http.Error(writer, "expired", http.StatusForbidden)
	}))
	t.Cleanup(server.Close)

	transport, err := network.New(network.Config{})
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	_, downloadErr := downloader.New(transport).Download(context.Background(), downloader.Job{
		URL: server.URL, OutputRoot: root, Destination: filepath.Join(root, "media.bin"), Overwrite: true,
	}, events.Nop())
	if downloadErr == nil {
		t.Fatal("expected download HTTP 403")
	}

	wrapped := categorized("download", fmt.Errorf("multi-track transfer: %w", downloadErr))
	code, ok := DownloadHTTPStatusCode(wrapped)
	if !ok || code != http.StatusForbidden {
		t.Fatalf("DownloadHTTPStatusCode() = %d, %t; want 403, true (err=%v)", code, ok, wrapped)
	}
	var status *DownloadHTTPStatusError
	if !errors.As(wrapped, &status) || status.Code != http.StatusForbidden {
		t.Fatalf("errors.As DownloadHTTPStatusError failed: %#v err=%v", status, wrapped)
	}
}
