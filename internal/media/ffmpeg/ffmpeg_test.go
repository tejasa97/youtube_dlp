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

func TestBoundedBuffer(t *testing.T) {
	buffer := newBoundedBuffer(4)
	if count, err := buffer.Write([]byte("123456")); err != nil || count != 6 {
		t.Fatalf("Write() = %d, %v", count, err)
	}
	if got := buffer.String(); got != "1234 [truncated]" {
		t.Fatalf("String() = %q", got)
	}
}
