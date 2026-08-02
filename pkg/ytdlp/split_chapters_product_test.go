package ytdlp

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
	"strings"
	"testing"

	"github.com/ytdlp-go/ytdlp/internal/media/ffmpeg"
	"github.com/ytdlp-go/ytdlp/internal/media/postprocess"
	"github.com/ytdlp-go/ytdlp/internal/value"
)

func TestProductSplitChaptersPublishesConfinedArtifactsAndOneArchiveRecord(t *testing.T) {
	media := generateSplitChapterMedia(t)
	server := serveSplitChapterMedia(t, media)
	defer server.Close()
	root := t.TempDir()
	archivePath := filepath.Join(root, "archive.txt")
	result, err := NewClient().Run(context.Background(), Request{
		URL: server.URL + "/page", OutputDir: root, OutputTemplate: "main.%(ext)s", Overwrite: true,
		DownloadArchive: archivePath, SplitChapters: true,
		OutputPaths:     OutputPaths{OutputPathHome: root, OutputPathChapter: "chapters"},
		OutputTemplates: OutputTemplates{OutputTemplateChapter: "%(title)s-%(section_number)02d-%(section_title)s.%(ext)s"},
	})
	if err != nil || !result.Downloaded {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	if filepath.Base(result.Filename) != "main.mp4" {
		t.Fatalf("main filename=%q", result.Filename)
	}
	chapterArtifacts := make([]Artifact, 0, 2)
	for _, artifact := range result.Artifacts {
		if artifact.Kind == "chapter" {
			chapterArtifacts = append(chapterArtifacts, artifact)
		}
	}
	if len(chapterArtifacts) != 2 {
		t.Fatalf("chapter artifacts=%+v all artifacts=%+v", chapterArtifacts, result.Artifacts)
	}
	wantNames := []string{"Chapter Fixture-01-Intro.mp4", "Chapter Fixture-02-Main.mp4"}
	for index, artifact := range chapterArtifacts {
		if filepath.Base(artifact.Path) != wantNames[index] {
			t.Fatalf("chapter %d path=%q", index+1, artifact.Path)
		}
		relative, relErr := filepath.Rel(filepath.Join(root, "chapters"), artifact.Path)
		if relErr != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			t.Fatalf("chapter escaped chapter root: %q", artifact.Path)
		}
		if stat, statErr := os.Stat(artifact.Path); statErr != nil || stat.Size() == 0 {
			t.Fatalf("chapter %d stat=%v size=%d", index+1, statErr, stat.Size())
		}
	}
	archive, err := os.ReadFile(archivePath)
	if err != nil || strings.Count(string(archive), "chapter-fixture") != 1 {
		t.Fatalf("archive=%q err=%v", archive, err)
	}
	var info map[string]any
	if err := json.Unmarshal(result.InfoJSON, &info); err != nil || info["title"] != "Chapter Fixture" {
		t.Fatalf("info=%v err=%v", info, err)
	}
}

func TestProductSplitChaptersFailureIsAllOrNothing(t *testing.T) {
	media := generateSplitChapterMedia(t)
	server := serveSplitChapterMedia(t, media)
	defer server.Close()
	root := t.TempDir()
	destination := filepath.Join(root, "main.mp4")
	sentinel := []byte("original destination")
	if err := os.WriteFile(destination, sentinel, 0o600); err != nil {
		t.Fatal(err)
	}
	archivePath := filepath.Join(root, "archive.txt")
	archiveSentinel := []byte("archive-before\n")
	if err := os.WriteFile(archivePath, archiveSentinel, 0o600); err != nil {
		t.Fatal(err)
	}
	var events []Event
	completed := 0
	var mutationErr error
	client := NewClient(WithEventHandler(func(_ context.Context, event Event) error {
		events = append(events, event)
		if event.Kind == EventPostprocessCompleted && completed == 0 {
			completed++
			mutationErr = os.WriteFile(destination, []byte("invalid media"), 0o600)
		}
		return nil
	}))
	_, err := client.Run(context.Background(), Request{
		URL: server.URL + "/page", OutputDir: root, OutputTemplate: "main.%(ext)s", Overwrite: true,
		DownloadArchive: archivePath, SplitChapters: true,
		OutputPaths:     OutputPaths{OutputPathHome: root, OutputPathChapter: "chapters"},
		OutputTemplates: OutputTemplates{OutputTemplateChapter: "%(title)s-%(section_number)02d-%(section_title)s.%(ext)s"},
	})
	if err == nil || !IsCategory(err, ErrorInternal) {
		t.Fatalf("split failure=%v, want internal error", err)
	}
	if completed != 1 {
		t.Fatalf("events=%+v", events)
	}
	if mutationErr != nil {
		t.Fatalf("inject split failure: %v", mutationErr)
	}
	restored, readErr := os.ReadFile(destination)
	if readErr != nil || !bytes.Equal(restored, sentinel) {
		t.Fatalf("destination not restored: err=%v bytes=%q", readErr, restored)
	}
	archive, readErr := os.ReadFile(archivePath)
	if readErr != nil || !bytes.Equal(archive, archiveSentinel) {
		t.Fatalf("archive changed: err=%v bytes=%q", readErr, archive)
	}
	if matches, globErr := filepath.Glob(filepath.Join(root, "chapters", "*")); globErr != nil || len(matches) != 0 {
		t.Fatalf("partial chapters=%v err=%v", matches, globErr)
	}
	assertNoPostprocessTemps(t, root)
}

func TestSplitChaptersRejectsConflictingRenderedNamesBeforeFFmpeg(t *testing.T) {
	operation := &operation{client: NewClient(), request: Request{
		SplitChapters: true, OutputDir: t.TempDir(), OutputTemplates: OutputTemplates{
			OutputTemplateChapter: "%(section_title)s.%(ext)s",
		},
	}}
	info := chapterFixtureInfo()
	if _, err := operation.splitChapters(context.Background(), info, "missing.mp4", nil); !errors.Is(err, ffmpeg.ErrInvalidOperation) {
		t.Fatalf("collision error=%v", err)
	}
}

func TestSplitChapterDurationBoundUsesDeltaForLateTimeline(t *testing.T) {
	operation := &operation{client: NewClient(), request: Request{
		SplitChapters: true, OutputDir: t.TempDir(), OutputTemplates: OutputTemplates{
			OutputTemplateChapter: "same.%(ext)s",
		},
	}}
	info := value.NewInfo(value.NewObject(
		value.Field{Key: "title", Value: value.String("Late")},
		value.Field{Key: "ext", Value: value.String("mp4")},
		value.Field{Key: "chapters", Value: value.List(
			value.ObjectValue(value.NewObject(
				value.Field{Key: "start_time", Value: value.Float(25 * 60 * 60)},
				value.Field{Key: "end_time", Value: value.Float(25*60*60 + 1)},
				value.Field{Key: "title", Value: value.String("Late")},
			)),
		)},
	))
	_, err := operation.splitChapters(context.Background(), info, "missing.mp4", nil)
	if err == nil || strings.Contains(err.Error(), "exceeds duration limit") {
		t.Fatalf("late timeline error=%v, want duration-delta acceptance", err)
	}
}

func TestSplitChaptersRejectsSymlinkedChapterRoot(t *testing.T) {
	home := t.TempDir()
	outside := t.TempDir()
	sentinelPath := filepath.Join(outside, "untouched.txt")
	sentinel := []byte("outside sentinel")
	if err := os.WriteFile(sentinelPath, sentinel, 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(home, "chapters")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	operation := &operation{client: NewClient(), request: Request{
		SplitChapters: true, OutputDir: home,
		OutputPaths: OutputPaths{OutputPathHome: home, OutputPathChapter: "chapters"},
	}}
	_, err := operation.splitChapters(context.Background(), chapterFixtureInfo(), "missing.mp4", nil)
	if !errors.Is(err, postprocess.ErrUnsafePath) {
		t.Fatalf("symlink chapter root error=%v", err)
	}
	untouched, readErr := os.ReadFile(sentinelPath)
	if readErr != nil || !bytes.Equal(untouched, sentinel) {
		t.Fatalf("outside directory changed: err=%v bytes=%q", readErr, untouched)
	}
}

func generateSplitChapterMedia(t *testing.T) string {
	t.Helper()
	ffmpegPath, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg unavailable")
	}
	path := filepath.Join(t.TempDir(), "source.mp4")
	if output, err := exec.Command(ffmpegPath, "-nostdin", "-y", "-f", "lavfi", "-i", "color=c=black:s=32x32:d=2", "-c:v", "mpeg4", path).CombinedOutput(); err != nil {
		t.Fatalf("generate split fixture: %v: %s", err, output)
	}
	return path
}

func serveSplitChapterMedia(t *testing.T, media string) *httptest.Server {
	t.Helper()
	bytes, err := os.ReadFile(media)
	if err != nil {
		t.Fatal(err)
	}
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/page":
			writer.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprintf(writer, `{"id":"chapter-fixture","title":"Chapter Fixture","duration":2,"ext":"mp4","formats":[{"format_id":"media","url":%q,"ext":"mp4","vcodec":"mpeg4","acodec":"none"}],"chapters":[{"start_time":0,"end_time":1,"title":"Intro"},{"start_time":1,"end_time":2,"title":"Main"}]}`, server.URL+"/media.mp4")
		case "/media.mp4":
			writer.Header().Set("Content-Type", "video/mp4")
			writer.Header().Set("Content-Length", fmt.Sprint(len(bytes)))
			if request.Method != http.MethodHead {
				_, _ = writer.Write(bytes)
			}
		default:
			http.NotFound(writer, request)
		}
	}))
	return server
}

func chapterFixtureInfo() value.Info {
	return value.NewInfo(value.NewObject(
		value.Field{Key: "title", Value: value.String("Fixture")},
		value.Field{Key: "ext", Value: value.String("mp4")},
		value.Field{Key: "chapters", Value: value.List(
			value.ObjectValue(value.NewObject(
				value.Field{Key: "start_time", Value: value.Float(0)},
				value.Field{Key: "end_time", Value: value.Float(1)},
				value.Field{Key: "title", Value: value.String("Same")},
			)),
			value.ObjectValue(value.NewObject(
				value.Field{Key: "start_time", Value: value.Float(1)},
				value.Field{Key: "end_time", Value: value.Float(2)},
				value.Field{Key: "title", Value: value.String("same")},
			)),
		)},
	))
}
