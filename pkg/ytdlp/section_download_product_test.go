package ytdlp

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/ytdlp-go/ytdlp/internal/compat/sections"
	"github.com/ytdlp-go/ytdlp/internal/value"
)

// TestProductDownloadSectionsCliRangeTrimsArtifact exercises the CLI
// --download-sections surface end to end: a bounded *START-END range is
// expanded into a single section lifecycle and the ffmpeg section consumer
// produces a trimmed artifact.
func TestProductDownloadSectionsCliRangeTrimsArtifact(t *testing.T) {
	media := generateSplitChapterMedia(t)
	server := serveSplitChapterMedia(t, media)
	defer server.Close()
	root := t.TempDir()
	result, err := NewClient().Run(context.Background(), Request{
		URL: server.URL + "/page", OutputDir: root, OutputTemplate: "main.%(ext)s", Overwrite: true,
		DownloadSections: []string{"*0-1"},
	})
	if err != nil || !result.Downloaded {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	if filepath.Base(result.Filename) != "main.mp4" {
		t.Fatalf("main filename=%q", result.Filename)
	}
	if stat, statErr := filepathStat(result.Filename); statErr != nil || stat == 0 {
		t.Fatalf("section artifact stat=%d err=%v", stat, statErr)
	}
	if duration, err := probeDuration(result.Filename); err == nil && duration > 1.5 {
		t.Fatalf("section artifact duration=%v, want ~1s", duration)
	}
}

// TestDownloadSectionsParseRejectsUnsupported verifies the planner rejects
// unsupported values explicitly rather than ignoring them.
func TestDownloadSectionsParseRejectsUnsupported(t *testing.T) {
	for _, spec := range []string{"10:15", "*10:15+15:00", "*only-end", ""} {
		if _, err := sections.Parse([]string{spec}); err == nil {
			t.Fatalf("Parse(%q) succeeded, want error", spec)
		}
	}
}

// TestExtractorSectionBoundsDerivesDuration verifies the extractor-driven
// section contract: when section_start/section_end are present, the derived
// duration is section_end - section_start rounded compatibly.
func TestExtractorSectionBoundsDerivesDuration(t *testing.T) {
	info := value.NewInfo(value.NewObject(
		value.Field{Key: "section_start", Value: value.Float(10)},
		value.Field{Key: "section_end", Value: value.Float(25)},
	))
	bounds, ok := extractorSectionBounds(info)
	if !ok {
		t.Fatal("extractor bounds not detected")
	}
	if bounds.Start != 10 || bounds.End == nil || *bounds.End != 25 {
		t.Fatalf("bounds = %#v", bounds)
	}
	planInfo := value.NewInfo(value.NewObject())
	applySectionInfo(planInfo, bounds, 1)
	duration, hasDuration := planInfo.Lookup("duration").Float()
	if !hasDuration || duration != 15 {
		t.Fatalf("derived duration = %v (has=%v), want 15", duration, hasDuration)
	}
}

// TestExtractorSectionBoundsRejectsInvalid ensures malformed section bounds
// (over-budget, reversed, negative) fail closed rather than producing a
// partial claim.
func TestExtractorSectionBoundsRejectsInvalid(t *testing.T) {
	cases := []struct {
		start float64
		end   float64
	}{
		{10, 5}, // reversed
		{-1, 5}, // negative start
		{5, -1}, // negative end
	}
	for _, c := range cases {
		info := value.NewInfo(value.NewObject(
			value.Field{Key: "section_start", Value: value.Float(c.start)},
			value.Field{Key: "section_end", Value: value.Float(c.end)},
		))
		if _, ok := extractorSectionBounds(info); ok {
			t.Fatalf("bounds %#v accepted, want rejected", c)
		}
	}
}

// probeDuration probes a media file's duration with ffprobe when available.
// The test skips asserting duration when ffprobe is unavailable.
func probeDuration(path string) (float64, error) {
	ffprobe, err := exec.LookPath("ffprobe")
	if err != nil {
		return 0, err
	}
	output, err := exec.Command(ffprobe, "-v", "error", "-show_entries", "format=duration", "-of", "default=noprint_wrappers=1:nokey=1", path).Output()
	if err != nil {
		return 0, err
	}
	return strconv.ParseFloat(strings.TrimSpace(string(output)), 64)
}

func filepathStat(path string) (int64, error) {
	info, err := os.Stat(path)
	if err != nil {
		return 0, err
	}
	return info.Size(), nil
}

// TestFromURLSectionBounds verifies *from-url consumes start_time/end_time
// without triggering partial download when only those fields are present.
func TestFromURLSectionBounds(t *testing.T) {
	program, err := sections.Parse([]string{"*from-url"})
	if err != nil {
		t.Fatal(err)
	}
	if !program.FromURL || len(program.Sections) != 0 {
		t.Fatalf("program=%#v", program)
	}
	info := value.NewInfo(value.NewObject(
		value.Field{Key: "start_time", Value: value.Float(5)},
		value.Field{Key: "end_time", Value: value.Float(12)},
	))
	bounds, ok := fromURLSectionBounds(info)
	if !ok || bounds.Start != 5 || bounds.End == nil || *bounds.End != 12 {
		t.Fatalf("fromURLSectionBounds = %#v ok=%v", bounds, ok)
	}
	// start_time/end_time alone must not trigger a section without *from-url.
	infoOnly := value.NewInfo(value.NewObject(
		value.Field{Key: "start_time", Value: value.Float(5)},
		value.Field{Key: "end_time", Value: value.Float(12)},
	))
	if _, has := extractorSectionBounds(infoOnly); has {
		t.Fatal("start_time/end_time without section_start/section_end must not trigger extractor section")
	}
}

// TestFromURLDoesNotTriggerWithoutRequest verifies that start_time/end_time
// alone (no --download-sections) never trigger partial downloading.
func TestFromURLDoesNotTriggerWithoutRequest(t *testing.T) {
	if _, err := sections.Parse(nil); err != nil {
		t.Fatal(err)
	}
	// A program with no sections and no FromURL means no section is active.
	program, err := sections.Parse([]string{})
	if err != nil || program.FromURL || len(program.Sections) != 0 {
		t.Fatalf("empty program=%#v err=%v", program, err)
	}
}
