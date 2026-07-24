package sponsorblock

import (
	"crypto/sha256"
	"encoding/hex"
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
