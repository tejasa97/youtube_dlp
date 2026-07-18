package ffmpeg

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func requireToolset(t *testing.T) *Toolset {
	t.Helper()
	tools, err := Discover(Config{})
	if err != nil {
		t.Skipf("ffmpeg toolchain unavailable: %v", err)
	}
	return tools
}

func generateAudio(t *testing.T, ctx context.Context, tools *Toolset, destination string) {
	t.Helper()
	if _, err := tools.execute(ctx, tools.ffmpeg, []string{"-nostdin", "-y", "-f", "lavfi", "-i", "sine=frequency=440:duration=0.3", "-c:a", "aac", destination}, nil); err != nil {
		t.Fatalf("generate audio: %v", err)
	}
}

// Generated media keeps this test license-free and stable. It semantically
// exercises the operation forms derived from yt-dlp postprocessor/ffmpeg.py
// at pinned reference aefce1eea4d0b6bab1ec2bd3beff09bff91a39c8.
func TestTypedAudioMetadataConcatOperations(t *testing.T) {
	tools := requireToolset(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	root := t.TempDir()
	input := filepath.Join(root, "input.m4a")
	generateAudio(t, ctx, tools, input)
	converted := filepath.Join(root, "audio.mp3")
	if err := tools.ExtractAudio(ctx, input, converted, AudioOptions{Codec: "libmp3lame", Bitrate: "96k"}, false, nil); err != nil {
		t.Fatal(err)
	}
	probe, err := tools.Probe(ctx, converted)
	if err != nil || len(probe.Streams) != 1 || probe.Streams[0].CodecType != "audio" {
		t.Fatalf("audio probe = %#v, err=%v", probe, err)
	}
	metadata := filepath.Join(root, "metadata.mp3")
	if err := tools.EmbedMetadata(ctx, converted, metadata, Metadata{"title": "Typed media test", "artist": "ytdlp-go"}, false, nil); err != nil {
		t.Fatal(err)
	}
	probe, err = tools.Probe(ctx, metadata)
	if err != nil || probe.Format.Tags["title"] != "Typed media test" {
		t.Fatalf("metadata probe = %#v, err=%v", probe, err)
	}
	concatenated := filepath.Join(root, "concat.mp3")
	if err := tools.Concat(ctx, []string{metadata, metadata}, concatenated, false, nil); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(concatenated); err != nil || info.Size() <= 0 {
		t.Fatalf("concat output: %v", err)
	}
	chaptered := filepath.Join(root, "chaptered.m4a")
	chapters := []Chapter{{Start: 0, End: 150 * time.Millisecond, Title: "Part = one"}, {Start: 150 * time.Millisecond, End: 300 * time.Millisecond, Title: "Part two"}}
	if err := tools.EmbedChapters(ctx, input, chaptered, chapters, false, nil); err != nil {
		t.Fatal(err)
	}
	probe, err = tools.Probe(ctx, chaptered)
	if err != nil || len(probe.Chapters) != 2 || probe.Chapters[0].Tags["title"] != "Part = one" {
		t.Fatalf("chapter probe = %#v, err=%v", probe, err)
	}
}

func TestTypedSubtitleAndImageConversions(t *testing.T) {
	tools := requireToolset(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	root := t.TempDir()
	srt := filepath.Join(root, "caption.srt")
	if err := os.WriteFile(srt, []byte("1\n00:00:00,000 --> 00:00:00,200\nhello\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	vtt := filepath.Join(root, "caption.vtt")
	if err := tools.ConvertSubtitle(ctx, srt, vtt, SubtitleOptions{Format: "webvtt"}, false, nil); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(vtt)
	if err != nil || !strings.Contains(string(body), "WEBVTT") {
		t.Fatalf("vtt = %q, err=%v", body, err)
	}
	imageInput := filepath.Join(root, "input.png")
	if _, err := tools.execute(ctx, tools.ffmpeg, []string{"-nostdin", "-y", "-f", "lavfi", "-i", "color=c=red:s=8x8:d=0.1", "-frames:v", "1", imageInput}, nil); err != nil {
		t.Fatal(err)
	}
	imageOutput := filepath.Join(root, "output.jpg")
	if err := tools.ConvertImage(ctx, imageInput, imageOutput, ImageOptions{Format: "jpg"}, false, nil); err != nil {
		t.Fatal(err)
	}
	probe, err := tools.Probe(ctx, imageOutput)
	if err != nil || len(probe.Streams) != 1 || probe.Streams[0].CodecName != "mjpeg" {
		t.Fatalf("image probe = %#v, err=%v", probe, err)
	}
}

func TestOperationValidationAndAtomicFailure(t *testing.T) {
	tools := requireToolset(t)
	root := t.TempDir()
	if err := tools.ExtractAudio(context.Background(), "ignored", "ignored.mp3", AudioOptions{Codec: "aac;rm"}, false, nil); !errors.Is(err, ErrInvalidOperation) {
		t.Fatalf("unsafe codec: %v", err)
	}
	if err := tools.EmbedMetadata(context.Background(), "ignored", "ignored.mp3", Metadata{"title\nunsafe": "x"}, false, nil); !errors.Is(err, ErrInvalidOperation) {
		t.Fatalf("unsafe metadata: %v", err)
	}
	if err := tools.Concat(context.Background(), nil, "ignored.mp3", false, nil); !errors.Is(err, ErrInvalidOperation) {
		t.Fatalf("empty concat: %v", err)
	}
	if err := tools.EmbedChapters(context.Background(), "ignored", "ignored.mp3", []Chapter{{Start: time.Second, End: time.Millisecond}}, false, nil); !errors.Is(err, ErrInvalidOperation) {
		t.Fatalf("invalid chapter: %v", err)
	}
	if err := tools.Concat(context.Background(), []string{"https://example.test/file.mp4"}, "ignored.mp3", false, nil); !errors.Is(err, ErrInvalidOperation) {
		t.Fatalf("URL concat input: %v", err)
	}
	regular := filepath.Join(root, "regular.mp4")
	if err := os.WriteFile(regular, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	symlink := filepath.Join(root, "link.mp4")
	if err := os.Symlink(regular, symlink); err == nil {
		if err := tools.Concat(context.Background(), []string{symlink}, "ignored.mp3", false, nil); !errors.Is(err, ErrInvalidOperation) {
			t.Fatalf("symlink concat input: %v", err)
		}
	}
	destination := filepath.Join(root, "missing.mp3")
	err := tools.ExtractAudio(context.Background(), filepath.Join(root, "does-not-exist.mp4"), destination, AudioOptions{Codec: "aac"}, false, nil)
	if !errors.Is(err, ErrMediaFailure) {
		t.Fatalf("failure category: %v", err)
	}
	if _, statErr := os.Stat(destination); !os.IsNotExist(statErr) {
		t.Fatalf("partial output exists: %v", statErr)
	}
}

func FuzzOperationInputValidation(f *testing.F) {
	f.Add("aac", "128k", "title", "value")
	f.Fuzz(func(t *testing.T, codec, rate, key, value string) {
		if len(codec)+len(rate)+len(key)+len(value) > 4096 {
			t.Skip()
		}
		_ = safeCodec(codec)
		_ = safeRate(rate)
		_ = validateMetadata(Metadata{key: value})
		_, _ = writeConcatList(t.TempDir()+"/out.mp4", []string{codec, rate})
	})
}
