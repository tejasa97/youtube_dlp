package sponsorblock

import (
	"testing"
)

func TestNormalizeDetailedDurationMismatchWarningSignal(t *testing.T) {
	got := NormalizeDetailed([]RawSegment{
		{Segment: [2]float64{0, 0}, Category: "sponsor", ActionType: "skip", VideoDuration: 60},
		{Segment: [2]float64{5, 9}, Category: "sponsor", ActionType: "skip", VideoDuration: 67},
		{Segment: [2]float64{5, 9}, Category: "not-a-category", ActionType: "skip", VideoDuration: 60},
		{Segment: [2]float64{5, 9}, Category: "sponsor", ActionType: "skip", VideoDuration: 60},
	}, 60)
	if !got.DurationMismatchFiltered {
		t.Fatal("expected duration-mismatch signal")
	}
	if len(got.Chapters) != 1 {
		t.Fatalf("chapters = %#v", got.Chapters)
	}
	noWarn := NormalizeDetailed([]RawSegment{
		{Segment: [2]float64{0, 0}, Category: "sponsor", ActionType: "skip", VideoDuration: 60},
		{Segment: [2]float64{5, 9}, Category: "bad", ActionType: "skip", VideoDuration: 999},
		{Segment: [2]float64{5, 9}, Category: "sponsor", ActionType: "skip", VideoDuration: 0},
	}, 60)
	if noWarn.DurationMismatchFiltered {
		t.Fatal("did not expect warning for (0,0)/invalid/absent duration")
	}
}

func TestNormalizeZeroZeroDiscard(t *testing.T) {
	segments := []RawSegment{
		{Segment: [2]float64{0, 0}, Category: "sponsor", ActionType: "skip", VideoDuration: 60},
		{Segment: [2]float64{1, 5}, Category: "sponsor", ActionType: "skip", VideoDuration: 60},
	}
	got := Normalize(segments, 60)
	if len(got) != 1 {
		t.Fatalf("got %d chapters, want 1", len(got))
	}
	if got[0].StartTime != 0 || got[0].EndTime != 5 {
		t.Fatalf("chapter = %+v, want 0..5", got[0])
	}
}

func TestNormalizeStartSnapping(t *testing.T) {
	segments := []RawSegment{
		{Segment: [2]float64{0.7, 5}, Category: "sponsor", ActionType: "skip", VideoDuration: 60},
		{Segment: [2]float64{1, 5}, Category: "sponsor", ActionType: "skip", VideoDuration: 60},
		{Segment: [2]float64{2.5, 5}, Category: "sponsor", ActionType: "skip", VideoDuration: 60},
	}
	got := Normalize(segments, 60)
	if len(got) != 3 {
		t.Fatalf("got %d chapters, want 3", len(got))
	}
	if got[0].StartTime != 0 {
		t.Fatalf("first start = %v, want 0", got[0].StartTime)
	}
	if got[1].StartTime != 0 {
		t.Fatalf("second start = %v, want 0", got[1].StartTime)
	}
	if got[2].StartTime != 2.5 {
		t.Fatalf("third start = %v, want 2.5", got[2].StartTime)
	}
}

func TestNormalizePOIExtension(t *testing.T) {
	segments := []RawSegment{
		{Segment: [2]float64{10, 12}, Category: "poi_highlight", ActionType: "poi", VideoDuration: 60},
	}
	got := Normalize(segments, 60)
	if len(got) != 1 {
		t.Fatalf("got %d chapters, want 1", len(got))
	}
	if got[0].EndTime != 13 {
		t.Fatalf("end = %v, want 13", got[0].EndTime)
	}
	if got[0].Title != "Highlight" {
		t.Fatalf("title = %q, want Highlight", got[0].Title)
	}
}
