package ffmpeg

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ytdlp-go/ytdlp/internal/events"
)

func TestDiscoverVersionsProbeAndMerge(t *testing.T) {
	tools, err := Discover(Config{})
	if errors.Is(err, ErrFFmpegUnavailable) || errors.Is(err, ErrFFprobeUnavailable) {
		t.Skipf("ffmpeg toolchain unavailable: %v", err)
	}
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	versions, err := tools.Versions(ctx)
	if err != nil || !strings.HasPrefix(versions.FFmpeg, "ffmpeg version") || !strings.HasPrefix(versions.FFprobe, "ffprobe version") {
		t.Fatalf("versions = %#v, error = %v", versions, err)
	}

	root := t.TempDir()
	video := filepath.Join(root, "video.mp4")
	audio := filepath.Join(root, "audio.m4a")
	if _, err := tools.execute(ctx, tools.ffmpeg, []string{
		"-nostdin", "-y", "-f", "lavfi", "-i", "color=c=black:s=16x16:d=0.2",
		"-an", "-c:v", "mpeg4", "-q:v", "5", video,
	}, nil); err != nil {
		t.Fatalf("generate video: %v", err)
	}
	if _, err := tools.execute(ctx, tools.ffmpeg, []string{
		"-nostdin", "-y", "-f", "lavfi", "-i", "sine=frequency=1000:duration=0.2",
		"-vn", "-c:a", "aac", audio,
	}, nil); err != nil {
		t.Fatalf("generate audio: %v", err)
	}
	destination := filepath.Join(root, "merged.mp4")
	var kinds []events.Kind
	sink := events.SinkFunc(func(_ context.Context, event events.Event) error {
		kinds = append(kinds, event.Kind)
		return nil
	})
	if err := tools.Merge(ctx, video, audio, destination, false, sink); err != nil {
		t.Fatal(err)
	}
	probe, err := tools.Probe(ctx, destination)
	if err != nil {
		t.Fatal(err)
	}
	streamTypes := make(map[string]bool)
	for _, stream := range probe.Streams {
		streamTypes[stream.CodecType] = true
	}
	if !streamTypes["video"] || !streamTypes["audio"] {
		t.Fatalf("merged streams = %#v", probe.Streams)
	}
	if len(kinds) < 3 || kinds[0] != events.KindPostprocessStarting || kinds[len(kinds)-1] != events.KindPostprocessCompleted {
		t.Fatalf("events = %v", kinds)
	}
	if info, err := os.Stat(destination); err != nil || info.Size() == 0 {
		t.Fatalf("merged output missing: %v", err)
	}
	remuxed := filepath.Join(root, "remuxed.mkv")
	if err := tools.Remux(ctx, destination, remuxed, false, nil); err != nil {
		t.Fatal(err)
	}
	remuxProbe, err := tools.Probe(ctx, remuxed)
	if err != nil {
		t.Fatal(err)
	}
	if len(remuxProbe.Streams) != len(probe.Streams) || remuxProbe.Format.FormatName == "" {
		t.Fatalf("remux probe = %#v", remuxProbe)
	}
}

func TestCommandCancellation(t *testing.T) {
	tools, err := Discover(Config{})
	if err != nil {
		t.Skipf("ffmpeg unavailable: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	started := time.Now()
	_, err = tools.execute(ctx, tools.ffmpeg, []string{
		"-nostdin", "-f", "lavfi", "-i", "testsrc=size=16x16:rate=30", "-f", "null", "-",
	}, nil)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("execute() error = %v", err)
	}
	if elapsed := time.Since(started); elapsed > 3*time.Second {
		t.Fatalf("cancellation took %v", elapsed)
	}
}

func TestDiscoverRejectsMissingConfiguredTool(t *testing.T) {
	_, err := Discover(Config{FFmpegPath: filepath.Join(t.TempDir(), "missing")})
	if !errors.Is(err, ErrFFmpegUnavailable) {
		t.Fatalf("Discover() error = %v", err)
	}
}

func TestRemuxCategorizesFailureAndDestinationConflict(t *testing.T) {
	tools, err := Discover(Config{})
	if err != nil {
		t.Skipf("ffmpeg unavailable: %v", err)
	}
	root := t.TempDir()
	destination := filepath.Join(root, "output.mkv")
	err = tools.Remux(context.Background(), filepath.Join(root, "missing.mp4"), destination, false, nil)
	if !errors.Is(err, ErrMediaFailure) {
		t.Fatalf("Remux() error = %v", err)
	}
	if writeErr := os.WriteFile(destination, []byte("existing"), 0o600); writeErr != nil {
		t.Fatal(writeErr)
	}
	err = tools.Remux(context.Background(), filepath.Join(root, "missing.mp4"), destination, false, nil)
	if !errors.Is(err, ErrDestinationExists) {
		t.Fatalf("Remux() conflict error = %v", err)
	}
}

func TestAtomicPostprocessCancellationRemovesTemporaryOutput(t *testing.T) {
	tools, err := Discover(Config{})
	if err != nil {
		t.Skipf("ffmpeg unavailable: %v", err)
	}
	root := t.TempDir()
	destination := filepath.Join(root, "cancelled.mp4")
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	err = tools.runAtomic(ctx, destination, false, nil, func(temporary string) []string {
		return []string{
			"-f", "lavfi", "-i", "testsrc=size=16x16:rate=30",
			"-c:v", "mpeg4", "-progress", "pipe:1", "-nostats", temporary,
		}
	})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("runAtomic() error = %v", err)
	}
	paths := []string{destination}
	parts, globErr := filepath.Glob(filepath.Join(root, ".ytdlp-part-*"))
	if globErr != nil {
		t.Fatal(globErr)
	}
	paths = append(paths, parts...)
	for _, path := range paths {
		if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
			t.Fatalf("temporary output remains at %s: %v", path, statErr)
		}
	}
}

func TestDiagnosticRedaction(t *testing.T) {
	input := "https://example.test/media?token=secret&visible=yes signature=hunter2 authorization=Bearer"
	got := redactDiagnostic(input)
	for _, secret := range []string{"secret", "hunter2", "Bearer"} {
		if strings.Contains(got, secret) {
			t.Fatalf("redacted diagnostic contains %q: %q", secret, got)
		}
	}
	if !strings.Contains(got, "visible=yes") {
		t.Fatalf("non-sensitive query was removed: %q", got)
	}
}

func TestBoundedBuffer(t *testing.T) {
	buffer := newBoundedBuffer(4)
	if count, err := buffer.Write([]byte("123456")); err != nil || count != 6 {
		t.Fatalf("Write() = %d, %v", count, err)
	}
	if got := buffer.String(); got != "1234 [truncated]" {
		t.Fatalf("String() = %q", got)
	}
}

func FuzzRedactDiagnostic(f *testing.F) {
	f.Add("https://example.test/video?token=secret&quality=best")
	f.Fuzz(func(t *testing.T, input string) {
		if len(input) > 1<<20 {
			t.Skip()
		}
		_ = redactDiagnostic(input)
	})
}
