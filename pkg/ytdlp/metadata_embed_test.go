package ytdlp

import (
	"context"
	"errors"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/ytdlp-go/ytdlp/internal/media/ffmpeg"
	"github.com/ytdlp-go/ytdlp/internal/value"
)

func TestCanonicalEmbeddedMetadataUsesPinnedPrecedenceAndBounds(t *testing.T) {
	info := value.NewInfo(value.NewObject(
		value.Field{Key: "title", Value: value.String("Episode title")},
		value.Field{Key: "track", Value: value.String("Track title")},
		value.Field{Key: "artists", Value: value.List(value.String("A"), value.String("B"))},
		value.Field{Key: "uploader", Value: value.String("fallback")},
		value.Field{Key: "episode_number", Value: value.Int(7)},
		value.Field{Key: "description", Value: value.String("line one\nline two\x00")},
	))
	got := canonicalEmbeddedMetadata(info)
	want := ffmpeg.Metadata{
		"title": "Track title", "artist": "A, B", "episode_sort": "7",
		"description": "line one line two", "synopsis": "line one line two",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("metadata=%#v want=%#v", got, want)
	}
}

func TestCanonicalEmbeddedChaptersValidatesTimeline(t *testing.T) {
	chapter := func(start, end float64, title string) value.Value {
		return value.ObjectValue(value.NewObject(
			value.Field{Key: "start_time", Value: value.Float(start)},
			value.Field{Key: "end_time", Value: value.Float(end)},
			value.Field{Key: "title", Value: value.String(title)},
		))
	}
	info := value.NewInfo(value.NewObject(value.Field{Key: "chapters", Value: value.List(
		chapter(0, 1.25, "Intro"), chapter(1.25, 3, "Main"),
	)}))
	got, err := canonicalEmbeddedChapters(info)
	if err != nil || len(got) != 2 || got[0].End != 1250*time.Millisecond || got[1].Title != "Main" {
		t.Fatalf("chapters=%+v err=%v", got, err)
	}
	info.Set("chapters", value.List(chapter(2, 1, "invalid")))
	if _, err := canonicalEmbeddedChapters(info); !errors.Is(err, ffmpeg.ErrInvalidOperation) {
		t.Fatalf("invalid timeline error=%v", err)
	}
}

func TestMetadataEmbeddingDependentChapterDefaultAndContainerPreflight(t *testing.T) {
	request := Request{EmbedMetadata: true}
	if !request.embedsChapters() {
		t.Fatal("metadata did not imply chapters")
	}
	disabled := false
	request.EmbedChapters = &disabled
	if request.embedsChapters() {
		t.Fatal("explicit chapter disable was ignored")
	}
	request.EmbedChapters = nil
	if err := validateMetadataEmbeddingContainer("video.mp4", request); err != nil {
		t.Fatal(err)
	}
	if err := validateMetadataEmbeddingContainer("video.avi", request); !errors.Is(err, ffmpeg.ErrInvalidOperation) {
		t.Fatalf("unsupported container error=%v", err)
	}
}

func TestAutomaticMetadataEmbeddingWithRealFFmpeg(t *testing.T) {
	ffmpegPath, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg unavailable")
	}
	root := t.TempDir()
	mediaPath := filepath.Join(root, "media.mp4")
	output, err := exec.Command(
		ffmpegPath, "-nostdin", "-y", "-f", "lavfi", "-i", "color=c=black:s=16x16:d=0.4",
		"-an", "-c:v", "mpeg4", mediaPath,
	).CombinedOutput()
	if err != nil {
		t.Fatalf("generate media: %v: %s", err, output)
	}
	info := value.NewInfo(value.NewObject(
		value.Field{Key: "title", Value: value.String("Embedded title")},
		value.Field{Key: "chapters", Value: value.List(value.ObjectValue(value.NewObject(
			value.Field{Key: "start_time", Value: value.Float(0)},
			value.Field{Key: "end_time", Value: value.Float(0.3)},
			value.Field{Key: "title", Value: value.String("Intro")},
		)))},
	))
	operation := &operation{client: NewClient(), request: Request{EmbedMetadata: true, Overwrite: true}}
	changed, err := operation.applyAutomaticMetadataEmbedding(context.Background(), info, mediaPath, nil)
	if err != nil || !changed {
		t.Fatalf("changed=%v err=%v", changed, err)
	}
	tools, err := ffmpeg.Discover(ffmpeg.Config{})
	if err != nil {
		t.Fatal(err)
	}
	probe, err := tools.Probe(context.Background(), mediaPath)
	if err != nil || probe.Format.Tags["title"] != "Embedded title" || len(probe.Chapters) != 1 || probe.Chapters[0].Tags["title"] != "Intro" {
		t.Fatalf("probe=%+v err=%v", probe, err)
	}
}
