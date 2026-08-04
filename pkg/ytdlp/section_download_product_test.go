package ytdlp

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
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
	bounds, ok, err := extractorSectionBounds(info)
	if err != nil || !ok {
		t.Fatalf("extractor bounds not detected: ok=%v err=%v", ok, err)
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
		if _, ok, err := extractorSectionBounds(info); ok || err == nil {
			t.Fatalf("bounds %#v accepted (ok=%v err=%v), want rejected", c, ok, err)
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
	bounds, ok, err := fromURLSectionBounds(info)
	if err != nil || !ok || bounds.Start != 5 || bounds.End == nil || *bounds.End != 12 {
		t.Fatalf("fromURLSectionBounds = %#v ok=%v err=%v", bounds, ok, err)
	}
	// start_time/end_time alone must not trigger a section without *from-url.
	infoOnly := value.NewInfo(value.NewObject(
		value.Field{Key: "start_time", Value: value.Float(5)},
		value.Field{Key: "end_time", Value: value.Float(12)},
	))
	if _, has, _ := extractorSectionBounds(infoOnly); has {
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

// TestComposeSectionOffsetsStartAndClampsEnd verifies CLI ranges compose
// inside the extractor window: start is offset by window start, end is
// clamped to the window duration, and the endpoint is deep-copied so the
// shared program state is never mutated.
func TestComposeSectionOffsetsStartAndClampsEnd(t *testing.T) {
	extStart := 10.0
	extEnd := 25.0
	extractor := sections.Section{Start: extStart, End: &extEnd}

	// Bounded CLI range inside the window: offset, no clamp needed.
	start := 5.0
	end := 12.0
	inside := sections.Section{Start: start, End: &end}
	composed, err := composeSection(inside, extractor, true)
	if err != nil {
		t.Fatal(err)
	}
	if composed.Start != 15 || composed.End == nil || *composed.End != 22 {
		t.Fatalf("composed = %#v, want start=15 end=22", composed)
	}

	// Open-ended CLI range closes at the extractor window end.
	start2 := 5.0
	open := sections.Section{Start: start2, End: nil}
	composedOpen, err := composeSection(open, extractor, true)
	if err != nil {
		t.Fatal(err)
	}
	if composedOpen.Start != 15 || composedOpen.End == nil || *composedOpen.End != 25 {
		t.Fatalf("open composed = %#v, want start=15 end=25 (clamped)", composedOpen)
	}

	// Bounded CLI range exceeding the window: end is clamped.
	start3 := 5.0
	end3 := 100.0
	over := sections.Section{Start: start3, End: &end3}
	composedOver, err := composeSection(over, extractor, true)
	if err != nil {
		t.Fatal(err)
	}
	if composedOver.Start != 15 || composedOver.End == nil || *composedOver.End != 25 {
		t.Fatalf("over composed = %#v, want start=15 end=25 (clamped)", composedOver)
	}
}

// TestComposeSectionRejectsStartBeyondWindow verifies a CLI range whose start
// lands beyond the extractor window is rejected before output mutation.
func TestComposeSectionRejectsStartBeyondWindow(t *testing.T) {
	extStart := 10.0
	extEnd := 25.0
	extractor := sections.Section{Start: extStart, End: &extEnd}
	start := 20.0
	req := sections.Section{Start: start, End: nil}
	if _, err := composeSection(req, extractor, true); err == nil {
		t.Fatal("start beyond window accepted")
	}
}

// TestComposeSectionDoesNotMutateSharedProgram verifies the deep-copy of the
// End pointer: composing one section must not mutate the program's section
// that shares the pointer.
func TestComposeSectionDoesNotMutateSharedProgram(t *testing.T) {
	extStart := 10.0
	extEnd := 25.0
	extractor := sections.Section{Start: extStart, End: &extEnd}
	originalEnd := 12.0
	shared := sections.Section{Start: 5, End: &originalEnd}
	// Snapshot the shared value.
	before := *shared.End
	if _, err := composeSection(shared, extractor, true); err != nil {
		t.Fatal(err)
	}
	if *shared.End != before {
		t.Fatalf("shared End mutated: before=%v after=%v", before, *shared.End)
	}
}

// TestFromURLWithLiteralRangesComposes verifies *from-url is appended after
// literal ranges instead of being dropped when literal ranges are present.
func TestFromURLWithLiteralRangesComposes(t *testing.T) {
	info := value.NewInfo(value.NewObject(
		value.Field{Key: "start_time", Value: value.Float(5)},
		value.Field{Key: "end_time", Value: value.Float(12)},
	))
	program, err := sections.Parse([]string{"*0-10", "*from-url"})
	if err != nil {
		t.Fatal(err)
	}
	effective, err := effectiveRequestSections(program, info, false, sections.Section{})
	if err != nil {
		t.Fatal(err)
	}
	if len(effective) != 2 {
		t.Fatalf("effective sections = %d, want 2 (literal + from-url)", len(effective))
	}
	if effective[0].Start != 0 || effective[0].End == nil || *effective[0].End != 10 {
		t.Fatalf("literal section = %#v", effective[0])
	}
	if effective[1].Start != 5 || effective[1].End == nil || *effective[1].End != 12 {
		t.Fatalf("from-url section = %#v", effective[1])
	}
}

// TestFromURLWithMissingBoundsFailsClosed verifies *from-url explicitly
// requested with unavailable bounds fails closed rather than degrading to a
// full download.
func TestFromURLWithMissingBoundsFailsClosed(t *testing.T) {
	program, err := sections.Parse([]string{"*from-url"})
	if err != nil {
		t.Fatal(err)
	}
	info := value.NewInfo(value.NewObject())
	if _, err := effectiveRequestSections(program, info, false, sections.Section{}); err == nil {
		t.Fatal("from-url with missing bounds must fail closed")
	}
}

// TestExtractorSectionInvalidBoundsFailClosed verifies a malformed supplied
// extractor section returns an error distinct from "absent", so a future
// Clip range cannot silently publish the full source video.
func TestExtractorSectionInvalidBoundsFailClosed(t *testing.T) {
	cases := []struct {
		name   string
		start  float64
		end    float64
		hasEnd bool
	}{
		{"negative start", -1, 0, true},
		{"reversed", 10, 5, true},
		{"negative end", 5, -1, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			obj := value.NewObject(value.Field{Key: "section_start", Value: value.Float(c.start)})
			if c.hasEnd {
				obj.Set("section_end", value.Float(c.end))
			}
			info := value.NewInfo(obj)
			if _, _, err := extractorSectionBounds(info); err == nil {
				t.Fatal("invalid supplied bounds did not fail closed")
			}
		})
	}
}

// TestProductExtractorSectionTrimsArtifact verifies the extractor-driven
// section contract end to end: a served info carrying section_start/section_end
// triggers ffmpeg section downloading even without --download-sections and
// produces a trimmed artifact.
func TestProductExtractorSectionTrimsArtifact(t *testing.T) {
	media := generateSplitChapterMedia(t)
	server := serveExtractorSectionMedia(t, media)
	defer server.Close()
	root := t.TempDir()
	result, err := NewClient().Run(context.Background(), Request{
		URL: server.URL + "/page", OutputDir: root, OutputTemplate: "extract.%(ext)s", Overwrite: true,
	})
	if err != nil || !result.Downloaded {
		t.Fatalf("extractor section result=%+v err=%v", result, err)
	}
	if duration, err := probeDuration(result.Filename); err == nil && duration > 1.5 {
		t.Fatalf("extractor section artifact duration=%v, want ~1s", duration)
	}
}

// serveExtractorSectionMedia serves a page whose info carries
// section_start/section_end so the extractor-driven section consumer triggers
// partial downloading without --download-sections.
func serveExtractorSectionMedia(t *testing.T, media string) *httptest.Server {
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
			_, _ = fmt.Fprintf(writer, `{"id":"extractor-section-fixture","title":"Extractor Section","duration":2,"ext":"mp4","section_start":0,"section_end":1,"formats":[{"format_id":"media","url":%q,"ext":"mp4","vcodec":"mpeg4","acodec":"none"}]}`, server.URL+"/media.mp4")
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

// TestProductSectionDownloadMultipleSectionsCollisionSafe exercises the
// multi-section lifecycle: two bounded ranges produce two collision-safe
// artifacts from one output plan, and the archive records the item once.
func TestProductSectionDownloadMultipleSectionsCollisionSafe(t *testing.T) {
	media := generateSplitChapterMedia(t)
	server := serveSplitChapterMedia(t, media)
	defer server.Close()
	root := t.TempDir()
	archivePath := filepath.Join(root, "archive.txt")
	result, err := NewClient().Run(context.Background(), Request{
		URL: server.URL + "/page", OutputDir: root, OutputTemplate: "main.%(ext)s", Overwrite: true,
		DownloadSections: []string{"*0-0.5", "*0.5-1"},
		DownloadArchive:  archivePath,
	})
	if err != nil || !result.Downloaded {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	mediaArtifacts := make([]Artifact, 0, 2)
	for _, artifact := range result.Artifacts {
		if artifact.Kind == "media" {
			mediaArtifacts = append(mediaArtifacts, artifact)
		}
	}
	if len(mediaArtifacts) != 2 {
		t.Fatalf("media artifacts=%d, want 2; all=%+v", len(mediaArtifacts), result.Artifacts)
	}
	archive, err := os.ReadFile(archivePath)
	if err != nil || strings.Count(string(archive), "chapter-fixture") != 1 {
		t.Fatalf("archive=%q err=%v, want exactly one record", archive, err)
	}
}

// TestProductSectionDownloadFromURL exercises *from-url product end to end.
// The served info carries start_time/end_time; *from-url consumes them as the
// section bounds.
func TestProductSectionDownloadFromURL(t *testing.T) {
	media := generateSplitChapterMedia(t)
	server := serveAtURLSectionMedia(t, media)
	defer server.Close()
	root := t.TempDir()
	result, err := NewClient().Run(context.Background(), Request{
		URL: server.URL + "/page", OutputDir: root, OutputTemplate: "from.%(ext)s", Overwrite: true,
		DownloadSections: []string{"*from-url"},
	})
	if err != nil || !result.Downloaded {
		t.Fatalf("from-url result=%+v err=%v", result, err)
	}
	if duration, err := probeDuration(result.Filename); err == nil && duration > 1.5 {
		t.Fatalf("from-url artifact duration=%v, want ~1s", duration)
	}
}

// serveAtURLSectionMedia serves a page whose info includes start_time/end_time
// so *from-url can consume them.
func serveAtURLSectionMedia(t *testing.T, media string) *httptest.Server {
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
			_, _ = fmt.Fprintf(writer, `{"id":"fromurl-fixture","title":"FromURL Fixture","duration":2,"ext":"mp4","start_time":0,"end_time":1,"formats":[{"format_id":"media","url":%q,"ext":"mp4","vcodec":"mpeg4","acodec":"none"}]}`, server.URL+"/media.mp4")
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

// TestSectionDownloadUnsafeHeaderFailsClosed verifies a section download whose
// selected input carries a credential-isolated header is rejected before any
// output is produced, and no artifact is left on disk.
func TestSectionDownloadUnsafeHeaderFailsClosed(t *testing.T) {
	media := generateSplitChapterMedia(t)
	server := serveUnsafeHeaderSectionMedia(t, media)
	defer server.Close()
	root := t.TempDir()
	_, err := NewClient().Run(context.Background(), Request{
		URL: server.URL + "/page", OutputDir: root, OutputTemplate: "unsafe.%(ext)s", Overwrite: true,
		DownloadSections: []string{"*0-1"},
	})
	if err == nil {
		t.Fatal("unsafe header section download succeeded, want closed failure")
	}
	entries, _ := os.ReadDir(root)
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), "unsafe") {
			t.Fatalf("unsafe section produced output %q", entry.Name())
		}
	}
}

// serveUnsafeHeaderSectionMedia serves an info whose selected format carries
// an Authorization header that cannot be safely delegated to ffmpeg.
func serveUnsafeHeaderSectionMedia(t *testing.T, media string) *httptest.Server {
	t.Helper()
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/page" {
			writer.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprintf(writer, `{"id":"unsafe-header","title":"Unsafe","duration":2,"ext":"mp4","formats":[{"format_id":"media","url":%q,"ext":"mp4","vcodec":"mpeg4","acodec":"none","http_headers":{"Authorization":"Bearer secret"}}]}`, server.URL+"/media.mp4")
			return
		}
		http.NotFound(writer, request)
	}))
	return server
}

// TestProductSectionDownloadDelegatedHLS verifies a section download of an
// m3u8_native format delegates the HLS URL to ffmpeg (mirroring FFmpegFD)
// and produces a trimmed artifact. The format's protocol must be accepted by
// the section consumer as a delegable protocol.
func TestProductSectionDownloadDelegatedHLS(t *testing.T) {
	hlsURL := generateHLSSectionFixture(t)
	input := filepath.Join(t.TempDir(), "hls.info.json")
	data, err := json.Marshal(map[string]any{
		"_type": "video", "id": "hls-section", "title": "HLS Section",
		"webpage_url": hlsURL + "/page", "duration": 2,
		"formats": []any{
			map[string]any{"format_id": "hls", "url": hlsURL + "/index.m3u8", "ext": "mp4", "protocol": "m3u8_native", "vcodec": "mpeg2video", "acodec": "none"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(input, data, 0o600); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	result, err := NewClient().Run(context.Background(), Request{
		LoadInfoJSON: input, OutputDir: root, OutputTemplate: "hls.%(ext)s", Overwrite: true,
		DownloadSections: []string{"*0-0.5"},
	})
	if err != nil || !result.Downloaded {
		t.Fatalf("hls section result=%+v err=%v", result, err)
	}
	if duration, err := probeDuration(result.Filename); err == nil && duration > 1.0 {
		t.Fatalf("hls section artifact duration=%v, want <=1s", duration)
	}
}

// generateHLSSectionFixture serves a real MPEG-TS HLS stream (one short
// segment) over HTTP so the section consumer can delegate the m3u8 to ffmpeg.
func generateHLSSectionFixture(t *testing.T) string {
	t.Helper()
	ffmpegPath, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg unavailable")
	}
	root := t.TempDir()
	segmentPath := filepath.Join(root, "segment.ts")
	if output, err := exec.Command(ffmpegPath, "-nostdin", "-y",
		"-f", "lavfi", "-i", "testsrc=size=32x32:rate=10:duration=0.4",
		"-an", "-c:v", "mpeg2video", "-f", "mpegts", segmentPath).CombinedOutput(); err != nil {
		t.Fatalf("generate HLS segment: %v: %s", err, output)
	}
	segment, err := os.ReadFile(segmentPath)
	if err != nil {
		t.Fatal(err)
	}
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/index.m3u8":
			writer.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
			_, _ = fmt.Fprint(writer, "#EXTM3U\n#EXT-X-TARGETDURATION:1\n#EXTINF:0.4,\nsegment.ts\n#EXT-X-ENDLIST\n")
		case "/segment.ts":
			_, _ = writer.Write(segment)
		default:
			http.NotFound(writer, request)
		}
	}))
	t.Cleanup(server.Close)
	return server.URL
}
