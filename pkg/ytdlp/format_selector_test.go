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

func formatSelectorInfo() value.Info {
	format := func(id, ext, vcodec, acodec string, height int64) value.Value {
		return value.ObjectValue(value.NewObject(
			value.Field{Key: "format_id", Value: value.String(id)},
			value.Field{Key: "url", Value: value.String("https://example.invalid/" + id)},
			value.Field{Key: "ext", Value: value.String(ext)},
			value.Field{Key: "vcodec", Value: value.String(vcodec)},
			value.Field{Key: "acodec", Value: value.String(acodec)},
			value.Field{Key: "height", Value: value.Int(height)},
		))
	}
	return value.NewInfo(value.NewObject(value.Field{Key: "formats", Value: value.List(
		format("v1080", "mp4", "avc1", "none", 1080),
		format("v720", "mp4", "avc1", "none", 720),
		format("a128", "m4a", "none", "aac", 0),
		format("a64", "m4a", "none", "aac", 0),
		format("mux", "mp4", "avc1", "aac", 360),
	)}))
}

func TestFormatSelectorPrecedenceTraps(t *testing.T) {
	info := formatSelectorInfo()
	tests := []struct {
		name       string
		expression string
		wantIDs    []string
	}{
		{"merge before slash", "bestvideo+bestaudio/best", []string{"v1080", "a128"}},
		{"grouped merge before slash", "(bestvideo+bestaudio)/best", []string{"v1080", "a128"}},
		{"comma independent", "bestvideo,(bestaudio/worst)", []string{"v1080"}},
		{"group filter", "(bv*+ba)[height<=1080]", []string{"v1080", "a128"}},
		{"nth best", "best.2", []string{"v720"}},
		{"nth worst video", "worstvideo.2", []string{"v1080"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if test.name == "comma independent" {
				selector, err := mediaformat.ParseSelector(test.expression)
				if err != nil {
					t.Fatal(err)
				}
				plans, err := mediaformat.PlanSelect(info, selector)
				if err != nil || len(plans) != 2 {
					t.Fatalf("plans = %#v, %v", plans, err)
				}
				if plans[0].Tracks[0].ID != "v1080" || plans[1].Tracks[0].ID != "a128" {
					t.Fatalf("plans = %#v", plans)
				}
				return
			}
			selector, err := mediaformat.ParseSelector(test.expression)
			if err != nil {
				t.Fatal(err)
			}
			selected, err := mediaformat.Select(info, selector)
			if err != nil {
				t.Fatal(err)
			}
			if len(selected) != len(test.wantIDs) {
				t.Fatalf("selected = %#v", selected)
			}
			for index, want := range test.wantIDs {
				if selected[index].ID != want {
					t.Fatalf("selected[%d] = %q, want %q", index, selected[index].ID, want)
				}
			}
		})
	}
}

func TestFormatSelectorCommaNeverMergesInDownload(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = writer.Write([]byte("media-bytes"))
	}))
	defer server.Close()

	operation := &operation{
		client: NewClient(),
		request: Request{
			OutputDir: t.TempDir(),
			Overwrite: true,
			Format:    "video,audio",
		},
		compatibility: compatibilityPlan{
			formatOptions: mediaformat.Options{},
		},
	}
	transport, err := network.New(network.Config{})
	if err != nil {
		t.Fatal(err)
	}
	operation.transport = transport
	selector, err := mediaformat.ParseSelector(operation.request.Format)
	if err != nil {
		t.Fatal(err)
	}
	operation.compatibility.selector = &selector

	info := value.NewInfo(value.NewObject(
		value.Field{Key: "title", Value: value.String("comma")},
		value.Field{Key: "formats", Value: value.List(
			value.ObjectValue(value.NewObject(
				value.Field{Key: "format_id", Value: value.String("video")},
				value.Field{Key: "url", Value: value.String(server.URL + "/video")},
				value.Field{Key: "ext", Value: value.String("mp4")},
				value.Field{Key: "vcodec", Value: value.String("avc1")},
				value.Field{Key: "acodec", Value: value.String("none")},
			)),
			value.ObjectValue(value.NewObject(
				value.Field{Key: "format_id", Value: value.String("audio")},
				value.Field{Key: "url", Value: value.String(server.URL + "/audio")},
				value.Field{Key: "ext", Value: value.String("m4a")},
				value.Field{Key: "vcodec", Value: value.String("none")},
				value.Field{Key: "acodec", Value: value.String("aac")},
			)),
		)},
	))
	plans, err := operation.planFormats(info)
	if err != nil || len(plans) != 2 {
		t.Fatalf("plans = %#v, %v", plans, err)
	}
	base := filepath.Join(operation.request.OutputDir, "comma.mp4")
	var artifacts []Artifact
	for index, plan := range plans {
		path, _, downloadErr := operation.downloadSelections(context.Background(), plan.Tracks, operation.request.OutputDir, outputPlanDestination(base, plan, true), nil)
		if downloadErr != nil {
			t.Fatal(downloadErr)
		}
		artifacts = append(artifacts, Artifact{Path: path, Kind: "media"})
		if index == 0 && !strings.Contains(path, ".fvideo") {
			t.Fatalf("path = %q", path)
		}
	}
	if len(artifacts) != 2 {
		t.Fatalf("artifacts = %#v", artifacts)
	}
}

func TestFormatSelectorMultiOutputLegacyAPIFailsClosed(t *testing.T) {
	selector, err := mediaformat.ParseSelector("bestvideo,bestaudio")
	if err != nil {
		t.Fatal(err)
	}
	_, err = mediaformat.Select(formatSelectorInfo(), selector)
	if !errors.Is(err, mediaformat.ErrMultiOutput) {
		t.Fatalf("Select() = %v", err)
	}
}

func TestFormatSelectorMalformedInputs(t *testing.T) {
	for _, input := range []string{"", "bestvideo,,best", "+bestaudio", "bestvideo+", "/", "(best", "best)"} {
		if _, err := mediaformat.ParseSelector(input); !errors.Is(err, mediaformat.ErrInvalidSelector) {
			t.Fatalf("ParseSelector(%q) = %v", input, err)
		}
	}
}

func TestFormatSelectorDeterministicAcrossGoroutines(t *testing.T) {
	info := formatSelectorInfo()
	selector, err := mediaformat.ParseSelector("bestvideo+bestaudio/best")
	if err != nil {
		t.Fatal(err)
	}
	const workers = 8
	results := make(chan string, workers)
	for worker := 0; worker < workers; worker++ {
		go func() {
			selected, selectErr := mediaformat.Select(info, selector)
			if selectErr != nil {
				results <- selectErr.Error()
				return
			}
			results <- selected[0].ID + "+" + selected[1].ID
		}()
	}
	want := "v1080+a128"
	for worker := 0; worker < workers; worker++ {
		if got := <-results; got != want {
			t.Fatalf("worker result = %q, want %q", got, want)
		}
	}
}

func TestFormatSelectorExtensionAmbiguity(t *testing.T) {
	info := value.NewInfo(value.NewObject(value.Field{Key: "formats", Value: value.List(
		value.ObjectValue(value.NewObject(
			value.Field{Key: "format_id", Value: value.String("mp4")},
			value.Field{Key: "url", Value: value.String("https://example.invalid/mp4-id")},
			value.Field{Key: "ext", Value: value.String("mp4")},
			value.Field{Key: "vcodec", Value: value.String("avc1")},
			value.Field{Key: "acodec", Value: value.String("aac")},
		)),
		value.ObjectValue(value.NewObject(
			value.Field{Key: "format_id", Value: value.String("other")},
			value.Field{Key: "url", Value: value.String("https://example.invalid/other")},
			value.Field{Key: "ext", Value: value.String("mp4")},
			value.Field{Key: "vcodec", Value: value.String("avc1")},
			value.Field{Key: "acodec", Value: value.String("none")},
		)),
	)}))
	selector, err := mediaformat.ParseSelector("mp4")
	if err != nil {
		t.Fatal(err)
	}
	selected, err := mediaformat.Select(info, selector)
	if err != nil || len(selected) != 1 || selected[0].ID != "mp4" {
		t.Fatalf("direct ID = %#v, %v", selected, err)
	}
	selector, err = mediaformat.ParseSelector("mp4/best")
	if err != nil {
		t.Fatal(err)
	}
	selected, err = mediaformat.Select(info, selector)
	if err != nil || selected[0].ID != "mp4" {
		t.Fatalf("fallback = %#v, %v", selected, err)
	}
}

func TestFormatSelectorProductRollbackOnFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = writer.Write([]byte("ok-bytes"))
	}))
	defer server.Close()

	root := t.TempDir()
	destination := filepath.Join(root, "rollback.mp4")
	transport, err := network.New(network.Config{})
	if err != nil {
		t.Fatal(err)
	}
	operation := &operation{
		transport: transport,
		request:   Request{OutputDir: root, Overwrite: true},
	}
	plans := []mediaformat.OutputPlan{
		{Tracks: []mediaformat.Selection{{ID: "ok", URL: server.URL + "/ok", Ext: "mp4", VCodec: "avc1", ACodec: "aac"}}},
		{Tracks: []mediaformat.Selection{{ID: "bad", URL: "://missing-scheme", Ext: "mp4", VCodec: "avc1", ACodec: "aac"}}},
	}
	var published []string
	for index, plan := range plans {
		path, _, err := operation.downloadSelections(context.Background(), plan.Tracks, root, outputPlanDestination(destination, plan, true), nil)
		if err != nil {
			removePublishedPaths(published)
			if index == 0 {
				t.Fatalf("first download should succeed: %v", err)
			}
			if _, statErr := os.Stat(filepath.Join(root, "rollback.fok.mp4")); statErr == nil {
				t.Fatal("partial first output was not rolled back")
			}
			return
		}
		published = append(published, path)
	}
	t.Fatal("expected second download to fail")
}
