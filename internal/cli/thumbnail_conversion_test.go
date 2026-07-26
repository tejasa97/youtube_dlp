package cli

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
)

func TestRunConvertsThumbnail(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test ffmpeg shim is a POSIX shell script")
	}
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/article":
			writer.Header().Set("Content-Type", "text/html")
			_, _ = fmt.Fprintf(writer, `<meta property="og:title" content="Fixture">
<meta property="og:video" content="%s/media.mp4">
<meta property="og:image" content="%s/cover.webp">`, server.URL, server.URL)
		case "/cover.webp":
			_, _ = writer.Write([]byte("image"))
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()
	root := t.TempDir()
	bin := t.TempDir()
	installFakeFFmpeg(t, bin)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	var stdout, stderr bytes.Buffer
	code := Run([]string{
		"--skip-download", "--write-thumbnail", "--convert-thumbnails", "png",
		"--output-dir", root, "-o", "%(id)s.%(ext)s", server.URL + "/article",
	}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || filepath.Ext(entries[0].Name()) != ".png" {
		t.Fatalf("entries = %#v", entries)
	}
	args, err := os.ReadFile(filepath.Join(bin, "ffmpeg.args"))
	if err != nil {
		t.Fatal(err)
	}
	if got := string(args); !strings.Contains(got, "-c:v png") || strings.ContainsAny(got, ";\n\r") {
		t.Fatalf("ffmpeg arguments = %q", got)
	}
	if _, err := os.Stat(filepath.Join(root, strings.TrimSuffix(entries[0].Name(), ".png")+".webp")); !os.IsNotExist(err) {
		t.Fatalf("source remains: %v", err)
	}
}

func TestRunThumbnailConversionConfigPrecedenceAndReset(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test ffmpeg shim is a POSIX shell script")
	}
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/article":
			writer.Header().Set("Content-Type", "text/html")
			_, _ = fmt.Fprintf(writer, `<meta property="og:title" content="Fixture">
<meta property="og:video" content="%s/media.mp4">
<meta property="og:image" content="%s/cover.webp">`, server.URL, server.URL)
		case "/cover.webp":
			_, _ = writer.Write([]byte("image"))
		}
	}))
	defer server.Close()
	configPath := filepath.Join(t.TempDir(), "yt-dlp.conf")
	if err := os.WriteFile(configPath, []byte("--skip-download\n--write-thumbnail\n--convert-thumbnails webp>png\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	installFakeFFmpeg(t, bin)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	for _, test := range []struct {
		name, override, extension string
	}{
		{"command wins", "jpg", ".jpg"},
		{"none resets", "none", ".webp"},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			var stdout, stderr bytes.Buffer
			code := Run([]string{
				"--config-location", configPath, "--convert-thumbnails", test.override,
				"--output-dir", root, "-o", "%(id)s.%(ext)s", server.URL + "/article",
			}, &stdout, &stderr)
			if code != 0 {
				t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
			}
			entries, err := os.ReadDir(root)
			if err != nil || len(entries) != 1 || filepath.Ext(entries[0].Name()) != test.extension {
				t.Fatalf("entries=%#v error=%v", entries, err)
			}
		})
	}
}

func TestRunRejectsInvalidThumbnailConversionBeforeNetwork(t *testing.T) {
	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		requests.Add(1)
	}))
	defer server.Close()
	var stdout, stderr bytes.Buffer
	code := Run([]string{
		"--write-thumbnail", "--convert-thumbnails", "gif", server.URL,
	}, &stdout, &stderr)
	if code != 2 || requests.Load() != 0 || !strings.Contains(stderr.String(), "invalid_input") {
		t.Fatalf("code=%d requests=%d stdout=%q stderr=%q", code, requests.Load(), stdout.String(), stderr.String())
	}
}
