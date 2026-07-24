package sponsorblock

import (
	"testing"
)

func TestNormalizeEmptyInput(t *testing.T) {
	got := Normalize(nil, 60)
	if len(got) != 0 {
		t.Fatalf("empty input produced %d chapters, want 0", len(got))
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
