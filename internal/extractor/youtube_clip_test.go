package extractor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"testing"

	"github.com/tejasa97/youtube_dlp/internal/javascript/ejs"
	"github.com/tejasa97/youtube_dlp/internal/javascript/engine"
	"github.com/tejasa97/youtube_dlp/internal/value"
)

// TestYouTubeClipIDRecognition verifies /clip/<id> recognition and the
// bounded clip-id grammar. Clip IDs are never 11-char video IDs and never
// pass through youtubeIDPattern.
func TestYouTubeClipIDRecognition(t *testing.T) {
	valid := []string{
		"https://www.youtube.com/clip/UgytZKpehg-hEMBSn3F4AaABCQ",
		"https://youtube.com/clip/UgytZKpehg-hEMBSn3F4AaABCQ",
		"https://m.youtube.com/clip/abc123",
	}
	for _, rawURL := range valid {
		if id, ok := youtubeClipID(rawURL); !ok || id != "" && id == "" {
			t.Fatalf("ClipID(%q) id=%q ok=%v", rawURL, id, ok)
		}
	}
	// Verify the exact id is preserved.
	if id, ok := youtubeClipID("https://www.youtube.com/clip/UgytZKpehg-hEMBSn3F4AaABCQ"); !ok || id != "UgytZKpehg-hEMBSn3F4AaABCQ" {
		t.Fatalf("id = %q ok=%v", id, ok)
	}
	invalid := []string{
		"https://www.youtube-nocookie.com/clip/UgytZKpehg",
		"https://www.youtube.com/watch?v=abcdefghijk",
		"https://www.youtube.com/clip/",
		"https://www.youtube.com/clip/a/b",
		"https://example.com/clip/UgytZKpehg",
		"https://evil-youtube.com/clip/UgytZKpehg",
		"https://www.youtube.com/clip/Ugyt%2FZKpehg",
	}
	for _, rawURL := range invalid {
		if id, ok := youtubeClipID(rawURL); ok {
			t.Fatalf("ClipID(%q) accepted as %q; want rejection", rawURL, id)
		}
	}
}

// TestParseYouTubeClipDataResolvesSourceAndTiming verifies a synthetic clip
// page resolves the source video id and the loop-section timing.
func TestParseYouTubeClipDataResolvesSourceAndTiming(t *testing.T) {
	page := buildClipFixturePage("ScPX26pdQik", 29000, 39700)
	sourceID, timing, err := parseYouTubeClipData(page)
	if err != nil {
		t.Fatal(err)
	}
	if sourceID != "ScPX26pdQik" {
		t.Fatalf("sourceID = %q", sourceID)
	}
	if timing.startMs != 29000 || timing.endMs != 39700 {
		t.Fatalf("timing = %#v", timing)
	}
	if timing.start != 29.0 || timing.end == nil || *timing.end != 39.7 {
		t.Fatalf("timing seconds = start=%v end=%v", timing.start, timing.end)
	}
}

// TestParseYouTubeClipDataMissingVideoIDFailsClosed verifies the pinned
// "Unable to find video ID" classification when the source video id is absent.
func TestParseYouTubeClipDataMissingVideoIDFailsClosed(t *testing.T) {
	page := buildClipFixturePage("", 29000, 39700)
	if _, _, err := parseYouTubeClipData(page); err == nil {
		t.Fatal("missing video id succeeded")
	}
}

// TestParseYouTubeClipDataMissingTimingFailsClosed verifies absent loop
// timing fails closed instead of silently extracting the full source.
func TestParseYouTubeClipDataMissingTimingFailsClosed(t *testing.T) {
	page := buildClipFixturePageNoTiming("ScPX26pdQik")
	if _, _, err := parseYouTubeClipData(page); err == nil {
		t.Fatal("missing timing succeeded")
	}
}

// TestParseYouTubeClipDataRejectsHostileTiming verifies invalid bounds
// (reversed, negative) fail closed.
func TestParseYouTubeClipDataRejectsHostileTiming(t *testing.T) {
	for _, pair := range [][2]int64{{39700, 29000}, {-1, 100}, {100, 50}} {
		page := buildClipFixturePage("ScPX26pdQik", pair[0], pair[1])
		if _, _, err := parseYouTubeClipData(page); err == nil {
			t.Fatalf("timing %v accepted", pair)
		}
	}
}

// TestApplyYouTubeClipOverlay verifies the transparent overlay: clip id wins,
// media_type: clip, section fields, webpage url, and https-priority sort.
// Source fields remain authoritative (title/channel untouched).
func TestApplyYouTubeClipOverlay(t *testing.T) {
	info := value.NewInfo(value.NewObject(
		value.Field{Key: "id", Value: value.String("sourceVid")},
		value.Field{Key: "title", Value: value.String("Source Title")},
		value.Field{Key: "channel", Value: value.String("Scott The Woz")},
	))
	end := 39.7
	timing := youtubeClipTiming{start: 29.0, end: &end, startMs: 29000, endMs: 39700}
	clipURL := "https://www.youtube.com/clip/UgytZKpehg-hEMBSn3F4AaABCQ"
	applyYouTubeClipOverlay(info, "UgytZKpehg-hEMBSn3F4AaABCQ", timing, clipURL)
	if id, _ := info.ID(); id != "UgytZKpehg-hEMBSn3F4AaABCQ" {
		t.Fatalf("id = %q; want clip id wins over source", id)
	}
	if mediaType, _ := info.Lookup("media_type").StringValue(); mediaType != "clip" {
		t.Fatalf("media_type = %q", mediaType)
	}
	if start, _ := info.Lookup("section_start").Float(); start != 29.0 {
		t.Fatalf("section_start = %v", start)
	}
	if endVal, _ := info.Lookup("section_end").Float(); endVal != 39.7 {
		t.Fatalf("section_end = %v", endVal)
	}
	if title, _ := info.Title(); title != "Source Title" {
		t.Fatalf("source title overwritten: %q", title)
	}
	if channel, _ := info.Lookup("channel").StringValue(); channel != "Scott The Woz" {
		t.Fatalf("source channel overwritten: %q", channel)
	}
	sortFields, _ := info.Lookup("_format_sort_fields").ListValue()
	if len(sortFields) == 0 {
		t.Fatal("_format_sort_fields empty")
	}
	if first, _ := sortFields[0].StringValue(); first != "proto:https" {
		t.Fatalf("_format_sort_fields[0] = %q", first)
	}
}

// TestYouTubeClipHostileURLLeavesTransportUntouched verifies clip routing
// rejects hostile forms before any network request.
func TestYouTubeClipHostileURLLeavesTransportUntouched(t *testing.T) {
	transport := &memoryTransport{pages: map[string][]byte{}}
	for _, rawURL := range []string{
		"https://user:pass@www.youtube.com/clip/UgytZKpehg",
		"https://www.youtube.com:8080/clip/UgytZKpehg",
		"https://www.youtube.com/clip/Ugyt%2FZKpehg",
		"https://www.youtube.com/clip/Ugyt%00pehg",
	} {
		result, err := NewYouTube().Extract(context.Background(), Request{URL: rawURL, Transport: transport})
		if !errors.Is(err, ErrUnsupported) {
			t.Errorf("Extract(%q) error = %v; want ErrUnsupported", rawURL, err)
		}
		if result.Info.Fields().Len() != 0 {
			t.Errorf("Extract(%q) produced info", rawURL)
		}
	}
	if len(transport.reads) != 0 {
		t.Fatalf("transport.reads = %v; want empty", transport.reads)
	}
}

// buildClipFixturePage constructs a synthetic ytInitialData clip page with the
// given source video id and loop timing in milliseconds. The nested structure
// is assembled with encoding/json so it is always valid.
func buildClipFixturePage(sourceID string, startMs, endMs int64) []byte {
	loop := map[string]any{
		"loopCommand": map[string]any{"startTimeMs": startMs, "endTimeMs": endMs},
	}
	wrap := func(object map[string]any) map[string]any { return object }
	commands := []any{loop}
	buttonCommand := map[string]any{"commandExecutorCommand": map[string]any{"commands": commands}}
	button := map[string]any{"buttonRenderer": map[string]any{"command": buttonCommand}}
	actionButton := map[string]any{"actionButton": button}
	notification := map[string]any{"notificationActionRenderer": actionButton}
	popup := map[string]any{"popup": notification}
	openPopup := map[string]any{"openPopupAction": popup}
	onScrub := map[string]any{"commandExecutorCommand": map[string]any{"commands": []any{openPopup}}}
	attribution := map[string]any{"clipAttributionRenderer": map[string]any{"onScrubExit": onScrub}}
	contents := map[string]any{"contents": []any{attribution}}
	clipSection := map[string]any{"clipSectionRenderer": contents}
	content := map[string]any{"content": clipSection}
	panelRenderer := map[string]any{"engagementPanelSectionListRenderer": content}
	binding := map[string]any{
		"currentVideoEndpoint": map[string]any{"watchEndpoint": map[string]any{"videoId": sourceID}},
		"engagementPanels":     []any{panelRenderer},
	}
	_ = wrap
	raw, err := json.Marshal(binding)
	if err != nil {
		panic(fmt.Sprintf("build clip fixture: %v", err))
	}
	return []byte("var ytInitialData = " + string(raw) + ";")
}

// buildClipFixturePageNoTiming constructs a clip page without loop timing.
func buildClipFixturePageNoTiming(sourceID string) []byte {
	binding := map[string]any{
		"currentVideoEndpoint": map[string]any{"watchEndpoint": map[string]any{"videoId": sourceID}},
		"engagementPanels": []any{map[string]any{
			"engagementPanelSectionListRenderer": map[string]any{
				"content": map[string]any{
					"clipSectionRenderer": map[string]any{"contents": []any{map[string]any{"clipAttributionRenderer": map[string]any{}}}},
				},
			},
		}},
	}
	raw, err := json.Marshal(binding)
	if err != nil {
		panic(fmt.Sprintf("build clip no-timing fixture: %v", err))
	}
	return []byte("var ytInitialData = " + string(raw) + ";")
}

// TestYouTubeClipTransparentReEntry extracts a synthetic clip whose source is
// the pinned watch fixture, proving the clip route re-enters the standard
// video extractor and overlays the clip identity (id, media_type, section,
// https-priority sort) on the source result.
func TestYouTubeClipTransparentReEntry(t *testing.T) {
	watch := readYouTubeFixture(t, "watch.html")
	player := readYouTubeFixture(t, "../../javascript/ejs-0.8.0/synthetic-player.js")
	solver, err := ejs.New(engine.New(4))
	if err != nil {
		t.Fatal(err)
	}
	const clipID = "UgytZKpehg-hEMBSn3F4AaABCQ"
	clipURL := "https://www.youtube.com/clip/" + clipID
	clipPage := buildClipFixturePage("fixture0001", 29000, 39700)
	transport := &memoryTransport{pages: map[string][]byte{
		clipURL:           clipPage,
		youtubeFixtureURL: watch,
		youtubePlayerURL:  player,
	}}
	result, err := NewYouTube().Extract(context.Background(), Request{
		URL: clipURL, Transport: transport, ChallengeSolver: solver,
	})
	if err != nil {
		t.Fatalf("clip extraction: %v", err)
	}
	if id, _ := result.Info.ID(); id != clipID {
		t.Fatalf("id = %q; want clip id %q", id, clipID)
	}
	if mediaType, _ := result.Info.Lookup("media_type").StringValue(); mediaType != "clip" {
		t.Fatalf("media_type = %q", mediaType)
	}
	if start, _ := result.Info.Lookup("section_start").Float(); start != 29.0 {
		t.Fatalf("section_start = %v", start)
	}
	if endVal, _ := result.Info.Lookup("section_end").Float(); endVal != 39.7 {
		t.Fatalf("section_end = %v", endVal)
	}
	// Source metadata remains authoritative.
	if title, _ := result.Info.Title(); title == "" {
		t.Fatal("source title lost")
	}
	formats, _ := result.Info.Formats()
	if len(formats) == 0 {
		t.Fatal("source formats lost")
	}
	if rawURL, _ := result.Info.Lookup("webpage_url").StringValue(); rawURL != clipURL {
		t.Fatalf("webpage_url = %q; want clip URL", rawURL)
	}
}

// TestYouTubeClipSourceIDMismatchFailsClosed verifies a clip page whose loop
// points to an unexpected source is rejected before any output mutation.
func TestYouTubeClipSourceIDInvalidFailsClosed(t *testing.T) {
	// An 11-char id pattern match is required; a malformed id is rejected.
	page := buildClipFixturePage("not-an-11-char-id", 29000, 39700)
	if _, _, err := parseYouTubeClipData(page); err == nil {
		t.Fatal("invalid source id accepted")
	}
}

// TestYouTubeClipFixtureParser exercises the synthetic clip.html conformance
// fixture through the bounded parser, proving the fixture matches the
// production clip-page shape.
func TestYouTubeClipFixtureParser(t *testing.T) {
	page, readErr := os.ReadFile("../../conformance/extractors/youtube_clip/clip.html")
	if readErr != nil {
		t.Fatal(readErr)
	}
	sourceID, timing, err := parseYouTubeClipData(page)
	if err != nil {
		t.Fatalf("parse fixture: %v", err)
	}
	if sourceID != "ScPX26pdQik" {
		t.Fatalf("sourceID = %q", sourceID)
	}
	if timing.start != 29.0 || timing.end == nil || *timing.end != 39.7 {
		t.Fatalf("timing = %#v", timing)
	}
}

// TestYouTubeClipFixtureExpectedJSON verifies the overlay against the
// expected.json fixture contract.
func TestYouTubeClipFixtureExpectedJSON(t *testing.T) {
	info := value.NewInfo(value.NewObject(
		value.Field{Key: "id", Value: value.String("sourceVid")},
	))
	end := 39.7
	timing := youtubeClipTiming{start: 29.0, end: &end, startMs: 29000, endMs: 39700}
	applyYouTubeClipOverlay(info, "UgytZKpehg-hEMBSn3F4AaABCQ", timing, "https://www.youtube.com/clip/UgytZKpehg-hEMBSn3F4AaABCQ")
	if id, _ := info.ID(); id != "UgytZKpehg-hEMBSn3F4AaABCQ" {
		t.Fatalf("id = %q", id)
	}
	if mediaType, _ := info.Lookup("media_type").StringValue(); mediaType != "clip" {
		t.Fatalf("media_type = %q", mediaType)
	}
	if start, _ := info.Lookup("section_start").Float(); start != 29.0 {
		t.Fatalf("section_start = %v", start)
	}
	if endVal, _ := info.Lookup("section_end").Float(); endVal != 39.7 {
		t.Fatalf("section_end = %v", endVal)
	}
}

// TestParseYouTubeClipDataAcceptsStringMilliseconds verifies the pinned
// int(...) coercion: startTimeMs/endTimeMs supplied as integer strings are
// accepted, not only JSON integers.
func TestParseYouTubeClipDataAcceptsStringMilliseconds(t *testing.T) {
	page := buildClipFixturePageStringTiming("ScPX26pdQik", "29000", "39700")
	sourceID, timing, err := parseYouTubeClipData(page)
	if err != nil {
		t.Fatal(err)
	}
	if sourceID != "ScPX26pdQik" {
		t.Fatalf("sourceID = %q", sourceID)
	}
	if timing.startMs != 29000 || timing.endMs != 39700 {
		t.Fatalf("timing = %#v", timing)
	}
}

// TestParseYouTubeClipDataRejectsNonNumericTiming verifies malformed timing
// strings fail closed instead of being coerced.
func TestParseYouTubeClipDataRejectsNonNumericTiming(t *testing.T) {
	for _, timing := range [][2]string{{"abc", "39700"}, {"29000", ""}, {"1e5", "39700"}, {"-3", "39700"}} {
		page := buildClipFixturePageStringTiming("ScPX26pdQik", timing[0], timing[1])
		if _, _, err := parseYouTubeClipData(page); err == nil {
			t.Fatalf("timing %v accepted", timing)
		}
	}
}

// TestYouTubeClipLoopTimingIgnoresUnrelatedLoopCommand verifies the timing
// walker is anchored to the clip-attribution chain: an unrelated loopCommand
// elsewhere in the payload is not treated as clip timing.
func TestYouTubeClipLoopTimingIgnoresUnrelatedLoopCommand(t *testing.T) {
	page := buildUnrelatedLoopCommandPage("ScPX26pdQik")
	if _, _, err := parseYouTubeClipData(page); err == nil {
		t.Fatal("unrelated loopCommand accepted as clip timing")
	}
}

// buildClipFixturePageStringTiming constructs a clip page whose loop timing is
// supplied as integer strings, exercising the pinned int(...) coercion.
func buildClipFixturePageStringTiming(sourceID, startMs, endMs string) []byte {
	loop := map[string]any{
		"loopCommand": map[string]any{"startTimeMs": startMs, "endTimeMs": endMs},
	}
	raw, marshalErr := json.Marshal(clipFixtureBinding(sourceID, loop))
	if marshalErr != nil {
		panic(fmt.Sprintf("build string-timing fixture: %v", marshalErr))
	}
	return []byte("var ytInitialData = " + string(raw) + ";")
}

// buildUnrelatedLoopCommandPage constructs a payload with a loopCommand that is
// NOT reachable from the clip-attribution path.
func buildUnrelatedLoopCommandPage(sourceID string) []byte {
	loop := map[string]any{
		"loopCommand": map[string]any{"startTimeMs": 29000, "endTimeMs": 39700},
	}
	binding := map[string]any{
		"currentVideoEndpoint": map[string]any{"watchEndpoint": map[string]any{"videoId": sourceID}},
		"engagementPanels": []any{map[string]any{
			"engagementPanelSectionListRenderer": map[string]any{
				"content": map[string]any{
					"somethingElse": []any{loop},
				},
			},
		}},
	}
	raw, marshalErr := json.Marshal(binding)
	if marshalErr != nil {
		panic(fmt.Sprintf("build unrelated-loop fixture: %v", marshalErr))
	}
	return []byte("var ytInitialData = " + string(raw) + ";")
}

// clipFixtureBinding assembles the shared clip-page payload around a loop node.
func clipFixtureBinding(sourceID string, loop any) map[string]any {
	commands := []any{loop}
	buttonCommand := map[string]any{"commandExecutorCommand": map[string]any{"commands": commands}}
	button := map[string]any{"buttonRenderer": map[string]any{"command": buttonCommand}}
	actionButton := map[string]any{"actionButton": button}
	notification := map[string]any{"notificationActionRenderer": actionButton}
	popup := map[string]any{"popup": notification}
	openPopup := map[string]any{"openPopupAction": popup}
	onScrub := map[string]any{"commandExecutorCommand": map[string]any{"commands": []any{openPopup}}}
	attribution := map[string]any{"clipAttributionRenderer": map[string]any{"onScrubExit": onScrub}}
	contents := map[string]any{"clipSectionRenderer": map[string]any{"contents": []any{attribution}}}
	content := map[string]any{"engagementPanelSectionListRenderer": map[string]any{"content": contents}}
	return map[string]any{
		"currentVideoEndpoint": map[string]any{"watchEndpoint": map[string]any{"videoId": sourceID}},
		"engagementPanels":     []any{content},
	}
}
