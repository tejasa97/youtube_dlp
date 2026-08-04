package ffmpegdetect

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestProbeFFmpeg(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX paths")
	}
	status := Probe(context.Background(), "")
	if status.Available && status.Version == "" {
		t.Fatalf("Available ffmpeg must report a version")
	}
}

func TestConfigureRejectsInvalidPath(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX paths")
	}
	status := ConfigurePath(context.Background(), "/definitely/not/a/real/path/ffmpeg")
	if status.Available {
		t.Fatalf("ConfigurePath returned Available=true for an invalid path: %+v", status)
	}
	if status.Message == "" {
		t.Fatalf("expected an explanatory message")
	}
}

func TestConfigureUsesSiblingFFprobe(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX test binaries")
	}
	binDir := fakeToolDir(t, true)
	status := ConfigurePath(context.Background(), filepath.Join(binDir, "ffmpeg"))
	if !status.Available {
		t.Fatalf("configured pair should be available: %+v", status)
	}
	if status.Path != filepath.Join(binDir, "ffmpeg") {
		t.Fatalf("ffmpeg path = %q; want configured path", status.Path)
	}
	if status.FFprobePath != filepath.Join(binDir, "ffprobe") {
		t.Fatalf("ffprobe path = %q; want sibling path", status.FFprobePath)
	}
}

func TestConfigureRejectsMissingSiblingFFprobe(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX test binaries")
	}
	binDir := fakeToolDir(t, false)
	status := ConfigurePath(context.Background(), filepath.Join(binDir, "ffmpeg"))
	if status.Available {
		t.Fatalf("missing sibling ffprobe should be unavailable: %+v", status)
	}
	if status.FFprobePath != filepath.Join(binDir, "ffprobe") {
		t.Fatalf("ffprobe path = %q; want sibling path", status.FFprobePath)
	}
	if status.Message == "" {
		t.Fatalf("expected an explanatory message")
	}
}

func TestProbeUsesConfiguredPATHPair(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX test binaries")
	}
	binDir := fakeToolDir(t, true)
	t.Setenv("PATH", binDir)
	status := Probe(context.Background(), "")
	if !status.Available {
		t.Fatalf("PATH pair should be available: %+v", status)
	}
	if status.Path != filepath.Join(binDir, "ffmpeg") || status.FFprobePath != filepath.Join(binDir, "ffprobe") {
		t.Fatalf("resolved tools = %q / %q; want PATH pair", status.Path, status.FFprobePath)
	}
}

func fakeToolDir(t *testing.T, includeFFprobe bool) string {
	t.Helper()
	dir := t.TempDir()
	script := []byte("#!/bin/sh\nif [ \"$1\" = \"-version\" ]; then\n  echo \"ffmpeg version desktop-test\"\nfi\n")
	if err := os.WriteFile(filepath.Join(dir, "ffmpeg"), script, 0o755); err != nil {
		t.Fatal(err)
	}
	if includeFFprobe {
		probeScript := []byte("#!/bin/sh\nif [ \"$1\" = \"-version\" ]; then\n  echo \"ffprobe version desktop-test\"\nfi\n")
		if err := os.WriteFile(filepath.Join(dir, "ffprobe"), probeScript, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}
