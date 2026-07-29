package ytdlp

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/ytdlp-go/ytdlp/internal/value"
)

func TestSubtitleConvertRollbackRestoresOverwrittenDestination(t *testing.T) {
	ffmpegPath, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg unavailable")
	}
	if _, err := exec.Command(ffmpegPath, "-version").CombinedOutput(); err != nil {
		t.Skip("ffmpeg unavailable")
	}

	root := t.TempDir()
	source := filepath.Join(root, "caption.en.srt")
	destination := filepath.Join(root, "caption.en.vtt")
	if err := os.WriteFile(source, []byte("1\n00:00:00,000 --> 00:00:00,200\nSource\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(destination, []byte("WEBVTT\n\nold\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	tx := newMediaTransaction()
	operation := &operation{
		request: Request{
			OutputDir: root, Overwrite: true,
			Subtitles: SubtitleOptions{ConvertFormat: "webvtt"},
		},
	}
	metadata := value.NewObject(
		value.Field{Key: "filepath", Value: value.String(source)},
		value.Field{Key: "ext", Value: value.String("srt")},
	)
	tracks := []subtitleTrack{{language: "en", extension: "srt", metadata: metadata}}
	artifacts := []Artifact{{Path: source, Kind: "subtitle"}}

	tracks, artifacts, converted, err := operation.convertSelectedSubtitles(
		withMediaTransaction(context.Background(), tx), tracks, artifacts, nil,
	)
	if err != nil || !converted {
		t.Fatalf("convert = %v converted=%v", err, converted)
	}
	if err := tx.rollback(); err != nil {
		t.Fatal(err)
	}
	destBody, err := os.ReadFile(destination)
	if err != nil || string(destBody) != "WEBVTT\n\nold\n" {
		t.Fatalf("destination restored = %q, %v", destBody, err)
	}
	sourceBody, err := os.ReadFile(source)
	if err != nil || string(sourceBody) != "1\n00:00:00,000 --> 00:00:00,200\nSource\n" {
		t.Fatalf("source restored = %q, %v", sourceBody, err)
	}
}

func TestSubtitleConvertRollbackRemovesNewDestination(t *testing.T) {
	ffmpegPath, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg unavailable")
	}
	if _, err := exec.Command(ffmpegPath, "-version").CombinedOutput(); err != nil {
		t.Skip("ffmpeg unavailable")
	}

	root := t.TempDir()
	source := filepath.Join(root, "caption.en.srt")
	if err := os.WriteFile(source, []byte("1\n00:00:00,000 --> 00:00:00,200\nSource\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(root, "caption.en.vtt")

	tx := newMediaTransaction()
	operation := &operation{
		request: Request{
			OutputDir: root, Overwrite: true,
			Subtitles: SubtitleOptions{ConvertFormat: "webvtt"},
		},
	}
	metadata := value.NewObject(
		value.Field{Key: "filepath", Value: value.String(source)},
		value.Field{Key: "ext", Value: value.String("srt")},
	)
	tracks := []subtitleTrack{{language: "en", extension: "srt", metadata: metadata}}
	artifacts := []Artifact{{Path: source, Kind: "subtitle"}}

	_, _, converted, err := operation.convertSelectedSubtitles(
		withMediaTransaction(context.Background(), tx), tracks, artifacts, nil,
	)
	if err != nil || !converted {
		t.Fatalf("convert = %v converted=%v", err, converted)
	}
	if err := tx.rollback(); err != nil {
		t.Fatal(err)
	}
	if _, statErr := os.Stat(destination); statErr == nil {
		t.Fatal("new converted destination should be removed")
	}
	sourceBody, err := os.ReadFile(source)
	if err != nil || string(sourceBody) != "1\n00:00:00,000 --> 00:00:00,200\nSource\n" {
		t.Fatalf("source restored = %q, %v", sourceBody, err)
	}
}
