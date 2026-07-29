package ytdlp

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"

	mediaformat "github.com/ytdlp-go/ytdlp/internal/format"
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
			value.Field{Key: "format_id", Value: value.String("22")},
			value.Field{Key: "format", Value: value.String("22 - 1280x720")},
		)),
	}
	lifecycle := newOutputLifecycleForPlan(0, plan, original, filepath.Join(t.TempDir(), "video.mp4"))
	if formatID, _ := lifecycle.Info.Lookup("format_id").StringValue(); formatID != "22" {
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

func TestAggregateLifecyclesPreservesPerOutputArtifacts(t *testing.T) {
	lifecycles := []outputLifecycle{
		{
			Index: 0, FinalPath: "/tmp/a.mp4", Downloaded: true,
			Artifacts: []Artifact{
				{Path: "/tmp/a.side.mp4", Kind: "sidecar"},
				{Path: "/tmp/a.mp4", Kind: "media"},
			},
			Prints: []PrintOutput{{Stage: PrintVideo, Text: "video-1"}},
		},
		{
			Index: 1, FinalPath: "/tmp/b.m4a", Downloaded: true,
			Artifacts: []Artifact{
				{Path: "/tmp/b.side.m4a", Kind: "sidecar"},
				{Path: "/tmp/b.m4a", Kind: "media"},
			},
			Prints: []PrintOutput{{Stage: PrintVideo, Text: "video-2"}},
		},
	}
	result := aggregateLifecycles(lifecycles)
	if len(result.Artifacts) != 4 {
		t.Fatalf("artifact count = %d", len(result.Artifacts))
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

func TestLifecycleInternalErrorIsCategorized(t *testing.T) {
	if err := errLifecycleInternal; err == nil {
		t.Fatal("expected sentinel")
	}
	if !strings.Contains(errLifecycleInternal.Error(), "output lifecycle") {
		t.Fatalf("sentinel text = %q", errLifecycleInternal.Error())
	}
}

func TestExecuteOutputLifecycleRejectsNilLifecycle(t *testing.T) {
	operation := &operation{}
	err := operation.executeOutputLifecycle(t.Context(), mediaTransaction{}, nil, nil)
	if err == nil {
		t.Fatal("expected error")
	}
	if !errors.Is(err, errLifecycleInternal) {
		t.Fatalf("err = %v", err)
	}
}
