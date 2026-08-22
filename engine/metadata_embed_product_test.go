package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/tejasa97/ytdlp-go/internal/events"
	"github.com/tejasa97/ytdlp-go/internal/value"
)

func TestProductEmbedInfoJSONExactCleaningReplacementAndCancellation(t *testing.T) {
	ffmpegPath, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg unavailable")
	}
	fixtureRoot := t.TempDir()
	media := filepath.Join(fixtureRoot, "source.mkv")
	if output, generateErr := exec.Command(ffmpegPath, "-nostdin", "-y", "-f", "lavfi", "-i", "color=c=black:s=16x16:d=0.4", "-c:v", "mpeg4", media).CombinedOutput(); generateErr != nil {
		t.Fatalf("generate mkv: %v: %s", generateErr, output)
	}
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/article":
			writer.Header().Set("Content-Type", "text/html")
			_, _ = fmt.Fprintf(writer, `<meta property="og:title" content="Embedded Info Fixture"><meta property="og:video" content="/source.mkv">`)
		case "/source.mkv":
			writer.Header().Set("Content-Type", "video/x-matroska")
			http.ServeFile(writer, request, media)
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()

	enabled := true
	root := t.TempDir()
	result, err := newBroadTestClient().Run(context.Background(), Request{
		URL: server.URL + "/article", OutputDir: root, Overwrite: true, EmbedInfoJSON: &enabled,
	})
	if err != nil || !result.Downloaded || result.Filename == "" {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	actual := extractInfoJSONAttachment(t, result.Filename)
	var decoded value.Value
	if err := json.Unmarshal(result.InfoJSON, &decoded); err != nil {
		t.Fatal(err)
	}
	object, ok := decoded.Object()
	if !ok {
		t.Fatalf("result info is not an object: %s", result.InfoJSON)
	}
	expected, err := boundedEmbeddedInfoJSON(value.NewInfo(object))
	if err != nil || !bytes.Equal(actual, expected) {
		t.Fatalf("attachment=%s expected=%s err=%v", actual, expected, err)
	}

	replacement := value.NewInfo(object.Clone())
	replacement.Set("title", value.String("Replacement title"))
	operation := &operation{client: newBroadTestClient(), request: Request{EmbedInfoJSON: &enabled, Overwrite: true}}
	if _, err := operation.applyAutomaticMetadataEmbedding(context.Background(), replacement, result.Filename, nil); err != nil {
		t.Fatal(err)
	}
	replacementExpected, err := boundedEmbeddedInfoJSON(replacement)
	if err != nil {
		t.Fatal(err)
	}
	if actual = extractInfoJSONAttachment(t, result.Filename); !bytes.Equal(actual, replacementExpected) {
		t.Fatalf("replacement attachment=%s expected=%s", actual, replacementExpected)
	}

	before, err := os.ReadFile(result.Filename)
	if err != nil {
		t.Fatal(err)
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancelledSink := events.SinkFunc(func(ctx context.Context, event events.Event) error {
		if event.Kind == events.KindPostprocessStarting {
			cancel()
		}
		return nil
	})
	if _, err := operation.applyAutomaticMetadataEmbedding(cancelled, replacement, result.Filename, cancelledSink); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation error=%v", err)
	}
	after, err := os.ReadFile(result.Filename)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("cancellation changed media: err=%v equal=%t", err, bytes.Equal(before, after))
	}
	assertNoPostprocessTemps(t, filepath.Dir(result.Filename))
}

func TestProductEmbedInfoJSONPostStageFailureRestoresDestinationAndArchive(t *testing.T) {
	ffmpegPath, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg unavailable")
	}
	fixtureRoot := t.TempDir()
	media := filepath.Join(fixtureRoot, "source.mkv")
	if output, generateErr := exec.Command(ffmpegPath, "-nostdin", "-y", "-f", "lavfi", "-i", "color=c=black:s=16x16:d=0.4", "-c:v", "mpeg4", media).CombinedOutput(); generateErr != nil {
		t.Fatalf("generate mkv: %v: %s", generateErr, output)
	}
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/article":
			writer.Header().Set("Content-Type", "text/html")
			_, _ = fmt.Fprintf(writer, `<meta property="og:title" content="Rollback Fixture"><meta property="og:video" content="/source.mkv"><meta property="og:image" content="/bad.jpg">`)
		case "/source.mkv":
			writer.Header().Set("Content-Type", "video/x-matroska")
			http.ServeFile(writer, request, media)
		case "/bad.jpg":
			writer.Header().Set("Content-Type", "image/jpeg")
			_, _ = writer.Write([]byte("not an image"))
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()

	root := t.TempDir()
	destination := filepath.Join(root, "Rollback Fixture.mkv")
	sentinel := []byte("pre-existing destination")
	if err := os.WriteFile(destination, sentinel, 0o600); err != nil {
		t.Fatal(err)
	}
	archivePath := filepath.Join(root, "archive.txt")
	archiveSentinel := []byte("already-recorded\n")
	if err := os.WriteFile(archivePath, archiveSentinel, 0o600); err != nil {
		t.Fatal(err)
	}
	enabled := true
	var lifecycleEvents []Event
	var postStageMutationErr error
	infoJSONCompleted := false
	client := newBroadTestClient(WithEventHandler(func(_ context.Context, event Event) error {
		lifecycleEvents = append(lifecycleEvents, event)
		if event.Kind == EventPostprocessCompleted && !infoJSONCompleted {
			infoJSONCompleted = true
			postStageMutationErr = os.WriteFile(event.Path, []byte("post-stage failure"), 0o600)
		}
		return nil
	}))
	rollbackResult, err := client.Run(context.Background(), Request{
		URL: server.URL + "/article", OutputDir: root, OutputTemplate: "Rollback Fixture.%(ext)s", Overwrite: true,
		DownloadArchive: archivePath, EmbedInfoJSON: &enabled,
		Thumbnails: ThumbnailOptions{WriteAll: true, Embed: true},
	})
	if err == nil {
		t.Logf("unexpected success result=%+v events=%+v", rollbackResult, lifecycleEvents)
		t.Fatal("post-stage failure unexpectedly succeeded")
	}
	if postStageMutationErr != nil {
		t.Fatalf("inject post-stage failure after info-json completion: %v", postStageMutationErr)
	}
	starting, completed := 0, 0
	for _, event := range lifecycleEvents {
		if event.Kind == EventPostprocessStarting {
			starting++
		}
		if event.Kind == EventPostprocessCompleted {
			completed++
		}
	}
	if !infoJSONCompleted || starting < 1 || completed != 1 {
		t.Fatalf("info-json stage was not proven complete before later failure: starting=%d completed=%d events=%+v", starting, completed, lifecycleEvents)
	}
	restored, readErr := os.ReadFile(destination)
	if readErr != nil || !bytes.Equal(restored, sentinel) {
		t.Fatalf("destination not restored: err=%v bytes=%q", readErr, restored)
	}
	archive, readErr := os.ReadFile(archivePath)
	if readErr != nil || !bytes.Equal(archive, archiveSentinel) {
		t.Fatalf("archive changed: err=%v bytes=%q", readErr, archive)
	}
	assertNoPostprocessTemps(t, root)
}

func extractInfoJSONAttachment(t *testing.T, media string) []byte {
	t.Helper()
	ffmpegPath, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg unavailable")
	}
	destination := filepath.Join(t.TempDir(), "info.json")
	output, err := exec.Command(ffmpegPath, "-nostdin", "-y", "-loglevel", "error", "-dump_attachment:t:0", destination, "-i", media, "-f", "null", "-").CombinedOutput()
	if err != nil {
		t.Fatalf("extract attachment: %v: %s", err, output)
	}
	payload, err := os.ReadFile(destination)
	if err != nil {
		t.Fatal(err)
	}
	return payload
}

func assertNoPostprocessTemps(t *testing.T, root string) {
	t.Helper()
	for _, pattern := range []string{
		filepath.Join(root, ".ytdlp-*"),
		filepath.Join(root, "*.ytdlp-*"),
	} {
		matches, err := filepath.Glob(pattern)
		if err != nil || len(matches) != 0 {
			t.Fatalf("postprocess temporary artifacts=%v err=%v", matches, err)
		}
	}
}
