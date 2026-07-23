package ytdlp

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/ytdlp-go/ytdlp/internal/extractor"
	"github.com/ytdlp-go/ytdlp/internal/network"
	"github.com/ytdlp-go/ytdlp/internal/testserver"
	"github.com/ytdlp-go/ytdlp/internal/value"
)

func TestPrintStagesCaptureTransformedAndSelectedMetadata(t *testing.T) {
	server := testserver.New()
	defer server.Close()
	rules := []PrintRule{
		{Stage: PrintPreProcess, Template: "%(title)s"},
		{Stage: PrintAfterFilter, Template: "%(title)s"},
		{Stage: PrintVideo, Template: "%(id)s|%(format_id)s|%(ext)s|%(urls)s|%(filename)s"},
		{Stage: PrintBeforeDL, Template: "before"},
	}
	root := t.TempDir()
	result, err := NewClient().Run(context.Background(), Request{
		URL: server.URL + "/page", OutputDir: root, Simulate: true,
		ReplaceMetadata: []string{"title:Deterministic:Changed"},
		PrintRules:      rules,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Downloaded || len(result.Artifacts) != 0 {
		t.Fatalf("simulation wrote output: %#v", result)
	}
	if len(result.Prints) != 3 {
		t.Fatalf("prints=%#v", result.Prints)
	}
	if result.Prints[0] != (PrintOutput{Stage: PrintPreProcess, Text: "Deterministic Fixture"}) ||
		result.Prints[1] != (PrintOutput{Stage: PrintAfterFilter, Text: "Changed Fixture"}) {
		t.Fatalf("metadata stage prints=%#v", result.Prints)
	}
	video := result.Prints[2]
	if video.Stage != PrintVideo ||
		!strings.Contains(video.Text, "fixture-direct|direct-http|bin|"+server.URL+"/media|") ||
		!strings.HasSuffix(video.Text, filepath.Join(root, "Changed Fixture.bin")) {
		t.Fatalf("video print=%#v", video)
	}
}

func TestPrintLaterStagesRunInLifecycleOrder(t *testing.T) {
	server := testserver.New()
	defer server.Close()
	stages := []PrintStage{
		PrintPreProcess, PrintAfterFilter, PrintVideo, PrintBeforeDL,
		PrintPostProcess, PrintAfterMove, PrintAfterVideo,
	}
	rules := make([]PrintRule, len(stages))
	for index, stage := range stages {
		rules[index] = PrintRule{Stage: stage, Template: string(stage)}
	}
	rules[5].Template = "%(filepath)s"
	result, err := NewClient().Run(context.Background(), Request{
		URL: server.URL + "/page", OutputDir: t.TempDir(), SkipDownload: true, PrintRules: rules,
	})
	if err != nil {
		t.Fatal(err)
	}
	got := make([]PrintStage, len(result.Prints))
	for index, output := range result.Prints {
		got[index] = output.Stage
	}
	if !reflect.DeepEqual(got, stages) {
		t.Fatalf("stages=%v want=%v", got, stages)
	}
	if !strings.HasSuffix(result.Prints[5].Text, "Deterministic Fixture.bin") {
		t.Fatalf("after-move filepath=%q", result.Prints[5].Text)
	}
}

func TestPrintRuleValidationOptionalFieldsAndCancellation(t *testing.T) {
	for _, request := range []Request{
		{PrintRules: []PrintRule{{Stage: "unknown", Template: "x"}}},
		{PrintRules: []PrintRule{{Stage: PrintVideo}}},
	} {
		if err := validateRequestOptions(request); err == nil {
			t.Fatalf("request accepted: %#v", request)
		}
	}
	info := value.NewInfo(value.NewObject(value.Field{Key: "id", Value: value.String("one")}))
	operation := operation{request: Request{PrintRules: []PrintRule{
		{Stage: PrintVideo, Template: "%(description)s", OmitIfMissing: "description"},
	}}}
	prints, err := operation.capturePrints(context.Background(), PrintVideo, info, nil, "")
	if err != nil || len(prints) != 0 {
		t.Fatalf("optional prints=%#v error=%v", prints, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := operation.capturePrints(ctx, PrintVideo, info, nil, ""); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation error=%v", err)
	}
}

func TestLatePrintValidationFailsBeforeOutputSideEffects(t *testing.T) {
	server := testserver.New()
	defer server.Close()
	root := t.TempDir()
	_, err := NewClient().Run(context.Background(), Request{
		URL: server.URL + "/page", OutputDir: root,
		RelatedFiles: RelatedFileOptions{WriteInfoJSON: true},
		PrintRules:   []PrintRule{{Stage: PrintAfterMove, Template: "%(title)d"}},
	})
	if err == nil || !IsCategory(err, ErrorInvalidInput) {
		t.Fatalf("error=%v", err)
	}
	entries, readErr := filepath.Glob(filepath.Join(root, "*"))
	if readErr != nil || len(entries) != 0 {
		t.Fatalf("side effects=%v error=%v", entries, readErr)
	}
}

func TestPlaylistPrintsFollowChildLifecycle(t *testing.T) {
	server := playlistMediaServer(t)
	defer server.Close()
	transport, err := network.New(network.Config{})
	if err != nil {
		t.Fatal(err)
	}
	operation := &operation{
		client: NewClient(),
		request: Request{SkipDownload: true, PrintRules: []PrintRule{
			{Stage: PrintVideo, Template: "%(title)s"},
			{Stage: PrintPlaylist, Template: "%(title)s"},
		}},
		transport: transport,
		registry:  extractor.NewRegistry(playlistFixtureExtractor{}, extractor.NewGeneric()),
	}
	result, err := operation.process(context.Background(), server.URL+"/list", "", nil, make(map[string]bool), 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Prints) != 1 || result.Prints[0] != (PrintOutput{Stage: PrintPlaylist, Text: "Root Playlist"}) {
		t.Fatalf("root prints=%#v", result.Prints)
	}
	if len(result.Entries) != 2 || len(result.Entries[0].Prints) != 1 ||
		len(result.Entries[1].Entries) != 1 || len(result.Entries[1].Prints) != 1 {
		t.Fatalf("child prints=%#v", result.Entries)
	}
	if result.Entries[1].Prints[0].Text != "Nested Playlist" {
		t.Fatalf("nested playlist prints=%#v", result.Entries[1].Prints)
	}
}

func TestFormatPrintDurationPinnedShape(t *testing.T) {
	for input, want := range map[float64]string{9.9: "9", 61: "1:01", 3661: "1:01:01"} {
		if got := formatPrintDuration(input); got != want {
			t.Fatalf("formatPrintDuration(%v)=%q want=%q", input, got, want)
		}
	}
}
