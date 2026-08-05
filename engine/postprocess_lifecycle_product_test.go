package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tejasa97/youtube_dlp/internal/events"
	"github.com/tejasa97/youtube_dlp/internal/media/ffmpeg"
)

func postprocessFixtureMedia(t *testing.T) []byte {
	t.Helper()
	ffmpegPath, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg unavailable")
	}
	root := t.TempDir()
	path := filepath.Join(root, "fixture.mp4")
	output, err := exec.Command(
		ffmpegPath, "-nostdin", "-y", "-f", "lavfi", "-i", "color=c=black:s=32x32:d=0.4",
		"-an", "-c:v", "mpeg4", "-pix_fmt", "yuv420p", path,
	).CombinedOutput()
	if err != nil {
		t.Fatalf("generate postprocess fixture: %v: %s", err, output)
	}
	media, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return media
}

func copyPostprocessFixture(t *testing.T, root string, media []byte) string {
	t.Helper()
	path := filepath.Join(root, "source.mp4")
	if err := os.WriteFile(path, media, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestPostprocessLifecycleSuccessAndKeepVideo(t *testing.T) {
	media := postprocessFixtureMedia(t)
	for _, test := range []struct {
		name      string
		keepVideo bool
	}{
		{name: "cleanup", keepVideo: false},
		{name: "keep", keepVideo: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			input := copyPostprocessFixture(t, root, media)
			operation := &operation{client: newBroadTestClient(), request: Request{
				OutputDir: root, KeepVideo: test.keepVideo,
				Postprocessors: []Postprocessor{{Remux: &RemuxPostprocessor{
					Destination: "success.mkv", Format: "mkv",
				}}},
			}}
			next, artifacts, err := operation.applyPostprocessors(t.Context(), root, input, nil)
			if err != nil {
				t.Fatal(err)
			}
			if next != filepath.Join(root, "success.mkv") || len(artifacts) != 1 {
				t.Fatalf("next=%q artifacts=%#v", next, artifacts)
			}
			if _, err := os.Stat(next); err != nil {
				t.Fatalf("successor missing: %v", err)
			}
			_, inputErr := os.Stat(input)
			if test.keepVideo && inputErr != nil {
				t.Fatalf("kept source missing: %v", inputErr)
			}
			if !test.keepVideo && !errors.Is(inputErr, os.ErrNotExist) {
				t.Fatalf("source cleanup error=%v", inputErr)
			}
		})
	}
}

func TestPostprocessLifecycleFailureRollsBackIntermediateCleanup(t *testing.T) {
	media := postprocessFixtureMedia(t)
	root := t.TempDir()
	input := copyPostprocessFixture(t, root, media)
	operation := &operation{client: newBroadTestClient(), request: Request{
		OutputDir: root, Postprocessors: []Postprocessor{
			{Remux: &RemuxPostprocessor{Destination: "intermediate.mkv", Format: "mkv"}},
			{RecodeVideo: &RecodeVideoPostprocessor{Destination: "never-created.mp4", Format: "jpg"}},
		},
	}}
	tx := newMediaTransaction()
	ctx := withMediaTransaction(t.Context(), tx)
	if err := tx.protectPath(filepath.Join(root, "intermediate.mkv"), true); err != nil {
		t.Fatal(err)
	}
	if _, _, err := operation.applyPostprocessors(ctx, root, input, nil); err == nil {
		t.Fatal("expected postprocessor failure")
	}
	if err := tx.rollback(); err != nil {
		t.Fatal(err)
	}
	if body, err := os.ReadFile(input); err != nil || len(body) != len(media) {
		t.Fatalf("source after rollback: bytes=%d err=%v", len(body), err)
	}
	if _, err := os.Stat(filepath.Join(root, "intermediate.mkv")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("intermediate after rollback: %v", err)
	}
}

func TestPostprocessLifecycleCancellationRollsBackCommittedSuccessor(t *testing.T) {
	media := postprocessFixtureMedia(t)
	root := t.TempDir()
	input := copyPostprocessFixture(t, root, media)
	operation := &operation{client: newBroadTestClient(), request: Request{
		OutputDir: root, Postprocessors: []Postprocessor{{Remux: &RemuxPostprocessor{
			Destination: "cancelled.mkv", Format: "mkv",
		}}},
	}}
	tx := newMediaTransaction()
	ctx, cancel := context.WithCancel(withMediaTransaction(t.Context(), tx))
	defer cancel()
	sink := events.SinkFunc(func(_ context.Context, event events.Event) error {
		if event.Kind == events.KindPostprocessCompleted && strings.HasSuffix(event.Path, "cancelled.mkv") {
			cancel()
		}
		return nil
	})
	if _, _, err := operation.applyPostprocessors(ctx, root, input, sink); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation error=%v", err)
	}
	if err := tx.rollback(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(input); err != nil {
		t.Fatalf("source after cancellation rollback: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "cancelled.mkv")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("successor after cancellation rollback: %v", err)
	}
}

func TestPostprocessLifecyclePostOverwriteRetry(t *testing.T) {
	media := postprocessFixtureMedia(t)
	root := t.TempDir()
	destination := filepath.Join(root, "retry.mkv")
	if err := os.WriteFile(destination, []byte("old postprocessed output"), 0o600); err != nil {
		t.Fatal(err)
	}
	input := copyPostprocessFixture(t, root, media)
	noPostOverwrites := false
	operation := &operation{client: newBroadTestClient(), request: Request{
		OutputDir: root, Overwrite: false, PostOverwrites: &noPostOverwrites,
		Postprocessors: []Postprocessor{{Remux: &RemuxPostprocessor{
			Destination: "retry.mkv", Format: "mkv",
		}}},
	}}
	if _, _, err := operation.applyPostprocessors(t.Context(), root, input, nil); !errors.Is(err, ffmpeg.ErrDestinationExists) {
		t.Fatalf("no-post-overwrites error=%v", err)
	}
	if body, err := os.ReadFile(destination); err != nil || string(body) != "old postprocessed output" {
		t.Fatalf("existing output changed: %q err=%v", body, err)
	}
	if _, err := os.Stat(input); err != nil {
		t.Fatalf("source removed after rejected overwrite: %v", err)
	}
	postOverwrites := true
	operation.request.PostOverwrites = &postOverwrites
	if _, _, err := operation.applyPostprocessors(t.Context(), root, input, nil); err != nil {
		t.Fatalf("retry with post-overwrites: %v", err)
	}
	if _, err := os.Stat(input); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("source after successful retry: %v", err)
	}
	if body, err := os.ReadFile(destination); err != nil || string(body) == "old postprocessed output" {
		t.Fatalf("retry did not replace destination: %q err=%v", body, err)
	}
}

func newPostprocessMultiOutputServer(t *testing.T, media []byte) *httptest.Server {
	t.Helper()
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/page":
			writer.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(writer).Encode(map[string]any{
				"id": "postprocess-multi", "title": "Postprocess multi",
				"ext": "mp4", "formats": []map[string]any{
					{"format_id": "one", "url": server.URL + "/one.mp4", "ext": "mp4", "vcodec": "mpeg4", "acodec": "none"},
					{"format_id": "two", "url": server.URL + "/two.mp4", "ext": "mp4", "vcodec": "mpeg4", "acodec": "none"},
				},
			})
		case "/one.mp4", "/two.mp4":
			writer.Header().Set("Content-Type", "video/mp4")
			writer.Header().Set("Content-Length", fmt.Sprint(len(media)))
			if request.Method != http.MethodHead {
				_, _ = writer.Write(media)
			}
		default:
			http.NotFound(writer, request)
		}
	}))
	t.Cleanup(server.Close)
	return server
}

func TestPostprocessLifecycleMultiOutputNoPartAndCleanup(t *testing.T) {
	server := newPostprocessMultiOutputServer(t, postprocessFixtureMedia(t))
	root := t.TempDir()
	result, err := newBroadTestClient().Run(t.Context(), Request{
		URL: server.URL + "/page", OutputDir: root, OutputTemplate: "%(format_id)s.%(ext)s",
		Format: "one,two", Overwrite: true,
		Filesystem:     FilesystemOptions{NoPart: true},
		Postprocessors: []Postprocessor{{Remux: &RemuxPostprocessor{Format: "mkv"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Downloaded || len(result.Artifacts) != 2 {
		t.Fatalf("result=%+v", result)
	}
	for _, name := range []string{"one.mkv", "two.mkv"} {
		if _, err := os.Stat(filepath.Join(root, name)); err != nil {
			t.Fatalf("final output %s: %v", name, err)
		}
	}
	for _, name := range []string{"one.mp4", "two.mp4", "one.mp4.part", "two.mp4.part"} {
		if _, err := os.Stat(filepath.Join(root, name)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("intermediate %s remains: %v", name, err)
		}
	}
}

func TestPostprocessLifecycleSimulationAndSkipDoNotCreateMedia(t *testing.T) {
	server := newPostprocessMultiOutputServer(t, postprocessFixtureMedia(t))
	for _, test := range []struct {
		name string
		set  func(*Request)
	}{
		{name: "simulate", set: func(request *Request) { request.Simulate = true }},
		{name: "skip-download", set: func(request *Request) { request.SkipDownload = true }},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			request := Request{
				URL: server.URL + "/page", OutputDir: root, Format: "one", Overwrite: true,
				Postprocessors: []Postprocessor{{Remux: &RemuxPostprocessor{Format: "mkv"}}},
			}
			test.set(&request)
			if _, err := newBroadTestClient().Run(t.Context(), request); err != nil {
				t.Fatal(err)
			}
			entries, err := os.ReadDir(root)
			if err != nil {
				t.Fatal(err)
			}
			if len(entries) != 0 {
				t.Fatalf("artifacts in %s mode: %v", test.name, entries)
			}
		})
	}
}

func TestPostprocessLifecycleMultiOutputCancellationRollsBackAllOutputs(t *testing.T) {
	server := newPostprocessMultiOutputServer(t, postprocessFixtureMedia(t))
	root := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	client := newBroadTestClient(WithEventHandler(func(_ context.Context, event Event) error {
		if event.Kind == EventPostprocessCompleted {
			cancel()
		}
		return nil
	}))
	_, err := client.Run(ctx, Request{
		URL: server.URL + "/page", OutputDir: root, OutputTemplate: "%(format_id)s.%(ext)s",
		Format: "one,two", Overwrite: true,
		Postprocessors: []Postprocessor{{Remux: &RemuxPostprocessor{Format: "mkv"}}},
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation error=%v", err)
	}
	entries, readErr := os.ReadDir(root)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if len(entries) != 0 {
		t.Fatalf("cancellation left artifacts: %v", entries)
	}
}
