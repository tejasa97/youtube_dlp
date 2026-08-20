package engine

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/tejasa97/ytdlp-go/internal/media/ffmpeg"
	"github.com/tejasa97/ytdlp-go/internal/value"
)

func TestMetadataModificationTimePrefersTimestamp(t *testing.T) {
	info := value.NewInfo(value.NewObject(
		value.Field{Key: "timestamp", Value: value.Int(1_700_000_000)},
		value.Field{Key: "upload_date", Value: value.String("20240101")},
	))
	got, ok := metadataModificationTime(info)
	if !ok || !got.Equal(time.Unix(1_700_000_000, 0).UTC()) {
		t.Fatalf("mtime = %v, ok = %v", got, ok)
	}
}

func TestApplyOutputMtimeUsesUploadDate(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "fixture.bin")
	if err := os.WriteFile(path, []byte("fixture"), 0o644); err != nil {
		t.Fatal(err)
	}
	operation := &operation{request: Request{Filesystem: FilesystemOptions{}}}
	info := value.NewInfo(value.NewObject(value.Field{Key: "upload_date", Value: value.String("20240102")}))
	if err := operation.applyOutputMtime(path, info); err != nil {
		t.Fatal(err)
	}
	stat, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	want := time.Date(2024, 1, 2, 0, 0, 0, 0, time.UTC)
	if !stat.ModTime().UTC().Equal(want) {
		t.Fatalf("mtime = %s; want %s", stat.ModTime().UTC(), want)
	}
}

func TestApplyOutputMtimeHonorsNoMtime(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "fixture.bin")
	before := time.Date(2020, 5, 1, 12, 0, 0, 0, time.UTC)
	if err := os.WriteFile(path, []byte("fixture"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, before, before); err != nil {
		t.Fatal(err)
	}
	operation := &operation{request: Request{Filesystem: FilesystemOptions{NoMtime: true}}}
	info := value.NewInfo(value.NewObject(value.Field{Key: "timestamp", Value: value.Int(1_700_000_000)}))
	if err := operation.applyOutputMtime(path, info); err != nil {
		t.Fatal(err)
	}
	stat, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !stat.ModTime().UTC().Equal(before) {
		t.Fatalf("mtime = %s; want unchanged %s", stat.ModTime().UTC(), before)
	}
}

func TestResolveConfiguredLocationDirectory(t *testing.T) {
	root := t.TempDir()
	ffmpegPath := filepath.Join(root, "ffmpeg")
	if err := os.WriteFile(ffmpegPath, []byte("stub"), 0o755); err != nil {
		t.Fatal(err)
	}
	gotFFmpeg, gotProbe := ffmpeg.ResolveConfiguredLocation(root)
	if gotFFmpeg != ffmpegPath {
		t.Fatalf("ffmpeg = %q; want %q", gotFFmpeg, ffmpegPath)
	}
	wantProbe := filepath.Join(root, "ffprobe")
	if gotProbe != wantProbe {
		t.Fatalf("ffprobe = %q; want %q", gotProbe, wantProbe)
	}
}

func TestPlannerCapabilitiesHonorConfiguredFFmpeg(t *testing.T) {
	root := t.TempDir()
	configured := filepath.Join(root, "ffmpeg")
	if err := os.WriteFile(configured, []byte("stub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if capabilities := plannerCapabilitiesFor(Request{Filesystem: FilesystemOptions{FfmpegLocation: root}}); !capabilities.CanMergeFormats {
		t.Fatal("configured ffmpeg should enable merge planning")
	}

	missing := filepath.Join(root, "missing-ffmpeg")
	if capabilities := plannerCapabilitiesFor(Request{Filesystem: FilesystemOptions{FfmpegLocation: missing}}); capabilities.CanMergeFormats {
		t.Fatal("missing configured ffmpeg should disable merge planning")
	}
}
