package hls

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestBuildDiscontinuityGroupsUsesAbsoluteIdentity(t *testing.T) {
	playlist := parseGroupTestPlaylist(t, "https://example.invalid/live.m3u8", `#EXTM3U
#EXT-X-MEDIA-SEQUENCE:40
#EXT-X-DISCONTINUITY-SEQUENCE:7
#EXTINF:1,
one.m4s
#EXT-X-DISCONTINUITY
#EXTINF:2,
two.m4s
#EXT-X-DISCONTINUITY
#EXTINF:3,
three.m4s
#EXT-X-ENDLIST
`)
	groups, err := BuildDiscontinuityGroups(playlist.Media)
	if err != nil {
		t.Fatal(err)
	}
	if len(groups) != 3 {
		t.Fatalf("groups=%#v", groups)
	}
	for index, wantSequence := range []int64{7, 8, 9} {
		if groups[index].Index != index || groups[index].ID.DiscontinuitySequence != wantSequence {
			t.Fatalf("group[%d]=%#v", index, groups[index])
		}
		if len(groups[index].Segments) != 1 || groups[index].Segments[0].Sequence != int64(40+index) {
			t.Fatalf("group[%d] segments=%#v", index, groups[index].Segments)
		}
	}

	delta := parseGroupTestPlaylist(t, "https://example.invalid/live.m3u8", `#EXTM3U
#EXT-X-MEDIA-SEQUENCE:41
#EXT-X-DISCONTINUITY-SEQUENCE:8
#EXTINF:2,
two.m4s
#EXT-X-ENDLIST
`)
	deltaGroups, err := BuildDiscontinuityGroups(delta.Media)
	if err != nil {
		t.Fatal(err)
	}
	if len(deltaGroups) != 1 || deltaGroups[0].Index != 0 || deltaGroups[0].ID.DiscontinuitySequence != 8 {
		t.Fatalf("delta groups=%#v", deltaGroups)
	}
}

func TestBuildDiscontinuityGroupsThroughLocalHTTPFixture(t *testing.T) {
	fixture, err := os.ReadFile(filepath.Join("testdata", "split-discontinuity.m3u8"))
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/split-discontinuity.m3u8" {
			http.NotFound(writer, request)
			return
		}
		writer.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
		_, _ = writer.Write(fixture)
	}))
	defer server.Close()

	response, err := http.Get(server.URL + "/split-discontinuity.m3u8")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, maxPlaylistBytes+1))
	if err != nil {
		t.Fatal(err)
	}
	playlist, err := Parse(response.Request.URL.String(), body)
	if err != nil {
		t.Fatal(err)
	}
	groups, err := BuildDiscontinuityGroups(playlist.Media)
	if err != nil {
		t.Fatal(err)
	}
	if len(groups) != 3 || groups[0].ID.DiscontinuitySequence != 7 || groups[1].ID.DiscontinuitySequence != 8 || groups[2].ID.DiscontinuitySequence != 9 {
		t.Fatalf("groups=%#v", groups)
	}
	if groups[1].Selectable || groups[1].MediaSegments != 0 || groups[1].AdvertisementSegments != 1 {
		t.Fatalf("ad-only group=%#v", groups[1])
	}
	if len(groups[0].MapTransitions) != 1 || len(groups[2].MapTransitions) != 1 || groups[0].MapTransitions[0].Map.URL != server.URL+"/init-a.mp4" {
		t.Fatalf("map transitions=%#v / %#v", groups[0].MapTransitions, groups[2].MapTransitions)
	}

	plan, err := BuildDefaultDiscontinuitySelectionPlan(playlist.Media)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Group.ID.DiscontinuitySequence != 7 || len(plan.Segments) != 1 || plan.Segments[0].URL != server.URL+"/media-100.m4s" {
		t.Fatalf("default plan=%#v", plan)
	}
	if len(plan.Group.Segments) != 1 || plan.Group.Segments[0].Advertisement {
		t.Fatalf("default group=%#v", plan.Group)
	}

	selected, err := SelectDiscontinuityGroup(playlist.Media, DiscontinuityGroupID{DiscontinuitySequence: 9})
	if err != nil {
		t.Fatal(err)
	}
	if selected.Group.Index != 2 || selected.Group.ID.DiscontinuitySequence != 9 || len(selected.Segments) != 1 || selected.Segments[0].URL != server.URL+"/media-102.m4s" {
		t.Fatalf("selected plan=%#v", selected)
	}
	if _, err := SelectDiscontinuityGroup(playlist.Media, DiscontinuityGroupID{DiscontinuitySequence: 8}); !errors.Is(err, ErrNoSelectableGroup) {
		t.Fatalf("ad-only selection error=%v", err)
	}
}

func TestDiscontinuitySelectionCanonicalizesPartsAndCompleteSegments(t *testing.T) {
	playlist := parseGroupTestPlaylist(t, "https://example.invalid/live.m3u8", `#EXTM3U
#EXT-X-MEDIA-SEQUENCE:50
#EXT-X-MAP:URI="init.mp4"
#EXT-X-PART:DURATION=0.25,URI="part-0.m4s"
#EXT-X-PART:DURATION=0.25,URI="part-1.m4s"
#EXTINF:1,
complete-50.m4s
#EXT-X-ENDLIST
`)
	groups, err := BuildDiscontinuityGroups(playlist.Media)
	if err != nil {
		t.Fatal(err)
	}
	if len(groups) != 1 || len(groups[0].Segments) != 1 {
		t.Fatalf("canonical groups=%#v", groups)
	}
	segment := groups[0].Segments[0]
	if segment.Partial || segment.PartIndex != 0 || segment.URL != "https://example.invalid/complete-50.m4s" {
		t.Fatalf("canonical segment=%#v", segment)
	}
	if groups[0].PartialSegments != 0 || groups[0].MediaSegments != 1 || groups[0].Duration != time.Second {
		t.Fatalf("canonical summary=%#v", groups[0])
	}
}

func TestBuildDiscontinuityGroupsRejectsRepeatedBoundaryIdentity(t *testing.T) {
	media := &MediaPlaylist{Segments: []Segment{
		{Sequence: 1, DiscontinuitySequence: 2, Duration: time.Second},
		{Sequence: 2, DiscontinuitySequence: 3, Duration: time.Second, Discontinuity: true},
		{Sequence: 3, DiscontinuitySequence: 2, Duration: time.Second},
	}}
	if _, err := BuildDiscontinuityGroups(media); !errors.Is(err, ErrInvalidDiscontinuityGroups) {
		t.Fatalf("error=%v", err)
	}
}

func TestDiscontinuitySelectionRejectsConflictingPhysicalDuplicates(t *testing.T) {
	for name, segments := range map[string][]Segment{
		"part": {
			{Sequence: 1, DiscontinuitySequence: 0, Partial: true, PartIndex: 0, URL: "https://example.invalid/part-a.m4s"},
			{Sequence: 1, DiscontinuitySequence: 0, Partial: true, PartIndex: 0, URL: "https://example.invalid/part-b.m4s"},
		},
		"complete": {
			{Sequence: 1, DiscontinuitySequence: 0, URL: "https://example.invalid/segment-a.m4s"},
			{Sequence: 1, DiscontinuitySequence: 0, URL: "https://example.invalid/segment-b.m4s"},
		},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := BuildDiscontinuityGroups(&MediaPlaylist{Segments: segments}); !errors.Is(err, ErrInvalidDiscontinuityGroups) {
				t.Fatalf("error=%v", err)
			}
		})
	}
}

func FuzzBuildDiscontinuityGroups(f *testing.F) {
	f.Add([]byte{0, 1, 2, 3})
	f.Add([]byte{7, 7, 1, 255})
	f.Fuzz(func(t *testing.T, raw []byte) {
		if len(raw) > 256 {
			t.Skip()
		}
		segments := make([]Segment, len(raw))
		for index, value := range raw {
			segments[index] = Segment{
				URL:                   "https://example.invalid/segment.m4s",
				Sequence:              int64(index),
				DiscontinuitySequence: int64(value % 8),
				Duration:              time.Duration(value%4) * time.Millisecond,
				Partial:               value&1 != 0,
				PartIndex:             index,
				Advertisement:         value&2 != 0,
			}
		}
		_, _ = BuildDiscontinuityGroups(&MediaPlaylist{Segments: segments})
	})
}

func parseGroupTestPlaylist(t *testing.T, rawURL, input string) Playlist {
	t.Helper()
	playlist, err := Parse(rawURL, []byte(input))
	if err != nil {
		t.Fatal(err)
	}
	if playlist.Media == nil {
		t.Fatal("playlist has no media")
	}
	return playlist
}
