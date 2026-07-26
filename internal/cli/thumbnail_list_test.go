package cli

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

func TestThumbnailModeFlagPrecedence(t *testing.T) {
	t.Parallel()
	var mode thumbnailModeFlag
	if err := mode.setAll("true"); err != nil {
		t.Fatal(err)
	}
	if err := mode.setBest("true"); err != nil || mode != thumbnailModeAll {
		t.Fatalf("best downgraded all: mode=%d err=%v", mode, err)
	}
	if err := mode.clear("true"); err != nil || mode != thumbnailModeNone {
		t.Fatalf("clear: mode=%d err=%v", mode, err)
	}
	if err := mode.setBest("true"); err != nil || mode != thumbnailModeBest {
		t.Fatalf("best: mode=%d err=%v", mode, err)
	}
	if err := mode.setAll("false"); err != nil || mode != thumbnailModeNone {
		t.Fatalf("false all: mode=%d err=%v", mode, err)
	}
	if err := mode.setBest("not-bool"); err == nil {
		t.Fatal("invalid boolean accepted")
	}
}

func TestRenderThumbnailListing(t *testing.T) {
	t.Parallel()
	table, status, err := renderThumbnailListing([]byte(`{
		"id":"fixture",
		"thumbnails":[
			{"id":"small","width":120,"height":90,"url":"https://cdn.example.test/small.jpg"},
			{"id":"large","url":"https://cdn.example.test/large.webp"}
		]
	}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(status, "Available thumbnails for fixture") ||
		!strings.Contains(table, "ID") || !strings.Contains(table, "small") ||
		!strings.Contains(table, "unknown") || !strings.Contains(table, "large.webp") {
		t.Fatalf("status=%q table=%q", status, table)
	}
	if _, _, err := renderThumbnailListing([]byte(`{"id":`)); err == nil {
		t.Fatal("malformed thumbnail listing accepted")
	}
}

func TestRunWritesAndListsThumbnailSidecars(t *testing.T) {
	var imageRequests atomic.Int32
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/article":
			writer.Header().Set("Content-Type", "text/html")
			_, _ = fmt.Fprintf(writer, `<html><head>
<meta property="og:title" content="Fixture">
<meta property="og:video" content="%s/media.mp4">
<meta property="og:image" content="%s/cover.png">
</head></html>`, server.URL, server.URL)
		case "/cover.png":
			imageRequests.Add(1)
			_, _ = writer.Write([]byte("image"))
		case "/media.mp4":
			_, _ = writer.Write([]byte("media"))
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()

	root := t.TempDir()
	var stdout, stderr bytes.Buffer
	code := Run([]string{
		"--skip-download", "--write-thumbnail", "--output-dir", root,
		"--output", "thumbnail:images/%(id)s.%(ext)s", server.URL + "/article",
	}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("write code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	matches, err := filepath.Glob(filepath.Join(root, "images", "*.png"))
	if err != nil || len(matches) != 1 {
		t.Fatalf("thumbnail matches=%v error=%v", matches, err)
	}

	imageRequests.Store(0)
	stdout.Reset()
	stderr.Reset()
	listRoot := t.TempDir()
	code = Run([]string{
		"--list-thumbnails", "--output-dir", listRoot, server.URL + "/article",
	}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("list code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	if !strings.Contains(stdout.String(), server.URL+"/cover.png") ||
		!strings.Contains(stderr.String(), "Available thumbnails") {
		t.Fatalf("list stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
	if imageRequests.Load() != 0 {
		t.Fatalf("listing downloaded image %d times", imageRequests.Load())
	}
	if entries, err := os.ReadDir(listRoot); err != nil || len(entries) != 0 {
		t.Fatalf("listing wrote files: %v, %v", entries, err)
	}
}

func TestRunNoWriteThumbnailOverridesConfiguration(t *testing.T) {
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/article":
			writer.Header().Set("Content-Type", "text/html")
			_, _ = fmt.Fprintf(writer, `<meta property="og:title" content="Fixture">
<meta property="og:video" content="%s/media.mp4">
<meta property="og:image" content="%s/cover.jpg">`, server.URL, server.URL)
		case "/cover.jpg":
			_, _ = writer.Write([]byte("image"))
		case "/media.mp4":
			_, _ = writer.Write([]byte("media"))
		}
	}))
	defer server.Close()
	root := t.TempDir()
	configPath := filepath.Join(t.TempDir(), "yt-dlp.conf")
	if err := os.WriteFile(configPath, []byte("--skip-download\n--write-all-thumbnails\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	code := Run([]string{
		"--config-location", configPath, "--no-write-thumbnail", "--output-dir", root, server.URL + "/article",
	}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	if entries, err := os.ReadDir(root); err != nil || len(entries) != 0 {
		t.Fatalf("disabled thumbnail wrote files: %v, %v", entries, err)
	}
}
