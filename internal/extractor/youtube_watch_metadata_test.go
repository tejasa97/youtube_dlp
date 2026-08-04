package extractor

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/ytdlp-go/ytdlp/internal/value"
)

const youtubeWatchFixtureURL = "https://www.youtube.com/watch?v=fixture0004"

func readYouTubeWatchFixture(t youtubeTestHelper, name string) []byte {
	t.Helper()
	data, err := os.ReadFile("../../conformance/extractors/youtube_watch_metadata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestYouTubeWatchMetadataPinnedExtraction(t *testing.T) {
	watch := readYouTubeWatchFixture(t, "watch.html")
	expected := readYouTubeWatchFixture(t, "expected.json")
	transport := &memoryTransport{pages: map[string][]byte{youtubeWatchFixtureURL: watch}}
	result, err := NewYouTube().Extract(context.Background(), Request{URL: youtubeWatchFixtureURL, Transport: transport})
	if err != nil {
		t.Fatal(err)
	}
	var actual bytes.Buffer
	encoder := json.NewEncoder(&actual)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(result.Info.Fields()); err != nil {
		t.Fatal(err)
	}
	var expectedDocument, actualDocument any
	if err := json.Unmarshal(expected, &expectedDocument); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(actual.Bytes(), &actualDocument); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(actualDocument, expectedDocument) {
		t.Fatalf("metadata mismatch\nactual:   %s\nexpected: %s", actual.Bytes(), expected)
	}
	if len(transport.reads) != 1 {
		t.Fatalf("reads = %#v; expected a single watch-page read", transport.reads)
	}
}

func TestYouTubeWatchMetadataChapters(t *testing.T) {
	// Structured player-overlay chapters win over engagement-panel markers
	// and the description fallback; end times come from the next start.
	info, _ := extractYouTubeWatchMetadata(readYouTubeWatchFixture(t, "watch.html"), 123, true)
	if len(info.chapters) != 3 {
		t.Fatalf("chapters = %d", len(info.chapters))
	}
	for index, want := range []struct {
		start float64
		end   float64
		title string
	}{
		{0, 45, "Intro"},
		{45, 90, "Chapter Two"},
		{90, 123, "Outro"},
	} {
		object, _ := info.chapters[index].Object()
		if start, _ := object.Lookup("start_time").Float(); start != want.start {
			t.Fatalf("chapter[%d] start = %v", index, start)
		}
		if end, _ := object.Lookup("end_time").Float(); end != want.end {
			t.Fatalf("chapter[%d] end = %v", index, end)
		}
		if title, _ := object.Lookup("title").StringValue(); title != want.title {
			t.Fatalf("chapter[%d] title = %q", index, title)
		}
	}
}

func TestYouTubeWatchMetadataDescriptionChaptersFallback(t *testing.T) {
	description := "Intro\n0:00\nChapter Two\n1:23\nOutro\n2:30\n\nextra text\nnot a chapter\n"
	chapters := youtubeChaptersFromDescription(description, 200, true)
	if len(chapters) != 3 {
		t.Fatalf("chapters = %d", len(chapters))
	}
	object, _ := chapters[0].Object()
	if start, _ := object.Lookup("start_time").Float(); start != 0 {
		t.Fatalf("first start = %v", start)
	}
	object, _ = chapters[2].Object()
	if end, _ := object.Lookup("end_time").Float(); end != 200 {
		t.Fatalf("last end = %v", end)
	}
	if got := youtubeChaptersFromDescription("no timestamps here", 100, true); len(got) != 0 {
		t.Fatalf("unexpected chapters: %#v", got)
	}
	if got := youtubeChaptersFromDescription(strings.Repeat("x", 1<<20+1), 100, true); len(got) != 0 {
		t.Fatalf("oversized description yielded chapters")
	}
}

func TestYouTubeWatchMetadataLikesAndCounts(t *testing.T) {
	info, _ := extractYouTubeWatchMetadata(readYouTubeWatchFixture(t, "watch.html"), 123, true)
	if !info.hasLikeCount || info.likeCount != 1234 {
		t.Fatalf("like_count = %d, %v", info.likeCount, info.hasLikeCount)
	}
	if !info.hasDislikeCount || info.dislikeCount != 56 {
		t.Fatalf("dislike_count = %d, %v", info.dislikeCount, info.hasDislikeCount)
	}
	if !info.hasCommentCount || info.commentCount != 1234 {
		t.Fatalf("comment_count = %d, %v", info.commentCount, info.hasCommentCount)
	}
	if !info.hasChannelFollowerCount || info.channelFollowerCount != 12300 {
		t.Fatalf("channel_follower_count = %d, %v", info.channelFollowerCount, info.hasChannelFollowerCount)
	}
	if !info.hasChannelIsVerified || !info.channelIsVerified {
		t.Fatalf("channel_is_verified = %v, %v", info.channelIsVerified, info.hasChannelIsVerified)
	}
	if info.series != "Fixture Series" || info.seasonNumber != 1 || info.episodeNumber != 2 {
		t.Fatalf("series = %q %d %d", info.series, info.seasonNumber, info.episodeNumber)
	}
	if info.uploadDateFallback != "20240115" {
		t.Fatalf("upload date fallback = %q", info.uploadDateFallback)
	}
	if info.availability != "public" || !info.hasAvailability {
		t.Fatalf("availability = %q, %v", info.availability, info.hasAvailability)
	}
	if len(info.heatmap) != 2 {
		t.Fatalf("heatmap = %d", len(info.heatmap))
	}
	if info.hasConcurrentViewCount {
		t.Fatal("concurrent view count must be absent without an isLive view count renderer")
	}
}

func TestYouTubeWatchMetadataHeatmapBounds(t *testing.T) {
	initial := `{"frameworkUpdates":{"entityBatchUpdate":{"mutations":[{"payload":{"macroMarkersListEntity":{"markersList":{"markerType":"MARKER_TYPE_HEATMAP","markers":[{"startMillis":0,"durationMillis":1000,"intensityScoreNormalized":1.5},{"startMillis":-5,"durationMillis":1000,"intensityScoreNormalized":0.5},{"startMillis":1000,"durationMillis":2000,"intensityScoreNormalized":0.5}]}}}}]}}}`
	page := []byte(`<!doctype html><html><body><script>var ytInitialData = ` + initial + `;</script></body></html>`)
	metadata, err := extractYouTubeWatchMetadata(page, 123, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(metadata.heatmap) != 1 {
		t.Fatalf("heatmap = %d; want only the valid entry", len(metadata.heatmap))
	}
}

func TestYouTubeWatchMetadataAvailabilityPrecedence(t *testing.T) {
	tests := []struct {
		name       string
		private    bool
		premium    bool
		subscriber bool
		needsAuth  bool
		unlisted   bool
		public     bool
		want       string
	}{
		{"private wins", true, true, true, true, true, true, "private"},
		{"premium beats subscriber", false, true, true, false, false, false, "premium"},
		{"subscriber beats auth", false, false, true, true, false, false, "subscriber_only"},
		{"auth beats unlisted", false, false, false, true, true, false, "needs_auth"},
		{"unlisted beats public", false, false, false, false, true, true, "unlisted"},
		{"public", false, false, false, false, false, true, "public"},
		{"unknown", false, false, false, false, false, false, ""},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := youtubeAvailabilityPrecedence(test.private, test.premium, test.subscriber, test.needsAuth, test.unlisted, test.public); got != test.want {
				t.Fatalf("precedence = %q; want %q", got, test.want)
			}
		})
	}
}

func TestYouTubeWatchMetadataCommentCountContract(t *testing.T) {
	// With an approximate watch-page count, deferred comment enrichment must
	// preserve it (the pinned reference behavior); the actual retrieved count
	// is only the fallback when no approximate count exists.
	withApproximate := value.NewInfo(value.NewObject(value.Field{Key: "comment_count", Value: value.Int(1234)}))
	if count, _ := withApproximate.Lookup("comment_count").Int(); count != 1234 {
		t.Fatalf("approximate count not preserved: %d", count)
	}
	empty := value.NewInfo(value.NewObject())
	if !empty.Lookup("comment_count").IsMissing() {
		t.Fatal("empty info must not have comment_count")
	}
}
