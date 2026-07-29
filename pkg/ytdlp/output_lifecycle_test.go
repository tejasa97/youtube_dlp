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

	mediaformat "github.com/ytdlp-go/ytdlp/internal/format"
	"github.com/ytdlp-go/ytdlp/internal/network"
	"github.com/ytdlp-go/ytdlp/internal/value"
)

func TestNewOutputLifecycleForPlanClonesMetadata(t *testing.T) {
	original := value.NewInfo(value.NewObject(
		value.Field{Key: "id", Value: value.String("multi")},
		value.Field{Key: "title", Value: value.String("Title")},
		value.Field{Key: "ext", Value: value.String("mp4")},
	))
	plan := mediaformat.OutputPlan{
		Tracks: []mediaformat.Selection{
			{ID: "video", Ext: "mp4", VCodec: "avc1", ACodec: "none"},
		},
		Metadata: value.NewInfo(value.NewObject(
			value.Field{Key: "format_id", Value: value.String("video")},
			value.Field{Key: "format", Value: value.String("video - 1280x720")},
		)),
	}
	lifecycle := newOutputLifecycleForPlan(0, plan, original, filepath.Join(t.TempDir(), "video.mp4"))
	if formatID, _ := lifecycle.Info.Lookup("format_id").StringValue(); formatID != "video" {
		t.Fatalf("format_id = %q", formatID)
	}
	lifecycle.Info.Set("title", value.String("Mutated"))
	if title, _ := original.Lookup("title").StringValue(); title != "Title" {
		t.Fatalf("original title mutated = %q", title)
	}
}

func TestAggregateLifecyclesReportsFirstFilename(t *testing.T) {
	lifecycles := []outputLifecycle{
		{Index: 0, FinalPath: "/tmp/a.mp4", Downloaded: true, Bytes: 100},
		{Index: 1, FinalPath: "/tmp/b.m4a", Downloaded: true, Bytes: 50},
	}
	result := aggregateLifecycles(lifecycles)
	if result.Filename != "/tmp/a.mp4" {
		t.Fatalf("filename = %q", result.Filename)
	}
	if result.Bytes != 150 {
		t.Fatalf("bytes = %d", result.Bytes)
	}
}

func TestAggregateLifecyclesPreservesSidecarsBeforeMedia(t *testing.T) {
	lifecycles := []outputLifecycle{
		{
			Index: 0, FinalPath: "/tmp/a.mp4", Downloaded: true,
			Sidecars:       []Artifact{{Path: "/tmp/a.side.mp4", Kind: "sidecar"}},
			MediaArtifacts: []Artifact{{Path: "/tmp/a.mp4", Kind: "media"}},
			Prints:         []PrintOutput{{Stage: PrintVideo, Text: "video-1"}},
		},
		{
			Index: 1, FinalPath: "/tmp/b.m4a", Downloaded: true,
			Sidecars:       []Artifact{{Path: "/tmp/b.side.m4a", Kind: "sidecar"}},
			MediaArtifacts: []Artifact{{Path: "/tmp/b.m4a", Kind: "media"}},
			Prints:         []PrintOutput{{Stage: PrintVideo, Text: "video-2"}},
		},
	}
	result := aggregateLifecycles(lifecycles)
	wantPaths := []string{
		"/tmp/a.side.mp4",
		"/tmp/b.side.m4a",
		"/tmp/a.mp4",
		"/tmp/b.m4a",
	}
	if len(result.Artifacts) != len(wantPaths) {
		t.Fatalf("artifact count = %d, want %d (%#v)", len(result.Artifacts), len(wantPaths), result.Artifacts)
	}
	for index, want := range wantPaths {
		if result.Artifacts[index].Path != want {
			t.Fatalf("artifact[%d] = %q, want %q (full: %#v)", index, result.Artifacts[index].Path, want, result.Artifacts)
		}
	}
	if len(result.Prints) != 2 {
		t.Fatalf("print count = %d", len(result.Prints))
	}
	if result.Prints[0].Text != "video-1" || result.Prints[1].Text != "video-2" {
		t.Fatalf("print order: %#v", result.Prints)
	}
}

func TestAggregateLifecyclesReportsNoFilenameWhenNothingDownloaded(t *testing.T) {
	lifecycles := []outputLifecycle{{Index: 0, FinalPath: "/tmp/a.mp4"}}
	result := aggregateLifecycles(lifecycles)
	if result.Filename != "" {
		t.Fatalf("filename = %q", result.Filename)
	}
}

func TestLifecycleInternalErrorPreservesUnderlyingIdentity(t *testing.T) {
	sentinel := errors.New("specific cause")
	wrapped := wrapLifecycleError("stage", sentinel)
	if !errors.Is(wrapped, sentinel) {
		t.Fatalf("wrapped = %v, must satisfy errors.Is(sentinel)", wrapped)
	}
	if !errors.Is(wrapped, errLifecycleInternal) {
		t.Fatalf("wrapped = %v, must satisfy errors.Is(errLifecycleInternal)", wrapped)
	}
	if !strings.Contains(wrapped.Error(), "stage") {
		t.Fatalf("wrapped = %q, must include op", wrapped.Error())
	}
}

func TestExecuteOutputLifecycleRejectsNilLifecycle(t *testing.T) {
	transaction := newMediaTransaction(nil)
	err := (&operation{}).executeOutputLifecycle(t.Context(), &transaction, nil, nil)
	if !errors.Is(err, errLifecycleInternal) {
		t.Fatalf("err = %v", err)
	}
}

func TestExecuteOutputLifecycleRejectsNilTransaction(t *testing.T) {
	plan := mediaformat.OutputPlan{Tracks: []mediaformat.Selection{{ID: "video", Ext: "mp4"}}}
	lifecycle := newOutputLifecycleForPlan(0, plan, value.NewInfo(value.NewObject()), "/tmp/video.mp4")
	err := (&operation{}).executeOutputLifecycle(t.Context(), nil, &lifecycle, nil)
	if !errors.Is(err, errLifecycleInternal) {
		t.Fatalf("err = %v", err)
	}
}

func TestExecuteOutputLifecycleRegistersPrintArtifactsInTransaction(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/video" {
			if request.Method == http.MethodHead {
				writer.Header().Set("Content-Length", "5")
				return
			}
			_, _ = writer.Write([]byte("bytes"))
			return
		}
		http.NotFound(writer, request)
	}))
	defer server.Close()

	root := t.TempDir()
	// Place the print file in a subdirectory so the confined root
	// check inside writePrintFiles succeeds.
	printDir := filepath.Join(root, "logs")
	if err := os.Mkdir(printDir, 0o755); err != nil {
		t.Fatalf("mkdir logs: %v", err)
	}
	printFile := filepath.Join(printDir, "video.print")

	info := value.NewInfo(value.NewObject(
		value.Field{Key: "id", Value: value.String("single")},
		value.Field{Key: "title", Value: value.String("Single")},
	))
	plan := mediaformat.OutputPlan{
		Tracks: []mediaformat.Selection{{
			ID: "video", URL: server.URL + "/video", Ext: "mp4",
			VCodec: "avc1", ACodec: "none",
		}},
	}
	destination := filepath.Join(root, "video.mp4")
	lifecycle := newOutputLifecycleForPlan(0, plan, info, destination)

	transport, err := network.New(network.Config{})
	if err != nil {
		t.Fatalf("network.New: %v", err)
	}
	defer transport.CloseIdleConnections()

	operation := &operation{
		client:    NewClient(),
		transport: transport,
		request:   Request{OutputDir: root, Overwrite: true, PrintRules: []PrintRule{{Stage: PrintVideo, Template: "[%(id)s]", FileTemplate: "logs/video.print"}}},
	}
	sink := operation.eventSink()

	transaction := newMediaTransaction([]string{destination})
	if err := operation.executeOutputLifecycle(t.Context(), &transaction, &lifecycle, sink); err != nil {
		t.Fatalf("lifecycle: %v", err)
	}
	if !lifecycle.Downloaded {
		t.Fatal("lifecycle.Downloaded = false")
	}
	if _, err := os.Stat(lifecycle.FinalPath); err != nil {
		t.Fatalf("media missing: %v", err)
	}
	if lifecycle.FinalPath != destination {
		t.Fatalf("FinalPath = %q, want %q", lifecycle.FinalPath, destination)
	}
	if _, err := os.Stat(printFile); err != nil {
		t.Fatalf("print file missing: %v", err)
	}
	// The lifecycle must register print artifacts with the
	// transaction so rollback covers them.
	transaction.rollback()
	if _, err := os.Stat(lifecycle.FinalPath); err == nil {
		t.Fatalf("transaction.rollback did not remove media %q", lifecycle.FinalPath)
	}
	if _, err := os.Stat(printFile); err == nil {
		t.Fatalf("transaction.rollback did not remove print file %q", printFile)
	}
}

func TestExecuteOutputLifecycleContextCancellationIsDiscoverable(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	plan := mediaformat.OutputPlan{Tracks: []mediaformat.Selection{{ID: "video", Ext: "mp4"}}}
	lifecycle := newOutputLifecycleForPlan(0, plan, value.NewInfo(value.NewObject()), "/tmp/video.mp4")
	transaction := newMediaTransaction([]string{lifecycle.Destination})
	err := (&operation{}).executeOutputLifecycle(ctx, &transaction, &lifecycle, nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}

func TestExecuteOutputLifecycleDownloadFailurePreservesErrorIdentity(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		http.Error(writer, "no", http.StatusNotFound)
	}))
	defer server.Close()

	root := t.TempDir()
	info := value.NewInfo(value.NewObject(value.Field{Key: "id", Value: value.String("missing")}))
	plan := mediaformat.OutputPlan{
		Tracks: []mediaformat.Selection{{
			ID: "video", URL: server.URL + "/missing", Ext: "mp4",
			VCodec: "avc1", ACodec: "none",
		}},
	}
	destination := filepath.Join(root, "video.mp4")
	lifecycle := newOutputLifecycleForPlan(0, plan, info, destination)

	transport, err := network.New(network.Config{})
	if err != nil {
		t.Fatalf("network.New: %v", err)
	}
	defer transport.CloseIdleConnections()

	operation := &operation{client: NewClient(), request: Request{OutputDir: root, Overwrite: true}, transport: transport}
	transaction := newMediaTransaction([]string{destination})
	err = operation.executeOutputLifecycle(t.Context(), &transaction, &lifecycle, operation.eventSink())
	if err == nil {
		t.Fatal("expected download error")
	}
	if !errors.Is(err, errLifecycleInternal) {
		t.Fatalf("err = %v, must wrap errLifecycleInternal", err)
	}
	if !strings.Contains(err.Error(), "download selected formats") {
		t.Fatalf("err = %v, must mention the failing stage", err)
	}
}

func writePrintFileFixture(path, content string) error {
	return os.WriteFile(path, []byte(content), 0o600)
}
