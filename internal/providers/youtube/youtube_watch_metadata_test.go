package youtube

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/tejasa97/ytdlp-go/internal/value"
)

const youtubeWatchFixtureURL = "https://www.youtube.com/watch?v=fixture0004"

func readYouTubeWatchFixture(t youtubeTestHelper, name string) []byte {
	t.Helper()
	data, err := os.ReadFile("../../../conformance/extractors/youtube_watch_metadata/" + name)
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

func TestYouTubeWatchMetadataFirstCollaboratorFollowerCount(t *testing.T) {
	initial := `{"contents":{"twoColumnWatchNextResults":{"results":{"results":{"contents":[{"videoSecondaryInfoRenderer":{"owner":{"videoOwnerRenderer":{"attributedTitle":{"commandRuns":[{"onTap":{"innertubeCommand":{"showDialogCommand":{"panelLoadingStrategy":{"inlineContent":{"dialogViewModel":{"customContent":{"listViewModel":{"listItems":[{"listItemViewModel":{"rendererContext":{"accessibilityContext":{"label":"First Collaborator, 1.23M subscribers"}}}},{"listItemViewModel":{"rendererContext":{"accessibilityContext":{"label":"Second Collaborator, 456K subscribers"}}}}]}}}}}}}}}]}}}}}]}}}}}`
	page := []byte(`<!doctype html><html><body><script>var ytInitialData = ` + initial + `;</script></body></html>`)
	metadata, err := extractYouTubeWatchMetadata(page, 0, false)
	if err != nil {
		t.Fatal(err)
	}
	if !metadata.hasChannelFollowerCount || metadata.channelFollowerCount != 1_230_000 {
		t.Fatalf("channel_follower_count=%d present=%t", metadata.channelFollowerCount, metadata.hasChannelFollowerCount)
	}
}

func TestYouTubeCollaboratorSubscriberLabelRejectsAmbiguity(t *testing.T) {
	for _, test := range []struct {
		label string
		want  int64
		ok    bool
	}{
		{"12.3K subscribers", 12_300, true},
		{"Channel, 1.2M subscribers", 1_200_000, true},
		{"Channel, 1.2M subscribers, 2M subscribers", 0, false},
		{"Channel, 1.2M followers", 0, false},
		{"Channel 1.2M subscribers", 1_200_000, true},
		{"Channel2 1.2M subscribers", 0, false},
		{"Channel,1.2M subscribers", 0, false},
	} {
		got, ok := youtubeCollaboratorSubscriberLabel(test.label)
		if got != test.want || ok != test.ok {
			t.Fatalf("%q => %d,%t want %d,%t", test.label, got, ok, test.want, test.ok)
		}
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
		{"premium_only beats subscriber", false, true, true, false, false, false, "premium_only"},
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

// TestYouTubeWatchMetadataDescriptionChaptersBothOrders reproduces the
// previously-broken case where a standard three-chapter description in the
// common "0:00 Title" order produced only one malformed chapter. The pinned
// parser accepts both "<title>\\n<timestamp>" and "<timestamp> <title>"
// orders, so a description using either form yields all three chapters.
func TestYouTubeWatchMetadataDescriptionChaptersBothOrders(t *testing.T) {
	cases := []struct {
		name        string
		description string
		wantTitles  []string
		wantStarts  []float64
	}{
		{
			name:        "title-first order",
			description: "Intro\n0:00\nChapter Two\n1:23\nOutro\n2:30\n",
			wantTitles:  []string{"Intro", "Chapter Two", "Outro"},
			wantStarts:  []float64{0, 83, 150},
		},
		{
			name:        "time-first order",
			description: "0:00 Intro\n1:23 Chapter Two\n2:30 Outro\n",
			wantTitles:  []string{"Intro", "Chapter Two", "Outro"},
			wantStarts:  []float64{0, 83, 150},
		},
		{
			name:        "mixed order",
			description: "Intro\n0:00\n1:23 Chapter Two\nOutro\n2:30\n",
			wantTitles:  []string{"Intro", "Chapter Two", "Outro"},
			wantStarts:  []float64{0, 83, 150},
		},
		{
			// "<title> <timestamp>" on the same line.
			name:        "title-then-time same line",
			description: "Intro 0:00\nChapter Two 1:23\nOutro 2:30\n",
			wantTitles:  []string{"Intro", "Chapter Two", "Outro"},
			wantStarts:  []float64{0, 83, 150},
		},
		{
			// Preceding prose must not anchor a chapter when the
			// timestamp line carries its own same-line title.
			name:        "preceding prose not attributed",
			description: "Welcome to the video\n0:00 Intro\n1:23 Next\n",
			wantTitles:  []string{"Intro", "Next"},
			wantStarts:  []float64{0, 83},
		},
		{
			// Non-Latin scripts (Japanese, Cyrillic, Arabic, CJK)
			// must be accepted; the previously-broken ASCII-only
			// check dropped every non-Latin title.
			name:        "Japanese hiragana",
			description: "0:00 はじめに\n1:23 次の章\n",
			wantTitles:  []string{"はじめに", "次の章"},
			wantStarts:  []float64{0, 83},
		},
		{
			name:        "Cyrillic Russian",
			description: "0:00 Введение\n1:23 Следующая глава\n",
			wantTitles:  []string{"Введение", "Следующая глава"},
			wantStarts:  []float64{0, 83},
		},
		{
			name:        "Arabic",
			description: "0:00 مرحبا\n1:23 الفصل التالي\n",
			wantTitles:  []string{"مرحبا", "الفصل التالي"},
			wantStarts:  []float64{0, 83},
		},
		{
			name:        "CJK Chinese",
			description: "0:00 序章\n1:23 第二章\n",
			wantTitles:  []string{"序章", "第二章"},
			wantStarts:  []float64{0, 83},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			chapters := youtubeChaptersFromDescription(c.description, 200, true)
			if len(chapters) != len(c.wantTitles) {
				t.Fatalf("chapters = %d, want %d", len(chapters), len(c.wantTitles))
			}
			for index, want := range c.wantTitles {
				object, _ := chapters[index].Object()
				if title := youtubeWatchText(object, "title"); title != want {
					t.Fatalf("chapter[%d].title = %q, want %q", index, title, want)
				}
				if start, _ := object.Lookup("start_time").Float(); start != c.wantStarts[index] {
					t.Fatalf("chapter[%d].start_time = %v, want %v", index, start, c.wantStarts[index])
				}
			}
		})
	}
}

// TestYouTubeWatchMetadataHeatmapGlobalCap reproduces the previously-broken
// case where two mutations of 1024 markers each produced 1025 entries
// because the cap only short-circuited the inner marker loop. The fix
// enforces the cap globally so the second mutation is short-circuited
// as well.
func TestYouTubeWatchMetadataHeatmapGlobalCap(t *testing.T) {
	mutation := func() string {
		markers := make([]string, 0, youtubeMaxWatchHeatmapEntries)
		for index := 0; index < youtubeMaxWatchHeatmapEntries; index++ {
			markers = append(markers, fmt.Sprintf(`{"startMillis":%d,"durationMillis":1000,"intensityScoreNormalized":0.5}`, index))
		}
		return `{"payload":{"macroMarkersListEntity":{"markersList":{"markerType":"MARKER_TYPE_HEATMAP","markers":[` + strings.Join(markers, ",") + `]}}}}`
	}
	page := []byte(`<!doctype html><html><body><script>var ytInitialData = {"frameworkUpdates":{"entityBatchUpdate":{"mutations":[` + mutation() + "," + mutation() + `]}}};</script></body></html>`)
	metadata, err := extractYouTubeWatchMetadata(page, 123, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(metadata.heatmap) != youtubeMaxWatchHeatmapEntries {
		t.Fatalf("heatmap = %d; want %d (global cap)", len(metadata.heatmap), youtubeMaxWatchHeatmapEntries)
	}
}

// TestYouTubeWatchMetadataAvailabilityInfersPublic covers the pinned
// inference: when isPrivate/isUnlisted are known-false and no badge
// elevates the video, the merged claim is "public" rather than absent.
func TestYouTubeWatchMetadataAvailabilityInfersPublic(t *testing.T) {
	privateFalse := false
	unlistedFalse := false
	privateTrue := true
	unlistedTrue := true
	// Both signals known-false: claim public.
	if got := youtubeMergedAvailability("", &privateFalse, &unlistedFalse, 0); got != "public" {
		t.Fatalf("merged availability with both known-false = %q, want public", got)
	}
	// Unknown signals must not claim public.
	if got := youtubeMergedAvailability("", nil, nil, 0); got != "" {
		t.Fatalf("merged availability with both unknown = %q, want \"\"", got)
	}
	// isPrivate known-false, isUnlisted unknown: must not claim public
	// (previously overclaimed; pinned yt-dlp requires all_known).
	if got := youtubeMergedAvailability("", &privateFalse, nil, 0); got != "" {
		t.Fatalf("merged availability with isPrivate=false, isUnlisted=nil = %q, want \"\"", got)
	}
	// isPrivate unknown, isUnlisted known-false: must not claim public.
	if got := youtubeMergedAvailability("", nil, &unlistedFalse, 0); got != "" {
		t.Fatalf("merged availability with isPrivate=nil, isUnlisted=false = %q, want \"\"", got)
	}
	// Premium_only state still beats public inference.
	if got := youtubeMergedAvailability("premium_only", &privateFalse, &unlistedFalse, 0); got != "premium_only" {
		t.Fatalf("merged availability with premium_only badge = %q, want premium_only", got)
	}
	// Other signals still beat public inference.
	if got := youtubeMergedAvailability("", &privateTrue, &unlistedFalse, 0); got != "private" {
		t.Fatalf("merged availability with private=true = %q, want private", got)
	}
	if got := youtubeMergedAvailability("", &privateFalse, &unlistedTrue, 0); got != "unlisted" {
		t.Fatalf("merged availability with unlisted=true = %q, want unlisted", got)
	}
}

// TestYouTubeWatchMetadataCaseInsensitiveDislikeMatching reproduces the
// previously-broken case where "56 Dislikes" was classified as like_count
// because the captured "dis" prefix was compared case-sensitively. The
// fix compares the captured prefix case-insensitively so all of
// "dislikes"/"Dislikes"/"DISLIKES" map to dislikeCount. The unit test
// invokes parseLikeToggle directly with synthesized toggle values, which
// is the canonical code path for the case-insensitive comparison.
func TestYouTubeWatchMetadataCaseInsensitiveDislikeMatching(t *testing.T) {
	cases := []struct {
		label       string
		wantLike    bool
		wantDislike bool
	}{
		{label: "56 likes", wantLike: true},
		{label: "56 Likes", wantLike: true},
		{label: "56 LIKES", wantLike: true},
		{label: "56 dislikes", wantDislike: true},
		{label: "56 Dislikes", wantDislike: true},
		{label: "56 DISLIKES", wantDislike: true},
	}
	for _, c := range cases {
		t.Run(c.label, func(t *testing.T) {
			metadata := &youtubeWatchMetadata{}
			toggle := value.ObjectValue(value.NewObject(
				value.Field{Key: "defaultText", Value: value.ObjectValue(value.NewObject(
					value.Field{Key: "accessibility", Value: value.ObjectValue(value.NewObject(
						value.Field{Key: "accessibilityData", Value: value.ObjectValue(value.NewObject(
							value.Field{Key: "label", Value: value.String(c.label)},
						))},
					))},
				))},
			))
			if !metadata.parseLikeToggle(toggle) {
				t.Fatalf("label %q: parseLikeToggle returned false", c.label)
			}
			if c.wantLike {
				if !metadata.hasLikeCount || metadata.likeCount != 56 {
					t.Fatalf("label %q => likeCount = %d, %v", c.label, metadata.likeCount, metadata.hasLikeCount)
				}
				if metadata.hasDislikeCount {
					t.Fatalf("label %q must not classify as dislike", c.label)
				}
			}
			if c.wantDislike {
				if !metadata.hasDislikeCount || metadata.dislikeCount != 56 {
					t.Fatalf("label %q => dislikeCount = %d, %v", c.label, metadata.dislikeCount, metadata.hasDislikeCount)
				}
				if metadata.hasLikeCount {
					t.Fatalf("label %q must not classify as like", c.label)
				}
			}
		})
	}
}

// TestYouTubeWatchMetadataRootBoundsApplied reproduces the previously-broken
// case where ytInitialData was fully unmarshaled without structural
// bounds. The fix enforces the pinned depth/node/byte limits on the root
// payload before parseRoot runs.
func TestYouTubeWatchMetadataRootBoundsApplied(t *testing.T) {
	// Oversized payload: more than youtubeMaxJSONBytes.
	oversized := `{"a":` + strings.Repeat(`{"b":1}`, youtubeMaxJSONBytes/7) + `}`
	page := []byte(`<!doctype html><html><body><script>var ytInitialData = ` + oversized + `;</script></body></html>`)
	metadata, err := extractYouTubeWatchMetadata(page, 123, true)
	if err != nil {
		t.Fatal(err)
	}
	if metadata.hasAvailability || metadata.availability != "" {
		t.Fatalf("oversized payload must not yield any availability claim")
	}
	if len(metadata.chapters) > 0 || len(metadata.heatmap) > 0 {
		t.Fatalf("oversized payload must not yield chapters or heatmap")
	}
	// Deeply nested payload: depth > youtubeMaxJSONDepth.
	deep := `{"a":`
	for i := 0; i < youtubeMaxJSONDepth+2; i++ {
		deep += `{"a":`
	}
	deep += `1`
	for i := 0; i < youtubeMaxJSONDepth+2; i++ {
		deep += `}`
	}
	deep += `}`
	page = []byte(`<!doctype html><html><body><script>var ytInitialData = ` + deep + `;</script></body></html>`)
	metadata, err = extractYouTubeWatchMetadata(page, 123, true)
	if err != nil {
		t.Fatal(err)
	}
	if metadata.hasAvailability || metadata.availability != "" {
		t.Fatalf("deep payload must not yield any availability claim")
	}
}

// TestYouTubeWatchMetadataCommentCountEnrichProduct exercises the pinned
// comment-count contract through the actual Enrich closure path. The
// synthetic transport serves an empty comment continuation so the closure
// completes successfully with zero retrieved comments, and the test
// proves that the approximate watch-page count is preserved through
// deferred comment retrieval (matching the pinned reference).
func TestYouTubeWatchMetadataCommentCountEnrichProduct(t *testing.T) {
	withApprox := readYouTubeWatchFixture(t, "watch.html")
	transport := newYouTubeCommentTransport(youtubeWatchFixtureURL, withApprox, nil)
	result, err := NewYouTube().Extract(context.Background(), Request{
		URL:       youtubeWatchFixtureURL,
		Transport: transport,
		Options:   Options{Comments: YouTubeCommentOptions{Enabled: true, MaxComments: 10}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if count, ok := result.Info.Lookup("comment_count").Int(); !ok || count != 1234 {
		t.Fatalf("initial comment_count = %d, ok=%v; want 1234", count, ok)
	}
	if result.Enrich == nil {
		t.Fatal("Enrich closure not wired when comment retrieval is enabled")
	}
	if err := result.Enrich(context.Background(), &result.Info); err != nil {
		t.Fatalf("Enrich failed: %v", err)
	}
	if count, ok := result.Info.Lookup("comment_count").Int(); !ok || count != 1234 {
		t.Fatalf("approximate comment_count not preserved through Enrich: %d, %v", count, ok)
	}
}

// youtubeCommentEnrichTransport is a minimal Transport that serves the
// watch page from a static byte slice and answers /youtubei/v1/next
// comment-continuation requests with an empty reloadContinuationItemsCommand
// payload, so the closure's fetch path completes without network failure.
type youtubeCommentEnrichTransport struct {
	pageURL      string
	page         []byte
	continuation []byte
	calls        int
}

func newYouTubeCommentTransport(pageURL string, page []byte, continuation []byte) *youtubeCommentEnrichTransport {
	return &youtubeCommentEnrichTransport{pageURL: pageURL, page: page, continuation: continuation}
}

func (transport *youtubeCommentEnrichTransport) ReadPage(_ context.Context, rawURL string) ([]byte, http.Header, error) {
	if rawURL != transport.pageURL {
		return nil, nil, fmt.Errorf("unexpected URL %q", rawURL)
	}
	return append([]byte(nil), transport.page...), make(http.Header), nil
}

func (transport *youtubeCommentEnrichTransport) Do(_ context.Context, request *http.Request) (*http.Response, error) {
	transport.calls++
	body := transport.continuation
	if body == nil {
		// Empty but valid comment-continuation response: an empty
		// reloadContinuationItemsCommand container so the parser sees
		// zero retrieved comments and the closure completes successfully.
		body = []byte(`{"onResponseReceivedEndpoints":[{"reloadContinuationItemsCommand":{"continuationItems":[]}}]}`)
	}
	response := &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(bytes.NewReader(body)),
		Header:     make(http.Header),
		Request:    request,
	}
	response.Header.Set("Content-Type", "application/json")
	return response, nil
}

func (transport *youtubeCommentEnrichTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	return transport.Do(nil, request)
}

// FuzzYouTubeWatchMetadataParser exercises the watch-page metadata
// extractor against arbitrary ytInitialData blobs. The fuzz target
// enforces the structural limits already promised by the evidence doc:
// an oversized or deeply-nested payload must not crash, leak unbounded
// allocations, or produce a partial availability/chapters/heatmap claim.
func FuzzYouTubeWatchMetadataParser(f *testing.F) {
	f.Add(`{"contents":{"twoColumnWatchNextResults":{"results":{"results":{"contents":[]}}}}}`)
	f.Add(`{"frameworkUpdates":{"entityBatchUpdate":{"mutations":[]}}}`)
	f.Add(`{"contents":{"twoColumnWatchNextResults":{"results":{"results":{"contents":[{"videoPrimaryInfoRenderer":{"dateText":{"simpleText":"Jan 15, 2024"}}}]}}}}}`)
	f.Add(`{"contents":{"twoColumnWatchNextResults":{"results":{"results":{"contents":[{"videoSecondaryInfoRenderer":{"owner":{"videoOwnerRenderer":{"subscriberCountText":{"simpleText":"12.3K subscribers"}},"badges":[{"metadataBadgeRenderer":{"style":"BADGE_STYLE_TYPE_VERIFIED"}}]}}}}]}}}}}`)
	f.Add(`<script>var ytInitialData = {"frameworkUpdates":{"entityBatchUpdate":{"mutations":[{"payload":{"macroMarkersListEntity":{"markersList":{"markerType":"MARKER_TYPE_HEATMAP","markers":[{"startMillis":0,"durationMillis":1000,"intensityScoreNormalized":0.5}]}}}}}]}}};</script>`)
	f.Fuzz(func(t *testing.T, raw string) {
		// Wrap the raw input as a minimal HTML payload; a non-JSON or
		// hostile blob must simply yield zero metadata, never panic.
		page := []byte(`<!doctype html><html><body><script>var ytInitialData = ` + raw + `;</script></body></html>`)
		metadata, err := extractYouTubeWatchMetadata(page, 123, true)
		if err != nil {
			t.Fatalf("extractYouTubeWatchMetadata returned error %v", err)
		}
		// Structural limits must keep partial claims impossible: any
		// successful parse yields either a complete claim or none.
		if metadata.hasAvailability {
			if metadata.availability == "" {
				t.Fatalf("hasAvailability with empty availability")
			}
			if !youtubeAvailabilityIsKnown(metadata.availability) {
				t.Fatalf("unknown availability string %q", metadata.availability)
			}
		}
		if len(metadata.heatmap) > youtubeMaxWatchHeatmapEntries {
			t.Fatalf("heatmap exceeded global cap: %d", len(metadata.heatmap))
		}
	})
}

// youtubeAvailabilityIsKnown validates that a parsed availability string
// is one of the pinned states, so a hostile fuzz payload cannot fabricate
// a non-existent state.
func youtubeAvailabilityIsKnown(value string) bool {
	switch value {
	case "private", "premium_only", "subscriber_only", "needs_auth", "unlisted", "public":
		return true
	}
	return false
}
