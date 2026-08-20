package engine

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/tejasa97/ytdlp-go/internal/media/ffmpeg"
)

func TestProductTypedFixupSuccessAndWarnAreObservable(t *testing.T) {
	media := generateFixupMPEGTS(t)
	server := serveFixupMedia(t, media, "video/mp2t")
	defer server.Close()

	input, err := os.ReadFile(media)
	if err != nil {
		t.Fatal(err)
	}
	var eventsMu sync.Mutex
	var events []Event
	client := newBroadTestClient(WithEventHandler(func(_ context.Context, event Event) error {
		eventsMu.Lock()
		defer eventsMu.Unlock()
		events = append(events, event)
		return nil
	}))
	root := t.TempDir()
	result, err := client.Run(context.Background(), Request{
		URL: server.URL + "/source.ts", OutputDir: root, OutputTemplate: "fixed.%(ext)s", Overwrite: true,
		FixupPolicy: FixupDetectOrWarn,
	})
	if err != nil || !result.Downloaded {
		t.Fatalf("success result=%+v err=%v", result, err)
	}
	fixed, err := os.ReadFile(result.Filename)
	if err != nil || len(fixed) == 0 {
		t.Fatalf("typed fixup did not publish media: err=%v bytes=%d", err, len(fixed))
	}
	if countEvent(events, EventPostprocessStarting) != 1 || countEvent(events, EventPostprocessCompleted) != 1 {
		t.Fatalf("events=%+v", events)
	}

	warnRoot := t.TempDir()
	warnResult, err := client.Run(context.Background(), Request{
		URL: server.URL + "/source.ts", OutputDir: warnRoot, OutputTemplate: "warn.%(ext)s", Overwrite: true,
		FixupPolicy: FixupWarn,
	})
	if err != nil {
		t.Fatal(err)
	}
	warnBytes, err := os.ReadFile(warnResult.Filename)
	if err != nil || !bytes.Equal(warnBytes, input) {
		t.Fatalf("warn mutated media: err=%v equal=%t", err, bytes.Equal(warnBytes, input))
	}
	if !hasMessage(events, "known media fixup available") {
		t.Fatalf("warn event missing: %+v", events)
	}
	assertFixupEventsSafe(t, events, media, server.URL)
}

func TestProductTypedFixupInspectionUnavailableAndForceClosed(t *testing.T) {
	media := generateFixupM4A(t)
	server := serveFixupMedia(t, media, "audio/m4a")
	defer server.Close()

	root := t.TempDir()
	var events []Event
	client := newBroadTestClient(WithEventHandler(func(_ context.Context, event Event) error {
		events = append(events, event)
		return nil
	}))
	result, err := client.Run(context.Background(), Request{
		URL: server.URL + "/source.m4a", OutputDir: root, OutputTemplate: "unavailable.%(ext)s", Overwrite: true,
		FixupPolicy: FixupDetectOrWarn, Filesystem: FilesystemOptions{FfmpegLocation: filepath.Join(root, "missing-ffmpeg")},
	})
	if err != nil || !result.Downloaded || !hasMessage(events, "ffmpeg is unavailable") {
		t.Fatalf("unavailable result=%+v err=%v events=%+v", result, err, events)
	}

	video := generateFixupMP4(t)
	videoServer := serveFixupMedia(t, video, "video/mp4")
	defer videoServer.Close()
	forceRoot := t.TempDir()
	destination := filepath.Join(forceRoot, "force.mp4")
	sentinel := []byte("original-force-destination")
	if err := os.WriteFile(destination, sentinel, 0o600); err != nil {
		t.Fatal(err)
	}
	archivePath := filepath.Join(forceRoot, "archive.txt")
	archiveSentinel := []byte("archive-before\n")
	if err := os.WriteFile(archivePath, archiveSentinel, 0o600); err != nil {
		t.Fatal(err)
	}
	_, err = client.Run(context.Background(), Request{
		URL: videoServer.URL + "/source.mp4", OutputDir: forceRoot, OutputTemplate: "force.%(ext)s", Overwrite: true,
		DownloadArchive: archivePath, FixupPolicy: FixupForce,
	})
	if !errors.Is(err, ffmpeg.ErrInvalidOperation) {
		t.Fatalf("force error=%v", err)
	}
	restored, readErr := os.ReadFile(destination)
	if readErr != nil || !bytes.Equal(restored, sentinel) {
		t.Fatalf("force destination not restored: err=%v bytes=%q", readErr, restored)
	}
	archive, readErr := os.ReadFile(archivePath)
	if readErr != nil || !bytes.Equal(archive, archiveSentinel) {
		t.Fatalf("force archive changed: err=%v bytes=%q", readErr, archive)
	}
	assertNoFixupTemps(t, forceRoot)
}

func TestProductTypedFixupCancellationRestoresDestinationAndArchive(t *testing.T) {
	media := generateFixupM4A(t)
	server := serveFixupMedia(t, media, "audio/m4a")
	defer server.Close()
	root := t.TempDir()
	destination := filepath.Join(root, "cancelled.m4a")
	sentinel := []byte("original-cancelled-destination")
	if err := os.WriteFile(destination, sentinel, 0o600); err != nil {
		t.Fatal(err)
	}
	archivePath := filepath.Join(root, "archive.txt")
	archiveSentinel := []byte("archive-before\n")
	if err := os.WriteFile(archivePath, archiveSentinel, 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	client := newBroadTestClient(WithEventHandler(func(_ context.Context, event Event) error {
		if event.Kind == EventPostprocessStarting {
			cancel()
		}
		return nil
	}))
	_, err := client.Run(ctx, Request{
		URL: server.URL + "/source.m4a", OutputDir: root, OutputTemplate: "cancelled.%(ext)s", Overwrite: true,
		DownloadArchive: archivePath, FixupPolicy: FixupDetectOrWarn,
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation error=%v", err)
	}
	restored, readErr := os.ReadFile(destination)
	if readErr != nil || !bytes.Equal(restored, sentinel) {
		t.Fatalf("cancelled destination not restored: err=%v bytes=%q", readErr, restored)
	}
	archive, readErr := os.ReadFile(archivePath)
	if readErr != nil || !bytes.Equal(archive, archiveSentinel) {
		t.Fatalf("cancelled archive changed: err=%v bytes=%q", readErr, archive)
	}
	assertNoFixupTemps(t, root)
}

func TestProductTypedFixupFailureRestoresDestinationAndArchive(t *testing.T) {
	media := generateFixupMPEGTS(t)
	server := serveFixupMedia(t, media, "video/mp2t")
	defer server.Close()
	root := t.TempDir()
	destination := filepath.Join(root, "failed.ts")
	sentinel := []byte("original-failed-destination")
	if err := os.WriteFile(destination, sentinel, 0o600); err != nil {
		t.Fatal(err)
	}
	archivePath := filepath.Join(root, "archive.txt")
	archiveSentinel := []byte("archive-before\n")
	if err := os.WriteFile(archivePath, archiveSentinel, 0o600); err != nil {
		t.Fatal(err)
	}
	var lifecycleEvents []Event
	mutated := false
	var mutationErr error
	client := newBroadTestClient(WithEventHandler(func(_ context.Context, event Event) error {
		lifecycleEvents = append(lifecycleEvents, event)
		if event.Kind == EventPostprocessStarting && !mutated {
			mutated = true
			mutationErr = os.WriteFile(event.Path, []byte("invalid media"), 0o600)
		}
		return nil
	}))
	_, err := client.Run(context.Background(), Request{
		URL: server.URL + "/source.ts", OutputDir: root, OutputTemplate: "failed.%(ext)s", Overwrite: true,
		DownloadArchive: archivePath, FixupPolicy: FixupDetectOrWarn,
	})
	if err == nil || !IsCategory(err, ErrorInternal) {
		t.Fatalf("fixup failure error=%v, want internal failure", err)
	}
	if !mutated || mutationErr != nil {
		t.Fatalf("typed fixup did not reach deterministic ffmpeg failure: mutated=%t err=%v", mutated, mutationErr)
	}
	if countEvent(lifecycleEvents, EventPostprocessStarting) != 1 || countEvent(lifecycleEvents, EventPostprocessCompleted) != 0 {
		t.Fatalf("unexpected failure events=%+v", lifecycleEvents)
	}
	assertFixupEventsSafe(t, lifecycleEvents, media, server.URL)
	restored, readErr := os.ReadFile(destination)
	if readErr != nil || !bytes.Equal(restored, sentinel) {
		t.Fatalf("failed fixup destination not restored: err=%v bytes=%q", readErr, restored)
	}
	archive, readErr := os.ReadFile(archivePath)
	if readErr != nil || !bytes.Equal(archive, archiveSentinel) {
		t.Fatalf("failed fixup archive changed: err=%v bytes=%q", readErr, archive)
	}
	assertNoFixupTemps(t, root)
}

func generateFixupM4A(t *testing.T) string {
	t.Helper()
	ffmpegPath, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg unavailable")
	}
	path := filepath.Join(t.TempDir(), "source.m4a")
	if output, err := exec.Command(ffmpegPath, "-nostdin", "-y", "-f", "lavfi", "-i", "sine=frequency=440:duration=0.3", "-c:a", "aac", path).CombinedOutput(); err != nil {
		t.Fatalf("generate m4a: %v: %s", err, output)
	}
	return path
}

func generateFixupMPEGTS(t *testing.T) string {
	t.Helper()
	ffmpegPath, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg unavailable")
	}
	path := filepath.Join(t.TempDir(), "source.ts")
	if output, err := exec.Command(ffmpegPath, "-nostdin", "-y", "-f", "lavfi", "-i", "color=c=black:s=16x16:d=0.3", "-c:v", "mpeg2video", "-f", "mpegts", path).CombinedOutput(); err != nil {
		t.Fatalf("generate mpegts: %v: %s", err, output)
	}
	return path
}

func generateFixupMP4(t *testing.T) string {
	t.Helper()
	ffmpegPath, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg unavailable")
	}
	path := filepath.Join(t.TempDir(), "source.mp4")
	if output, err := exec.Command(ffmpegPath, "-nostdin", "-y", "-f", "lavfi", "-i", "color=c=black:s=16x16:d=0.3", "-c:v", "mpeg4", path).CombinedOutput(); err != nil {
		t.Fatalf("generate mp4: %v: %s", err, output)
	}
	return path
}

func serveFixupMedia(t *testing.T, media, contentType string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/source"+filepath.Ext(media) {
			http.NotFound(writer, request)
			return
		}
		writer.Header().Set("Content-Type", contentType)
		http.ServeFile(writer, request, media)
	}))
}

func hasEvent(events []Event, kind string) bool {
	for _, event := range events {
		if event.Kind == kind {
			return true
		}
	}
	return false
}

func countEvent(events []Event, kind string) int {
	count := 0
	for _, event := range events {
		if event.Kind == kind {
			count++
		}
	}
	return count
}

func hasMessage(events []Event, message string) bool {
	for _, event := range events {
		if strings.Contains(event.Message, message) {
			return true
		}
	}
	return false
}

func assertFixupEventsSafe(t *testing.T, events []Event, forbidden ...string) {
	t.Helper()
	for _, event := range events {
		if strings.ContainsAny(event.Message, "\x00\r\n") || strings.Contains(event.Message, "secret") {
			t.Fatalf("unsafe fixup event=%+v", event)
		}
		for _, value := range forbidden {
			if strings.Contains(event.Message, value) {
				t.Fatalf("fixup event leaked %q: %+v", value, event)
			}
		}
	}
}

func assertNoFixupTemps(t *testing.T, root string) {
	t.Helper()
	for _, pattern := range []string{filepath.Join(root, ".ytdlp-*"), filepath.Join(root, "*.ytdlp-*")} {
		matches, err := filepath.Glob(pattern)
		if err != nil || len(matches) != 0 {
			t.Fatalf("fixup temporary artifacts=%v err=%v", matches, err)
		}
	}
}
