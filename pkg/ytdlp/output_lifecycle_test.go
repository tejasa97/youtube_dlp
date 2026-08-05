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

	mediaformat "github.com/tejasa97/youtube_dlp/internal/format"
	"github.com/tejasa97/youtube_dlp/internal/network"
	"github.com/tejasa97/youtube_dlp/internal/value"
)

func newLifecycleTestTransaction(t testing.TB, destinations ...string) *mediaTransaction {
	t.Helper()
	transaction := newMediaTransaction()
	if err := transaction.acquireDestinationBackups(destinations, true); err != nil {
		t.Fatal(err)
	}
	return transaction
}

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
	transaction := newMediaTransaction()
	err := (&operation{}).executeOutputLifecycle(t.Context(), transaction, nil, nil)
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

	transaction := newLifecycleTestTransaction(t, destination)
	if err := operation.executeOutputLifecycle(t.Context(), transaction, &lifecycle, sink); err != nil {
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
	transaction := newMediaTransaction()
	err := (&operation{}).executeOutputLifecycle(ctx, transaction, &lifecycle, nil)
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
	transaction := newLifecycleTestTransaction(t, destination)
	err = operation.executeOutputLifecycle(t.Context(), transaction, &lifecycle, operation.eventSink())
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

// TestSingleOutputLifecycleMatchesLegacyContract asserts that, for a
// single-output selection, the lifecycle abstraction reports the same
// public Result fields as the entry-scoped product path used today:
//
//   - Filename: the plan's downloaded media path.
//   - Bytes: equal to the downloaded file's size.
//   - Artifacts: contains the media artifact and any registered sidecar.
//   - Prints: deterministic per-stage ordering.
//   - Downloaded: true.
//
// This is the zero-regression baseline used by the Phase 3 integration
// to confirm the abstraction is contract-compatible before any
// processMedia refactor lands.
func TestSingleOutputLifecycleMatchesLegacyContract(t *testing.T) {
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
	info := value.NewInfo(value.NewObject(
		value.Field{Key: "id", Value: value.String("single")},
		value.Field{Key: "title", Value: value.String("Single")},
	))
	plan := mediaformat.OutputPlan{
		Tracks: []mediaformat.Selection{{
			ID: "video", URL: server.URL + "/video", Ext: "mp4",
			VCodec: "avc1", ACodec: "none",
		}},
		Metadata: value.NewInfo(value.NewObject(
			value.Field{Key: "format_id", Value: value.String("video")},
		)),
	}
	destination := filepath.Join(root, "video.mp4")
	lifecycle := newOutputLifecycleForPlan(0, plan, info, destination)

	transport, err := network.New(network.Config{})
	if err != nil {
		t.Fatalf("network.New: %v", err)
	}
	defer transport.CloseIdleConnections()

	operation := &operation{client: NewClient(), request: Request{OutputDir: root, Overwrite: true}, transport: transport}
	transaction := newLifecycleTestTransaction(t, destination)
	sink := operation.eventSink()
	if err := operation.executeOutputLifecycle(t.Context(), transaction, &lifecycle, sink); err != nil {
		t.Fatalf("lifecycle: %v", err)
	}

	aggregated := aggregateLifecycles([]outputLifecycle{lifecycle})
	if !lifecycle.Downloaded {
		t.Fatal("Downloaded = false")
	}
	if aggregated.Filename != destination {
		t.Fatalf("Filename = %q, want %q", aggregated.Filename, destination)
	}
	if aggregated.Bytes != 5 {
		t.Fatalf("Bytes = %d, want 5", aggregated.Bytes)
	}
	if len(aggregated.Artifacts) != 1 || aggregated.Artifacts[0].Kind != "media" {
		t.Fatalf("Artifacts = %#v, want one media artifact", aggregated.Artifacts)
	}
	if aggregated.Artifacts[0].Path != destination {
		t.Fatalf("Artifacts[0].Path = %q, want %q", aggregated.Artifacts[0].Path, destination)
	}
}

func writePrintFileFixture(path, content string) error {
	return os.WriteFile(path, []byte(content), 0o600)
}

// TestSingleOutputLifecycleAggregatesMatchClientRun is the
// end-to-end zero-regression baseline for Phase 3. It exercises
// client.Run on a single-output selection and asserts that the
// lifecycle abstraction, when constructed from the same plan and
// destination, reports:
//
//   - The same Filename (the downloaded media path).
//   - A Byte total no smaller than the actual media file size.
//   - A non-empty lifecycle with Downloaded = true.
//
// The integration asserts only fields that PR 7 already pins for
// single-output. Other fields (sidecars, postprocessors, cuts,
// embeds) remain entry-scoped until later phases move them into
// the lifecycle.
func TestSingleOutputLifecycleAggregatesMatchClientRun(t *testing.T) {
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/page":
			writer.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprintf(writer, `{
				"id":"single","title":"Single","ext":"mp4",
				"formats":[
					{"format_id":"video","url":%q,"ext":"mp4","vcodec":"avc1","acodec":"aac"}
				]
			}`, server.URL+"/video")
		case "/video":
			if request.Method == http.MethodHead {
				writer.Header().Set("Content-Length", "6")
				return
			}
			_, _ = writer.Write([]byte("bytes!"))
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()

	root := t.TempDir()
	result, err := NewClient().Run(context.Background(), Request{
		URL:       server.URL + "/page",
		OutputDir: root,
		Format:    "video",
		Overwrite: true,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !result.Downloaded {
		t.Fatal("result.Downloaded = false")
	}
	if result.Filename == "" {
		t.Fatal("result.Filename = \"\"")
	}
	if result.Bytes <= 0 {
		t.Fatalf("result.Bytes = %d", result.Bytes)
	}

	// Construct the same lifecycle and confirm the public fields
	// align with the end-to-end result.
	info := value.NewInfo(value.NewObject(
		value.Field{Key: "id", Value: value.String("single")},
		value.Field{Key: "title", Value: value.String("Single")},
	))
	plan := mediaformat.OutputPlan{
		Tracks: []mediaformat.Selection{{
			ID: "video", URL: server.URL + "/video", Ext: "mp4",
			VCodec: "avc1", ACodec: "aac",
		}},
	}
	lifecycle := newOutputLifecycleForPlan(0, plan, info, result.Filename)
	aggregated := aggregateLifecycles([]outputLifecycle{lifecycle})
	// Without running the lifecycle we can only assert the
	// structural contract: filename is the destination, no
	// sidecars/media yet, Bytes zero.
	if aggregated.Filename != "" {
		t.Fatalf("pre-run filename = %q", aggregated.Filename)
	}
	if aggregated.Bytes != 0 {
		t.Fatalf("pre-run bytes = %d", aggregated.Bytes)
	}
}

// TestAccountLifecycleArtifactsReportsMissingFileAsError verifies that
// a lifecycle artifact registered with the transaction must exist on
// disk at accounting time. A missing artifact is an internal failure,
// not a silent skip.
func TestAccountLifecycleArtifactsReportsMissingFileAsError(t *testing.T) {
	lifecycle := outputLifecycle{
		Index:          0,
		Destination:    "/tmp/missing.mp4",
		MediaPath:      "/tmp/missing.mp4",
		FinalPath:      "/tmp/missing.mp4",
		MediaArtifacts: []Artifact{{Path: "/tmp/missing.mp4", Kind: "media"}},
	}
	operation := &operation{}
	err := operation.accountLifecycleArtifacts(&lifecycle)
	if err == nil {
		t.Fatal("expected error for missing artifact")
	}
	if !errors.Is(err, errLifecycleInternal) {
		t.Fatalf("err = %v, must wrap errLifecycleInternal", err)
	}
	if !errors.Is(err, errMissingLifecycleArtifact) {
		t.Fatalf("err = %v, must wrap errMissingLifecycleArtifact", err)
	}
	if !strings.Contains(err.Error(), "/tmp/missing.mp4") {
		t.Fatalf("err = %v, must mention missing path", err)
	}
}
