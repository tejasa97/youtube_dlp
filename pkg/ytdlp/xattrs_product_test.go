package ytdlp

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tejasa97/youtube_dlp/internal/archive"
	platformxattrs "github.com/tejasa97/youtube_dlp/internal/platform/xattrs"
	"github.com/tejasa97/youtube_dlp/internal/value"
)

func TestProductXattrsWritesBoundedMetadataMapping(t *testing.T) {
	if !platformxattrs.Supported() {
		t.Skip("xattrs unsupported on this platform")
	}
	media := generateXattrMedia(t)
	server := serveXattrMedia(t, media)
	defer server.Close()
	root := t.TempDir()
	result, err := NewClient().Run(context.Background(), Request{
		URL: server.URL + "/page", OutputDir: root, OutputTemplate: "fixed.%(ext)s", Overwrite: true,
		Xattrs: true,
	})
	if errors.Is(err, ErrXattrsUnsupported) {
		t.Skipf("filesystem xattrs unavailable: %v", err)
	}
	if err != nil || !result.Downloaded {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	attrs, err := platformxattrs.List(result.Filename)
	if err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]string{
		"user.dublincore.title":       "Xattr Fixture",
		"user.dublincore.date":        "2024-01-02",
		"user.dublincore.contributor": "Uploader",
		"user.dublincore.description": "bounded description",
	} {
		if got := string(attrs[name]); got != want {
			t.Fatalf("xattr %q=%q want %q all=%v", name, got, want, attrs)
		}
	}
}

func TestProductXattrsCancellationRestoresDestinationAndArchive(t *testing.T) {
	if !platformxattrs.Supported() {
		t.Skip("xattrs unsupported on this platform")
	}
	media := generateXattrMedia(t)
	server := serveXattrMedia(t, media)
	defer server.Close()
	root := t.TempDir()
	destination := filepath.Join(root, "cancelled.mp4")
	sentinel := []byte("original destination")
	if err := os.WriteFile(destination, sentinel, 0o600); err != nil {
		t.Fatal(err)
	}
	archivePath := filepath.Join(root, "archive.txt")
	archiveStore, err := archive.Open(context.Background(), archivePath, archive.Options{})
	if err != nil {
		t.Fatal(err)
	}
	seed, err := archive.NewIdentity("seed", "existing")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := archiveStore.Record(context.Background(), seed); err != nil {
		t.Fatal(err)
	}
	archiveBefore, err := os.ReadFile(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	client := NewClient(WithEventHandler(func(_ context.Context, event Event) error {
		if event.Kind == EventPostprocessStarting {
			cancel()
		}
		return nil
	}))
	_, err = client.Run(ctx, Request{
		URL: server.URL + "/page", OutputDir: root, OutputTemplate: "cancelled.%(ext)s", Overwrite: true,
		DownloadArchive: archivePath, Xattrs: true,
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("xattrs cancellation error=%v", err)
	}
	restored, readErr := os.ReadFile(destination)
	if readErr != nil || !bytes.Equal(restored, sentinel) {
		t.Fatalf("destination changed: err=%v bytes=%q", readErr, restored)
	}
	archiveAfter, readErr := os.ReadFile(archivePath)
	if readErr != nil || !bytes.Equal(archiveAfter, archiveBefore) {
		t.Fatalf("archive changed: err=%v before=%q after=%q", readErr, archiveBefore, archiveAfter)
	}
	assertNoPostprocessTemps(t, root)
}

func TestXattrsRejectsBoundedValueAndSymlinkPath(t *testing.T) {
	if _, err := xattrValues(valueInfoWithXattrFields(strings.Repeat("x", maxXattrValueBytes+1))); !errors.Is(err, ErrXattrsUnsupported) {
		t.Fatalf("oversized value error=%v", err)
	}
	if !platformxattrs.Supported() {
		return
	}
	home := t.TempDir()
	outside := t.TempDir()
	link := filepath.Join(home, "linked")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	operation := &operation{client: NewClient(), request: Request{Xattrs: true, OutputDir: home}}
	if err := operation.applyXattrs(context.Background(), valueInfoWithXattrFields("title"), filepath.Join(link, "media.mp4")); !errors.Is(err, ErrXattrsUnsupported) {
		t.Fatalf("symlink path error=%v", err)
	}
}

func generateXattrMedia(t *testing.T) string {
	t.Helper()
	ffmpegPath, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg unavailable")
	}
	path := filepath.Join(t.TempDir(), "source.mp4")
	if output, err := exec.Command(ffmpegPath, "-nostdin", "-y", "-f", "lavfi", "-i", "color=c=black:s=16x16:d=0.3", "-c:v", "mpeg4", path).CombinedOutput(); err != nil {
		t.Fatalf("generate xattr fixture: %v: %s", err, output)
	}
	return path
}

func serveXattrMedia(t *testing.T, media string) *httptest.Server {
	t.Helper()
	content, err := os.ReadFile(media)
	if err != nil {
		t.Fatal(err)
	}
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/page":
			writer.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprintf(writer, `{"id":"xattr-fixture","title":"Xattr Fixture","description":"bounded description","uploader":"Uploader","upload_date":"20240102","formats":[{"format_id":"media","url":%q,"ext":"mp4","vcodec":"mpeg4","acodec":"none"}]}`, server.URL+"/media.mp4")
		case "/media.mp4":
			writer.Header().Set("Content-Type", "video/mp4")
			if request.Method != http.MethodHead {
				_, _ = writer.Write(content)
			}
		default:
			http.NotFound(writer, request)
		}
	}))
	return server
}

func valueInfoWithXattrFields(description string) value.Info {
	return value.NewInfo(value.NewObject(
		value.Field{Key: "title", Value: value.String("title")},
		value.Field{Key: "description", Value: value.String(description)},
	))
}
