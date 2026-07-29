package ytdlp

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ytdlp-go/ytdlp/internal/downloader"
	mediaformat "github.com/ytdlp-go/ytdlp/internal/format"
	"github.com/ytdlp-go/ytdlp/internal/value"
)

func TestResolveOutputPlanDestinationsPerPlanTemplates(t *testing.T) {
	info := value.NewInfo(value.NewObject(
		value.Field{Key: "id", Value: value.String("multi")},
		value.Field{Key: "title", Value: value.String("Title")},
		value.Field{Key: "ext", Value: value.String("mp4")},
	))
	plans := []mediaformat.OutputPlan{
		{Tracks: []mediaformat.Selection{{ID: "video", Ext: "mp4", VCodec: "avc1", ACodec: "none"}}},
		{Tracks: []mediaformat.Selection{{ID: "audio", Ext: "m4a", VCodec: "none", ACodec: "aac"}}},
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
		{Tracks: []mediaformat.Selection{{ID: "video", Ext: "mp4", VCodec: "avc1", ACodec: "none"}}},
		{Tracks: []mediaformat.Selection{{ID: "audio", Ext: "mp4", VCodec: "none", ACodec: "aac"}}},
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

func TestPreflightPlanDestinationsRejectsExistingWithoutOverwrite(t *testing.T) {
	root := t.TempDir()
	existing := filepath.Join(root, "Title.mp4")
	if err := os.WriteFile(existing, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	operation := &operation{request: Request{OutputDir: root, Overwrite: false}}
	_, err := operation.preflightPlanDestinations([]string{existing})
	if !errors.Is(err, downloader.ErrDestinationExists) {
		t.Fatalf("preflight = %v", err)
	}
}

func TestPreflightPlanDestinationsRejectsDuplicateRenderedPaths(t *testing.T) {
	operation := &operation{request: Request{Overwrite: true}}
	dup := filepath.Join(t.TempDir(), "same.mp4")
	_, err := operation.preflightPlanDestinations([]string{dup, dup})
	if !errors.Is(err, errDestinationCollision) {
		t.Fatalf("preflight = %v", err)
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
