package sponsorblock

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestConformanceSampleResponseDecodeOnly(t *testing.T) {
	body, err := os.ReadFile(filepath.Join("testdata", "response.json"))
	if err != nil {
		t.Fatal(err)
	}
	groups, err := decodeResponse(body, "fixture0001")
	if err != nil {
		t.Fatal(err)
	}
	if len(groups) != 1 {
		t.Fatalf("got %d groups, want 1", len(groups))
	}
	segments := groups[0].Segments
	// Three raw segments, two surviving after the pinned
	// filter: the (0,0) marker is dropped.
	if len(segments) != 3 {
		t.Fatalf("got %d raw segments, want 3", len(segments))
	}
	chapters := Normalize(segments, 120)
	if len(chapters) != 2 {
		t.Fatalf("got %d chapters, want 2", len(chapters))
	}
	if chapters[0].Category != "sponsor" || chapters[0].Title != "Sponsor" {
		t.Fatalf("first chapter = %+v", chapters[0])
	}
	if chapters[1].Category != "poi_highlight" || chapters[1].Title != "Highlight" {
		t.Fatalf("second chapter = %+v", chapters[1])
	}
	if chapters[1].EndTime-chapters[1].StartTime != 1.5 {
		t.Fatalf("POI span = %v, want 1.5", chapters[1].EndTime-chapters[1].StartTime)
	}
}

func TestConformanceCollisionSelectsExactVideoID(t *testing.T) {
	body, err := os.ReadFile(filepath.Join("testdata", "collision.json"))
	if err != nil {
		t.Fatal(err)
	}
	groups, err := decodeResponse(body, "fixture-210")
	if err != nil {
		t.Fatal(err)
	}
	var matched []RawSegment
	for _, group := range groups {
		if group.VideoID == "fixture-210" {
			matched = group.Segments
		}
	}
	if matched == nil {
		t.Fatal("matching group not found in collision fixture")
	}
	chapters := Normalize(matched, 60)
	if len(chapters) != 1 {
		t.Fatalf("got %d chapters, want 1", len(chapters))
	}
	if chapters[0].StartTime != 10 || chapters[0].EndTime != 25 {
		t.Fatalf("chapter = %+v, want 10..25", chapters[0])
	}
	first, _ := hashPrefix("fixture-30")
	second, _ := hashPrefix("fixture-210")
	if first != "b200" || second != first {
		t.Fatalf("fixture IDs do not collide: %q != %q", first, second)
	}
}

func TestConformanceMalformedEnvelopeDropsInvalidSegments(t *testing.T) {
	body, err := os.ReadFile(filepath.Join("testdata", "malformed.json"))
	if err != nil {
		t.Fatal(err)
	}
	groups, err := decodeResponse(body, "fixture0001")
	if err != nil {
		t.Fatal(err)
	}
	if len(groups) != 1 {
		t.Fatalf("got %d groups, want 1", len(groups))
	}
	segments := groups[0].Segments
	if len(segments) != 1 {
		t.Fatalf("got %d segments, want 1 (malformed entries dropped)", len(segments))
	}
	chapters := Normalize(segments, 60)
	if len(chapters) != 1 {
		t.Fatalf("got %d chapters, want 1", len(chapters))
	}
}

func TestConformancePrefixIsSHA256FirstFour(t *testing.T) {
	const videoID = "fixture0001"
	sum := sha256.Sum256([]byte(videoID))
	want := hex.EncodeToString(sum[:])[:4]
	got, err := hashPrefix(videoID)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("prefix = %q, want %q", got, want)
	}
}

func TestConformanceArrangeMarkRemoveFixture(t *testing.T) {
	body, err := os.ReadFile(filepath.Join("testdata", "arrange_mark_remove.json"))
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Ordinary []struct {
			StartTime float64 `json:"start_time"`
			EndTime   float64 `json:"end_time"`
			Title     string  `json:"title"`
		} `json:"ordinary_chapters"`
		Sponsors []struct {
			StartTime float64 `json:"start_time"`
			EndTime   float64 `json:"end_time"`
			Category  string  `json:"category"`
			Title     string  `json:"title"`
			Remove    bool    `json:"remove"`
		} `json:"sponsor_chapters"`
		ExpectedCuts []struct {
			Start float64 `json:"start"`
			End   float64 `json:"end"`
		} `json:"expected_cuts"`
		ExpectedChapters []struct {
			StartTime float64 `json:"start_time"`
			EndTime   float64 `json:"end_time"`
			Title     string  `json:"title"`
			Sponsor   bool    `json:"sponsor"`
			Category  string  `json:"category"`
		} `json:"expected_chapters"`
	}
	if err := json.Unmarshal(body, &fixture); err != nil {
		t.Fatal(err)
	}
	input := make([]ArrangeChapter, 0, len(fixture.Ordinary)+len(fixture.Sponsors))
	for i, chapter := range fixture.Ordinary {
		input = append(input, ArrangeChapter{
			StartTime: chapter.StartTime, EndTime: chapter.EndTime, Title: chapter.Title, Source: i,
		})
	}
	for _, chapter := range fixture.Sponsors {
		input = append(input, ArrangeChapter{
			StartTime: chapter.StartTime, EndTime: chapter.EndTime, Title: chapter.Title,
			Remove: chapter.Remove, Source: -1,
			Categories: []CategorySpan{{
				Category: chapter.Category, Start: chapter.StartTime, End: chapter.EndTime, Title: chapter.Title,
			}},
		})
	}
	got, err := Arrange(input)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Cuts) != len(fixture.ExpectedCuts) {
		t.Fatalf("cuts=%#v", got.Cuts)
	}
	for i, cut := range fixture.ExpectedCuts {
		if got.Cuts[i].Start != cut.Start || got.Cuts[i].End != cut.End {
			t.Fatalf("cut[%d]=%#v want %#v", i, got.Cuts[i], cut)
		}
	}
	if len(got.Chapters) != len(fixture.ExpectedChapters) {
		t.Fatalf("chapters=%#v", got.Chapters)
	}
	for i, want := range fixture.ExpectedChapters {
		chapter := got.Chapters[i]
		if chapter.StartTime != want.StartTime || chapter.EndTime != want.EndTime ||
			chapter.Title != want.Title || chapter.Sponsor != want.Sponsor || chapter.Category != want.Category {
			t.Fatalf("chapter[%d]=%#v want %#v", i, chapter, want)
		}
	}
}

func TestConformanceDurationMismatchWarningFixture(t *testing.T) {
	body, err := os.ReadFile(filepath.Join("testdata", "duration_mismatch.json"))
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Duration                      float64      `json:"duration"`
		Segments                      []RawSegment `json:"segments"`
		ExpectDurationMismatchWarning bool         `json:"expect_duration_mismatch_warning"`
		ExpectedChapterCount          int          `json:"expected_chapter_count"`
	}
	if err := json.Unmarshal(body, &fixture); err != nil {
		t.Fatal(err)
	}
	got := NormalizeDetailed(fixture.Segments, fixture.Duration)
	if got.DurationMismatchFiltered != fixture.ExpectDurationMismatchWarning {
		t.Fatalf("mismatch=%v want %v", got.DurationMismatchFiltered, fixture.ExpectDurationMismatchWarning)
	}
	if len(got.Chapters) != fixture.ExpectedChapterCount {
		t.Fatalf("chapters=%d want %d", len(got.Chapters), fixture.ExpectedChapterCount)
	}
}
