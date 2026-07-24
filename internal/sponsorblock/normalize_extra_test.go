package sponsorblock

import (
	"encoding/json"
	"math"
	"sort"
	"testing"
)

func TestNormalizeEndSnappingAndClamp(t *testing.T) {
	segments := []RawSegment{
		{Segment: [2]float64{5, 59.5}, Category: "sponsor", ActionType: "skip", VideoDuration: 60},
		{Segment: [2]float64{5, 70}, Category: "sponsor", ActionType: "skip", VideoDuration: 60},
		{Segment: [2]float64{5, 60.6}, Category: "sponsor", ActionType: "skip", VideoDuration: 60},
	}
	got := Normalize(segments, 60)
	if len(got) != 3 {
		t.Fatalf("got %d chapters, want 3", len(got))
	}
	if got[0].EndTime != 60 {
		t.Fatalf("first end = %v, want 60 (snap)", got[0].EndTime)
	}
	if got[1].EndTime != 60 {
		t.Fatalf("second end = %v, want 60 (clamp)", got[1].EndTime)
	}
	if got[2].EndTime != 60 {
		t.Fatalf("third end = %v, want 60 (clamp)", got[2].EndTime)
	}
}

func TestNormalizeDurationMismatchPolicy(t *testing.T) {
	// The pinned reference is: diff<1s or (diff<5s and diff/segment<5%).
	// Segment lengths here: 4 and 100.
	segments := []RawSegment{
		{Segment: [2]float64{5, 9}, Category: "sponsor", ActionType: "skip", VideoDuration: 60.4},       // diff 0.4 → keep
		{Segment: [2]float64{5, 9}, Category: "sponsor", ActionType: "skip", VideoDuration: 62},         // diff 2, 2/4=0.5 → drop
		{Segment: [2]float64{5, 105}, Category: "sponsor", ActionType: "skip", VideoDuration: 62},       // diff 2, 2/100=0.02 → keep
		{Segment: [2]float64{5, 9}, Category: "sponsor", ActionType: "skip", VideoDuration: 67},         // diff 7 → drop
		{Segment: [2]float64{5, 9}, Category: "sponsor", ActionType: "skip", VideoDuration: 0},          // absent/zero → keep
		{Segment: [2]float64{5, 9}, Category: "sponsor", ActionType: "skip", VideoDuration: math.NaN()}, // NaN → drop
	}
	got := Normalize(segments, 60)
	if len(got) != 3 {
		t.Fatalf("got %d chapters, want 3 (including missing reported duration)", len(got))
	}
}

func TestNormalizeSortingDeterministic(t *testing.T) {
	segments := []RawSegment{
		{Segment: [2]float64{30, 40}, Category: "sponsor", ActionType: "skip", VideoDuration: 60, UUID: "b"},
		{Segment: [2]float64{10, 20}, Category: "intro", ActionType: "skip", VideoDuration: 60, UUID: "z"},
		{Segment: [2]float64{10, 20}, Category: "sponsor", ActionType: "skip", VideoDuration: 60, UUID: "a"},
		{Segment: [2]float64{1, 5}, Category: "intro", ActionType: "skip", VideoDuration: 60, UUID: "z"},
	}
	got := Normalize(segments, 60)
	wantStarts := []float64{0, 10, 10, 30}
	if len(got) != len(wantStarts) {
		t.Fatalf("len = %d, want %d", len(got), len(wantStarts))
	}
	for index, chapter := range got {
		if chapter.StartTime != wantStarts[index] {
			t.Fatalf("chapter[%d].Start = %v, want %v", index, chapter.StartTime, wantStarts[index])
		}
	}
	// Equal coordinates retain source order rather than sorting by UUID.
	if got[1].Category != "intro" || got[2].Category != "sponsor" {
		t.Fatalf("equal coordinates lost source order: %+v then %+v", got[1], got[2])
	}
}

func TestNormalizePointPOIAndEmptyChapterTitle(t *testing.T) {
	got := Normalize([]RawSegment{
		{Segment: [2]float64{10, 10}, Category: "poi_highlight", ActionType: "poi", VideoDuration: 60},
		{Segment: [2]float64{20, 21}, Category: "chapter", ActionType: "chapter", VideoDuration: 0},
	}, 60)
	if len(got) != 2 || got[0].StartTime != 10 || got[0].EndTime != 11 {
		t.Fatalf("point POI = %+v", got)
	}
	if got[1].Title != "" {
		t.Fatalf("empty chapter description became %q", got[1].Title)
	}
}

func TestNormalizeRejectsMalformed(t *testing.T) {
	segments := []RawSegment{
		{Segment: [2]float64{-1, 5}, Category: "sponsor", ActionType: "skip", VideoDuration: 60},
		{Segment: [2]float64{10, 5}, Category: "sponsor", ActionType: "skip", VideoDuration: 60},
		{Segment: [2]float64{math.NaN(), 5}, Category: "sponsor", ActionType: "skip", VideoDuration: 60},
		{Segment: [2]float64{math.Inf(1), 5}, Category: "sponsor", ActionType: "skip", VideoDuration: 60},
		{Segment: [2]float64{1 << 32, 5}, Category: "sponsor", ActionType: "skip", VideoDuration: 60},
		{Segment: [2]float64{5, math.Inf(-1)}, Category: "sponsor", ActionType: "skip", VideoDuration: 60},
		{Segment: [2]float64{5, 1 << 32}, Category: "sponsor", ActionType: "skip", VideoDuration: 60},
		{Segment: [2]float64{1, 5}, Category: "sponsor", ActionType: "skip", VideoDuration: 60, Description: "valid"},
	}
	got := Normalize(segments, 60)
	if len(got) != 1 {
		t.Fatalf("got %d chapters, want 1", len(got))
	}
}

func TestNormalizeWithoutKnownDurationKeepsAll(t *testing.T) {
	segments := []RawSegment{
		{Segment: [2]float64{1, 5}, Category: "sponsor", ActionType: "skip", VideoDuration: 600},
		{Segment: [2]float64{10, 15}, Category: "sponsor", ActionType: "skip", VideoDuration: 600},
		{Segment: [2]float64{0, 0}, Category: "sponsor", ActionType: "skip", VideoDuration: 0},
	}
	got := Normalize(segments, 0)
	if len(got) != 2 {
		t.Fatalf("got %d, want 2", len(got))
	}
}

func TestNormalizeChapterTitleUsesDescription(t *testing.T) {
	segments := []RawSegment{
		{Segment: [2]float64{5, 6}, Category: "chapter", ActionType: "chapter", VideoDuration: 60, Description: "Introduction"},
	}
	got := Normalize(segments, 60)
	if len(got) != 1 {
		t.Fatalf("got %d, want 1", len(got))
	}
	if got[0].Title != "Introduction" {
		t.Fatalf("title = %q, want Introduction", got[0].Title)
	}
}

func TestNormalizeChapterJSONMatchesPinnedFields(t *testing.T) {
	segments := []RawSegment{
		{Segment: [2]float64{5, 6}, Category: "sponsor", ActionType: "skip", VideoDuration: 60},
	}
	got := Normalize(segments, 60)
	encoded, err := json.Marshal(got[0])
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]interface{}
	if err := json.Unmarshal(encoded, &fields); err != nil {
		t.Fatal(err)
	}
	want := []string{"category", "end_time", "start_time", "title", "type"}
	keys := make([]string, 0, len(fields))
	for key := range fields {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	if !equalStringSlices(keys, want) {
		t.Fatalf("keys = %v, want %v", keys, want)
	}
}

func equalStringSlices(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
