package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/tejasa97/ytdlp-go/internal/extractor"
	mediaformat "github.com/tejasa97/ytdlp-go/internal/format"
	"github.com/tejasa97/ytdlp-go/internal/value"
)

func TestFormatNormalizationPreservesOriginalSelectedMetadata(t *testing.T) {
	info := value.NewInfo(value.NewObject(
		value.Field{Key: "http_headers", Value: value.ObjectValue(value.NewObject(
			value.Field{Key: "Referer", Value: value.String("https://page.example/video")},
		))},
		value.Field{Key: "formats", Value: value.List(value.ObjectValue(value.NewObject(
			value.Field{Key: "format_id", Value: value.String("a b")},
			value.Field{Key: "url", Value: value.String("https://cdn.example/video")},
			value.Field{Key: "ext", Value: value.String("mp4")},
			value.Field{Key: "protocol", Value: value.String("https")},
			value.Field{Key: "vcodec", Value: value.String("avc1")},
			value.Field{Key: "acodec", Value: value.String("aac")},
			value.Field{Key: "language", Value: value.String("nl")},
			value.Field{Key: "format_note", Value: value.String("source note")},
			value.Field{Key: "format", Value: value.String("extractor description")},
			value.Field{Key: "http_headers", Value: value.ObjectValue(value.NewObject(
				value.Field{Key: "User-Agent", Value: value.String("format-agent")},
			))},
		)))},
	))
	before, err := json.Marshal(info.Fields())
	if err != nil {
		t.Fatal(err)
	}
	selector, err := mediaformat.ParseSelector("a_b")
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := mediaformat.Prepare(info, mediaformat.Options{})
	if err != nil {
		t.Fatal(err)
	}
	plans, err := prepared.Plan(selector)
	if err != nil {
		t.Fatal(err)
	}
	selections := plans[0].Tracks
	if len(selections) != 1 || selections[0].ID != "a_b" {
		t.Fatalf("selections = %#v", selections)
	}
	if source, ok := selections[0].SourceFormatIndex(); !ok || source != 0 {
		t.Fatalf("source = %d, known = %v", source, ok)
	}
	if selections[0].Headers.Get("Referer") != "https://page.example/video" || selections[0].Headers.Get("User-Agent") != "format-agent" {
		t.Fatalf("headers = %v", selections[0].Headers)
	}
	selected := selectedFormatInfo(prepared.Info(), selections)
	checks := map[string]string{
		"format_id":   "a_b",
		"url":         "https://cdn.example/video",
		"protocol":    "https",
		"vcodec":      "avc1",
		"acodec":      "aac",
		"language":    "nl",
		"format_note": "source note",
		"format":      "extractor description",
	}
	for field, want := range checks {
		if got, _ := selected.Lookup(field).StringValue(); got != want {
			t.Fatalf("selected %s = %q, want %q", field, got, want)
		}
	}
	after, err := json.Marshal(info.Fields())
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatalf("original info mutated\nbefore=%s\nafter=%s", before, after)
	}
}

func TestFormatNormalizationMergedSourceOrder(t *testing.T) {
	info := value.NewInfo(value.NewObject(value.Field{Key: "formats", Value: value.List(
		value.ObjectValue(value.NewObject(
			value.Field{Key: "format_id", Value: value.String("video main")},
			value.Field{Key: "url", Value: value.String("https://cdn.example/video")},
			value.Field{Key: "ext", Value: value.String("mp4")},
			value.Field{Key: "vcodec", Value: value.String("avc1")},
			value.Field{Key: "acodec", Value: value.String("none")},
		)),
		value.ObjectValue(value.NewObject(
			value.Field{Key: "format_id", Value: value.String("audio main")},
			value.Field{Key: "url", Value: value.String("https://cdn.example/audio")},
			value.Field{Key: "ext", Value: value.String("m4a")},
			value.Field{Key: "vcodec", Value: value.String("none")},
			value.Field{Key: "acodec", Value: value.String("aac")},
		)),
	)}))
	selector, err := mediaformat.ParseSelector("video_main+audio_main")
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := mediaformat.Prepare(info, mediaformat.Options{})
	if err != nil {
		t.Fatal(err)
	}
	plans, err := prepared.Plan(selector)
	if err != nil {
		t.Fatal(err)
	}
	selections := plans[0].Tracks
	if len(selections) != 2 || selections[0].ID != "video_main" || selections[1].ID != "audio_main" {
		t.Fatalf("selections = %#v", selections)
	}
	for index, selection := range selections {
		if source, ok := selection.SourceFormatIndex(); !ok || source != index {
			t.Fatalf("selection[%d] source = %d, known = %v", index, source, ok)
		}
	}
	selected := selectedFormatInfo(prepared.Info(), selections)
	if got, _ := selected.Lookup("format_id").StringValue(); got != "video_main+audio_main" {
		t.Fatalf("merged format_id = %q", got)
	}
}

func TestFormatNormalizationCanonicalAcrossProductSurfaces(t *testing.T) {
	newInfo := func() value.Info {
		return value.NewInfo(value.NewObject(
			value.Field{Key: "id", Value: value.String("canonical")},
			value.Field{Key: "title", Value: value.String("Canonical")},
			value.Field{Key: "ext", Value: value.String("mp4")},
			value.Field{Key: "formats", Value: value.List(
				value.ObjectValue(value.NewObject(
					value.Field{Key: "format_id", Value: value.String("drm")},
					value.Field{Key: "url", Value: value.String("https://example.invalid/drm")},
					value.Field{Key: "ext", Value: value.String("mp4")},
					value.Field{Key: "vcodec", Value: value.String("avc1")},
					value.Field{Key: "acodec", Value: value.String("aac")},
					value.Field{Key: "has_drm", Value: value.Bool(true)},
				)),
				value.ObjectValue(value.NewObject(
					value.Field{Key: "format_id", Value: value.String("empty")},
					value.Field{Key: "url", Value: value.String("")},
					value.Field{Key: "ext", Value: value.String("mp4")},
				)),
				value.ObjectValue(value.NewObject(
					value.Field{Key: "format_id", Value: value.Int(7)},
					value.Field{Key: "url", Value: value.String("https://example.invalid/valid")},
					value.Field{Key: "ext", Value: value.String("mp4")},
					value.Field{Key: "vcodec", Value: value.String("avc1")},
					value.Field{Key: "acodec", Value: value.String("aac")},
					value.Field{Key: "height", Value: value.String("720")},
				)),
			)},
		))
	}
	assertJSON := func(t *testing.T, raw json.RawMessage) {
		t.Helper()
		var decoded struct {
			Formats []map[string]any `json:"formats"`
		}
		if err := json.Unmarshal(raw, &decoded); err != nil {
			t.Fatal(err)
		}
		if len(decoded.Formats) != 1 || decoded.Formats[0]["format_id"] != "7" || decoded.Formats[0]["height"] != float64(720) {
			t.Fatalf("canonical InfoJSON formats = %#v", decoded.Formats)
		}
	}

	t.Run("simulate-selection-print-table-and-json", func(t *testing.T) {
		request := Request{
			Simulate: true,
			Format:   "7",
			PrintRules: []PrintRule{
				{Stage: PrintPreProcess, Template: "%(formats_table)s"},
				{Stage: PrintAfterFilter, Template: "%(formats_table)s"},
				{Stage: PrintVideo, Template: "%(format_id)s|%(formats_table)s"},
			},
		}
		compatibility, err := prepareCompatibility(request)
		if err != nil {
			t.Fatal(err)
		}
		operation := &operation{client: newBroadTestClient(), request: request, compatibility: compatibility}
		result, err := operation.processMedia(context.Background(), extractor.Media(newInfo()), "fixture")
		if err != nil {
			t.Fatal(err)
		}
		assertJSON(t, result.InfoJSON)
		if len(result.Prints) != 3 {
			t.Fatalf("canonical prints = %#v", result.Prints)
		}
		for index := range 2 {
			if !strings.Contains(result.Prints[index].Text, "7  mp4") || strings.Contains(result.Prints[index].Text, "drm") || strings.Contains(result.Prints[index].Text, "empty") {
				t.Fatalf("canonical stage print[%d] = %#v", index, result.Prints[index])
			}
		}
		if !strings.HasPrefix(result.Prints[2].Text, "7|") || !strings.Contains(result.Prints[2].Text, "7  mp4") || strings.Contains(result.Prints[2].Text, "drm") || strings.Contains(result.Prints[2].Text, "empty") {
			t.Fatalf("canonical selected print/table = %#v", result.Prints[2])
		}
	})

	t.Run("match-filter-skip-json", func(t *testing.T) {
		request := Request{SkipDownload: true, MatchFilters: []string{"title=discarded"}}
		compatibility, err := prepareCompatibility(request)
		if err != nil {
			t.Fatal(err)
		}
		operation := &operation{client: newBroadTestClient(), request: request, compatibility: compatibility}
		result, err := operation.processMedia(context.Background(), extractor.Media(newInfo()), "fixture")
		if err != nil {
			t.Fatal(err)
		}
		if !result.Skipped {
			t.Fatalf("result was not skipped: %#v", result)
		}
		assertJSON(t, result.InfoJSON)
	})
}

func TestFormatNormalizationReplaceMetadataURLCoherentWithSelection(t *testing.T) {
	// Implicit top-level format: ReplaceMetadata mutates canonical url after
	// Prepare. InfoJSON and selection must observe the same URL.
	info := value.NewInfo(value.NewObject(
		value.Field{Key: "id", Value: value.String("implicit")},
		value.Field{Key: "title", Value: value.String("Implicit")},
		value.Field{Key: "url", Value: value.String("https://example.invalid/original")},
		value.Field{Key: "ext", Value: value.String("mp4")},
		value.Field{Key: "vcodec", Value: value.String("avc1")},
		value.Field{Key: "acodec", Value: value.String("aac")},
	))
	request := Request{
		Simulate:        true,
		Format:          "best",
		ReplaceMetadata: []string{`url:original:replaced`},
		PrintRules: []PrintRule{
			{Stage: PrintVideo, Template: "%(url)s"},
		},
	}
	compatibility, err := prepareCompatibility(request)
	if err != nil {
		t.Fatal(err)
	}
	operation := &operation{client: newBroadTestClient(), request: request, compatibility: compatibility}
	result, err := operation.processMedia(context.Background(), extractor.Media(info), "fixture")
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(result.InfoJSON, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded["url"] != "https://example.invalid/replaced" {
		t.Fatalf("InfoJSON url = %#v", decoded["url"])
	}
	if len(result.Prints) == 0 || result.Prints[len(result.Prints)-1].Text != "https://example.invalid/replaced" {
		t.Fatalf("selection/print url = %#v", result.Prints)
	}
	before, err := json.Marshal(info.Fields())
	if err != nil {
		t.Fatal(err)
	}
	// Extractor-owned info must remain unchanged.
	if url, _ := info.Lookup("url").StringValue(); url != "https://example.invalid/original" {
		t.Fatalf("extractor info mutated: %s", before)
	}
}

func TestFormatNormalizationErrorsCategorizedInternal(t *testing.T) {
	for _, sentinel := range []error{mediaformat.ErrInvalidFormats, mediaformat.ErrFormatLimit} {
		wrapped := fmt.Errorf("context: %w", sentinel)
		got := categorized("select format", wrapped)
		if !IsCategory(got, ErrorInternal) || !errors.Is(got, sentinel) {
			t.Fatalf("categorized(%v) = %v", sentinel, got)
		}
	}
}
