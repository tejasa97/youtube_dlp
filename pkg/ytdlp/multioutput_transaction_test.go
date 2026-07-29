package ytdlp

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ytdlp-go/ytdlp/internal/downloader"
	mediaformat "github.com/ytdlp-go/ytdlp/internal/format"
	"github.com/ytdlp-go/ytdlp/internal/value"
)

func planMetadataForTrack(id, ext string) value.Info {
	return value.NewInfo(value.NewObject(
		value.Field{Key: "format_id", Value: value.String(id)},
		value.Field{Key: "ext", Value: value.String(ext)},
	))
}

func TestPortablePathKeyCaseFolding(t *testing.T) {
	if portablePathKey("/tmp/A.mp4") != portablePathKey("/tmp/a.mp4") {
		t.Fatal("case-only paths should collide")
	}
}

func TestResolveOutputPlanDestinationsPerPlanTemplates(t *testing.T) {
	info := value.NewInfo(value.NewObject(
		value.Field{Key: "id", Value: value.String("multi")},
		value.Field{Key: "title", Value: value.String("Title")},
		value.Field{Key: "ext", Value: value.String("mp4")},
	))
	plans := []mediaformat.OutputPlan{
		{
			Tracks:   []mediaformat.Selection{{ID: "video", Ext: "mp4", VCodec: "avc1", ACodec: "none"}},
			Metadata: planMetadataForTrack("video", "mp4"),
		},
		{
			Tracks:   []mediaformat.Selection{{ID: "audio", Ext: "m4a", VCodec: "none", ACodec: "aac"}},
			Metadata: planMetadataForTrack("audio", "m4a"),
		},
	}
	root := t.TempDir()
	operation := &operation{request: Request{OutputDir: root}}
	destinations, err := operation.resolveOutputPlanDestinations(info, plans)
	if err != nil {
		t.Fatal(err)
	}
	if len(destinations) != 2 {
		t.Fatalf("destinations = %#v", destinations)
	}
	if filepath.Ext(destinations[0]) != ".mp4" || filepath.Ext(destinations[1]) != ".m4a" {
		t.Fatalf("extensions = %q %q", destinations[0], destinations[1])
	}
	if destinations[0] == destinations[1] {
		t.Fatalf("paths collided: %q", destinations)
	}
	if strings.Contains(destinations[0], ".f1_") || strings.Contains(destinations[1], ".f2_") {
		t.Fatalf("unexpected mechanical suffix: %#v", destinations)
	}
}

func TestResolveOutputPlanDestinationsCollidingExtAppliesSuffix(t *testing.T) {
	info := value.NewInfo(value.NewObject(
		value.Field{Key: "id", Value: value.String("multi")},
		value.Field{Key: "title", Value: value.String("Title")},
	))
	plans := []mediaformat.OutputPlan{
		{
			Tracks:   []mediaformat.Selection{{ID: "video", Ext: "mp4", VCodec: "avc1", ACodec: "none"}},
			Metadata: planMetadataForTrack("video", "mp4"),
		},
		{
			Tracks:   []mediaformat.Selection{{ID: "audio", Ext: "mp4", VCodec: "none", ACodec: "aac"}},
			Metadata: planMetadataForTrack("audio", "mp4"),
		},
	}
	root := t.TempDir()
	operation := &operation{request: Request{OutputDir: root}}
	destinations, err := operation.resolveOutputPlanDestinations(info, plans)
	if err != nil {
		t.Fatal(err)
	}
	if destinations[0] == destinations[1] {
		t.Fatalf("paths collided without suffix: %#v", destinations)
	}
	if !strings.Contains(destinations[1], ".f2_") {
		t.Fatalf("second destination = %q, want mechanical suffix", destinations[1])
	}
}

func TestAlignMergedDestinationExtensionCustomSuffix(t *testing.T) {
	plan := mediaformat.OutputPlan{
		Tracks: []mediaformat.Selection{
			{ID: "v", Ext: "mp4", VCodec: "avc1", ACodec: "none"},
			{ID: "a", Ext: "m4a", VCodec: "none", ACodec: "aac"},
		},
		Metadata: value.NewInfo(value.NewObject(value.Field{Key: "ext", Value: value.String("mp4")})),
	}
	got, err := alignMergedDestinationExtension("/tmp/out/name.custom", plan, nil, Request{})
	if err != nil {
		t.Fatal(err)
	}
	if got != "/tmp/out/name.custom.mp4" {
		t.Fatalf("merged correct_ext = %q", got)
	}
}

func TestSingleOutputSkipsMergedExtensionAlignment(t *testing.T) {
	info := value.NewInfo(value.NewObject(
		value.Field{Key: "id", Value: value.String("one")},
		value.Field{Key: "title", Value: value.String("Stem")},
	))
	plan := mediaformat.OutputPlan{
		Tracks: []mediaformat.Selection{{ID: "a", Ext: "m4a", VCodec: "none", ACodec: "aac"}},
		Metadata: value.NewInfo(value.NewObject(
			value.Field{Key: "format_id", Value: value.String("a")},
			value.Field{Key: "ext", Value: value.String("m4a")},
		)),
	}
	root := t.TempDir()
	operation := &operation{
		request: Request{OutputDir: root, OutputTemplate: "Stem"},
	}
	destinations, err := operation.resolveOutputPlanDestinations(info, []mediaformat.OutputPlan{plan})
	if err != nil {
		t.Fatal(err)
	}
	if destinations[0] != filepath.Join(root, "Stem") {
		t.Fatalf("extensionless template = %q", destinations[0])
	}
}

func TestBeginMediaTransactionRejectsExistingWithoutOverwrite(t *testing.T) {
	root := t.TempDir()
	existing := filepath.Join(root, "Title.mp4")
	if err := os.WriteFile(existing, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	operation := &operation{request: Request{OutputDir: root, Overwrite: false}}
	_, err := operation.beginMediaTransaction([]string{existing})
	if !errors.Is(err, downloader.ErrDestinationExists) {
		t.Fatalf("begin = %v", err)
	}
}

func TestBeginMediaTransactionRejectsPortableCollision(t *testing.T) {
	root := t.TempDir()
	first := filepath.Join(root, "A.mp4")
	second := filepath.Join(root, "a.mp4")
	operation := &operation{request: Request{OutputDir: root, Overwrite: true}}
	_, err := operation.beginMediaTransaction([]string{first, second})
	if !errors.Is(err, errDestinationCollision) {
		t.Fatalf("begin = %v", err)
	}
}

func TestMediaTransactionRestoresOverwrittenDestination(t *testing.T) {
	root := t.TempDir()
	existing := filepath.Join(root, "keep.mp4")
	original := []byte("original-bytes")
	if err := os.WriteFile(existing, original, 0o644); err != nil {
		t.Fatal(err)
	}
	operation := &operation{request: Request{OutputDir: root, Overwrite: true}}
	tx, err := operation.beginMediaTransaction([]string{existing})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(existing, []byte("replacement"), 0o644); err != nil {
		t.Fatal(err)
	}
	tx.markPublished(existing)
	if err := tx.rollback(); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(existing)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != string(original) {
		t.Fatalf("restored = %q, want %q", body, original)
	}
}

func TestMediaTransactionCommitRemovesBackups(t *testing.T) {
	root := t.TempDir()
	existing := filepath.Join(root, "keep.mp4")
	if err := os.WriteFile(existing, []byte("original"), 0o644); err != nil {
		t.Fatal(err)
	}
	operation := &operation{request: Request{OutputDir: root, Overwrite: true}}
	tx, err := operation.beginMediaTransaction([]string{existing})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(existing, []byte("new"), 0o644); err != nil {
		t.Fatal(err)
	}
	tx.markPublished(existing)
	if err := tx.commit(); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.Contains(entry.Name(), ".ytdlp-trx-") {
			t.Fatalf("backup still present: %s", entry.Name())
		}
	}
	body, err := os.ReadFile(existing)
	if err != nil || string(body) != "new" {
		t.Fatalf("committed file = %q, %v", body, err)
	}
}

func TestMediaTransactionRollbackSurfacesRestoreError(t *testing.T) {
	root := t.TempDir()
	existing := filepath.Join(root, "keep.mp4")
	if err := os.WriteFile(existing, []byte("original"), 0o644); err != nil {
		t.Fatal(err)
	}
	operation := &operation{request: Request{OutputDir: root, Overwrite: true}}
	tx, err := operation.beginMediaTransaction([]string{existing})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(existing, []byte("replacement"), 0o644); err != nil {
		t.Fatal(err)
	}
	tx.markPublished(existing)
	for _, slot := range tx.destinations {
		if slot.backupPath != "" {
			if err := os.Remove(slot.backupPath); err != nil {
				t.Fatal(err)
			}
		}
	}
	rollbackErr := tx.rollback()
	if rollbackErr == nil {
		t.Fatal("expected rollback error")
	}
}

func TestMultiOutputProductDownloadsCommaSelector(t *testing.T) {
	pageURL := multiOutputSelectorFixture(t, nil)
	root := t.TempDir()
	result, err := NewClient().Run(context.Background(), Request{
		URL: pageURL, OutputDir: root, Format: "video,audio", Overwrite: true,
	})
	if err != nil {
		t.Fatalf("Run() = %v", err)
	}
	if !result.Downloaded || result.Filename == "" {
		t.Fatalf("result = %#v", result)
	}
	media := 0
	for _, artifact := range result.Artifacts {
		if artifact.Kind == "media" {
			media++
			if _, statErr := os.Stat(artifact.Path); statErr != nil {
				t.Fatalf("missing artifact %q: %v", artifact.Path, statErr)
			}
		}
	}
	if media != 2 {
		t.Fatalf("media artifacts = %d, want 2: %#v", media, result.Artifacts)
	}
	if result.Bytes <= 0 {
		t.Fatalf("bytes = %d", result.Bytes)
	}
}

func TestMultiOutputProductRollbackRestoresOverwrite(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = writer.Write([]byte("NEW"))
	}))
	defer server.Close()

	root := t.TempDir()
	videoPath := filepath.Join(root, "one.mp4")
	audioPath := filepath.Join(root, "two.m4a")
	if err := os.WriteFile(videoPath, []byte("OLD1"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(audioPath, []byte("OLD2"), 0o644); err != nil {
		t.Fatal(err)
	}

	page := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(writer, `{
			"id":"overwrite","title":"One","ext":"mp4",
			"formats":[
				{"format_id":"one","url":%q,"ext":"mp4","vcodec":"avc1","acodec":"aac"},
				{"format_id":"two","url":"://missing","ext":"m4a","vcodec":"none","acodec":"aac"}
			]
		}`, server.URL)
	}))
	defer page.Close()

	_, err := NewClient().Run(context.Background(), Request{
		URL: page.URL, OutputDir: root, Format: "one,two",
		OutputTemplate: "%(format_id)s.%(ext)s", Overwrite: true,
	})
	if err == nil {
		t.Fatal("expected download error")
	}
	videoBody, err := os.ReadFile(videoPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(videoBody) != "OLD1" {
		t.Fatalf("video restored = %q", videoBody)
	}
	audioBody, err := os.ReadFile(audioPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(audioBody) != "OLD2" {
		t.Fatalf("audio restored = %q", audioBody)
	}
}

func TestMultiOutputPreflightBlocksSidecarWrites(t *testing.T) {
	root := t.TempDir()
	existing := filepath.Join(root, "Multi.mp4")
	if err := os.WriteFile(existing, []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}
	pageURL := multiOutputSelectorFixture(t, nil)
	_, err := NewClient().Run(context.Background(), Request{
		URL: pageURL, OutputDir: root, Format: "video,audio", Overwrite: false,
	})
	if err == nil {
		t.Fatal("expected preflight error")
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		name := entry.Name()
		if strings.HasSuffix(name, ".vtt") || strings.HasSuffix(name, ".webp") ||
			strings.HasSuffix(name, ".description") || strings.HasSuffix(name, ".print") {
			t.Fatalf("sidecar written during failed preflight: %s", name)
		}
	}
}

func TestMultiOutputProductRollbackThroughRun(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = writer.Write([]byte("ok"))
	}))
	defer server.Close()

	root := t.TempDir()
	page := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{
			"id":"multi","title":"Rollback","ext":"mp4",
			"formats":[
				{"format_id":"ok","url":"` + server.URL + `","ext":"mp4","vcodec":"avc1","acodec":"aac"},
				{"format_id":"bad","url":"://missing","ext":"mp4","vcodec":"avc1","acodec":"aac"}
			]
		}`))
	}))
	defer page.Close()

	_, err := NewClient().Run(context.Background(), Request{
		URL: page.URL, OutputDir: root, Format: "ok,bad", Overwrite: true,
	})
	if err == nil {
		t.Fatal("expected download error")
	}
	for _, name := range []string{"Rollback.mp4", "Rollback.f2_bad.mp4"} {
		if _, statErr := os.Stat(filepath.Join(root, name)); statErr == nil {
			t.Fatalf("partial output %q still present", name)
		}
	}
}

func TestMultiOutputDownloadCancellationRollsBack(t *testing.T) {
	root := t.TempDir()
	existing := filepath.Join(root, "Slow.mp4")
	if err := os.WriteFile(existing, []byte("KEEP"), 0o644); err != nil {
		t.Fatal(err)
	}
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/page" {
			writer.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprintf(writer, `{
				"id":"slow","title":"Slow","ext":"mp4",
				"formats":[
					{"format_id":"one","url":%q,"ext":"mp4","vcodec":"avc1","acodec":"aac"},
					{"format_id":"two","url":%q,"ext":"mp4","vcodec":"avc1","acodec":"aac"}
				]
			}`, server.URL+"/one", server.URL+"/two")
			return
		}
		if request.URL.Path == "/one" {
			_, _ = writer.Write([]byte("ONE"))
			return
		}
		if request.URL.Path == "/two" {
			flusher, ok := writer.(http.Flusher)
			if !ok {
				http.Error(writer, "no flush", http.StatusInternalServerError)
				return
			}
			writer.WriteHeader(http.StatusOK)
			_, _ = writer.Write([]byte("TWO"))
			flusher.Flush()
			<-request.Context().Done()
			return
		}
		http.NotFound(writer, request)
	}))
	defer server.Close()

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := NewClient().Run(ctx, Request{
			URL: server.URL + "/page", OutputDir: root, Format: "one,two", Overwrite: true,
		})
		done <- err
	}()
	time.Sleep(50 * time.Millisecond)
	cancel()
	err := <-done
	if err == nil {
		t.Fatal("expected cancellation error")
	}
	body, readErr := os.ReadFile(existing)
	if readErr != nil || string(body) != "KEEP" {
		t.Fatalf("restored overwrite = %q %v", body, readErr)
	}
}
