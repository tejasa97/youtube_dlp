package extractor

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/ytdlp-go/ytdlp/internal/value"
)

const youtubeMetadataFixtureURL = "https://www.youtube.com/watch?v=fixture0002"

func readYouTubePlayerMetadataFixture(t youtubeTestHelper, name string) []byte {
	t.Helper()
	data, err := os.ReadFile("../../conformance/extractors/youtube_player_metadata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// youtubeMetadataPlayer returns a minimal watch page embedding the supplied
// player response JSON so targeted tests can vary one field at a time.
func youtubeMetadataPlayer(player string) []byte {
	return []byte(`<!doctype html><html><head><title>t</title></head><body><script>var ytInitialPlayerResponse = ` + player + `;</script></body></html>`)
}

func extractYouTubePlayerMetadata(t *testing.T, page []byte) *value.Object {
	t.Helper()
	transport := &memoryTransport{pages: map[string][]byte{youtubeMetadataFixtureURL: page}}
	result, err := NewYouTube().Extract(context.Background(), Request{URL: youtubeMetadataFixtureURL, Transport: transport})
	if err != nil {
		t.Fatal(err)
	}
	return result.Info.Fields()
}

func youtubeMetadataString(object *value.Object, key string) string {
	text, _ := object.Lookup(key).StringValue()
	return text
}

func youtubeMetadataHas(object *value.Object, key string) bool {
	return !object.Lookup(key).IsMissing()
}

// youtubeMinimalPlayerMetadata is the base player response for targeted
// metadata tests. %s is replaced with the microformat object content.
const youtubeMinimalPlayerMetadata = `{
  "playabilityStatus": {"status": "OK"},
  "videoDetails": {
    "videoId": "fixture0002", "title": "T", "lengthSeconds": "10",
    "author": "A", "channelId": "UCfixture", "shortDescription": "D",
    "viewCount": "1", "isLiveContent": false
  },
  "microformat": {"playerMicroformatRenderer": {%s}},
  "streamingData": {"formats": [{"itag": 18, "url": "https://media.example/v.mp4", "mimeType": "video/mp4; codecs=\"avc1.42001E\"", "bitrate": 100000, "contentLength": "100"}]}
}`

func TestYouTubePlayerMetadataPinnedExtraction(t *testing.T) {
	watch := readYouTubePlayerMetadataFixture(t, "watch.html")
	expected := readYouTubePlayerMetadataFixture(t, "expected.json")
	transport := &memoryTransport{pages: map[string][]byte{youtubeMetadataFixtureURL: watch}}
	result, err := NewYouTube().Extract(context.Background(), Request{URL: youtubeMetadataFixtureURL, Transport: transport})
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
	if len(transport.reads) != 1 || transport.reads[0] != youtubeMetadataFixtureURL {
		t.Fatalf("reads = %#v; expected a single watch-page read (no player fetch)", transport.reads)
	}
}

func TestYouTubePlayerMetadataUploadDates(t *testing.T) {
	tests := []struct {
		name      string
		raw       string
		wantDate  string
		wantTS    int64
		wantHasTS bool
	}{
		{"rfc3339 offset day rollover", "2024-01-15T18:30:00-08:00", "20240116", 1705372200, true},
		{"rfc3339 zulu", "2024-01-15T00:00:00Z", "20240115", 1705276800, true},
		{"date only", "2024-01-15", "20240115", 0, false},
		{"empty", "", "", 0, false},
		{"malformed calendar", "2024-13-45", "", 0, false},
		{"no timezone", "2024-01-15T18:30:00", "", 0, false},
		{"not a date", "yesterday", "", 0, false},
		{"oversized", strings.Repeat("2", 65), "", 0, false},
		{"control chars", "2024-01-15\n", "", 0, false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			player := fmt.Sprintf(youtubeMinimalPlayerMetadata, fmt.Sprintf(`"uploadDate": %q`, test.raw))
			info := extractYouTubePlayerMetadata(t, youtubeMetadataPlayer(player))
			if got := youtubeMetadataString(info, "upload_date"); got != test.wantDate {
				t.Fatalf("upload_date = %q; want %q", got, test.wantDate)
			}
			if test.wantHasTS {
				got, ok := info.Lookup("timestamp").Int()
				if !ok || got != test.wantTS {
					t.Fatalf("timestamp = %d, %v; want %d", got, ok, test.wantTS)
				}
			} else if youtubeMetadataHas(info, "timestamp") {
				t.Fatal("timestamp must be absent when time/timezone are not attributable")
			}
		})
	}
}

func TestYouTubePlayerMetadataOwnerProfile(t *testing.T) {
	tests := []struct {
		name       string
		ownerURL   string
		wantHandle string
	}{
		{"valid", "https://www.youtube.com/@fixturechannel", "@fixturechannel"},
		{"bare host", "https://youtube.com/@handle", "@handle"},
		{"http scheme", "http://www.youtube.com/@foo", "@foo"},
		{"dots dashes underscore", "https://www.youtube.com/@a.b-c_d", "@a.b-c_d"},
		{"unicode letters", "https://www.youtube.com/@üñïçodé", "@üñïçodé"},
		{"empty", "", ""},
		{"evil host", "https://evil.example/@foo", ""},
		{"userinfo", "https://user@www.youtube.com/@foo", ""},
		{"port", "https://www.youtube.com:8443/@foo", ""},
		{"extra path segment", "https://www.youtube.com/@foo/videos", ""},
		{"query string", "https://www.youtube.com/@foo?tab=1", ""},
		{"fragment", "https://www.youtube.com/@foo#about", ""},
		{"no handle", "https://www.youtube.com/", ""},
		{"space in handle", "https://www.youtube.com/@fo o", ""},
		{"control chars", "https://www.youtube.com/@fo\x00o", ""},
		{"oversized handle", "https://www.youtube.com/@" + strings.Repeat("a", 250), ""},
		{"file scheme", "file:///www.youtube.com/@foo", ""},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			encodedURL, _ := json.Marshal(test.ownerURL)
			player := fmt.Sprintf(youtubeMinimalPlayerMetadata, `"ownerProfileUrl": `+string(encodedURL))
			info := extractYouTubePlayerMetadata(t, youtubeMetadataPlayer(player))
			if got := youtubeMetadataString(info, "uploader_id"); got != test.wantHandle {
				t.Fatalf("uploader_id = %q; want %q", got, test.wantHandle)
			}
			if test.wantHandle != "" {
				wantURL := "https://www.youtube.com/" + test.wantHandle
				if got := youtubeMetadataString(info, "uploader_url"); got != wantURL {
					t.Fatalf("uploader_url = %q; want %q", got, wantURL)
				}
			} else if youtubeMetadataHas(info, "uploader_url") {
				t.Fatal("uploader_url must be absent for an unattributable owner profile")
			}
		})
	}
	// channel_url derives from the channel ID, not the owner profile.
	info := extractYouTubePlayerMetadata(t, youtubeMetadataPlayer(fmt.Sprintf(youtubeMinimalPlayerMetadata, ``)))
	if got := youtubeMetadataString(info, "channel_url"); got != "https://www.youtube.com/channel/UCfixture" {
		t.Fatalf("channel_url = %q", got)
	}
}

func TestYouTubePlayerMetadataAvailabilityAndAgeLimit(t *testing.T) {
	tests := []struct {
		name             string
		videoFlags       string
		microFlags       string
		wantAvailability string
		wantAgeLimit     int
	}{
		{"private", `, "isPrivate": true`, `"isUnlisted": false, "isFamilySafe": true`, "private", 0},
		{"unlisted", `, "isPrivate": false`, `"isUnlisted": true, "isFamilySafe": true`, "unlisted", 0},
		{"family unsafe", `, "isPrivate": false`, `"isUnlisted": false, "isFamilySafe": false`, "needs_auth", 18},
		{"private beats family unsafe", `, "isPrivate": true`, `"isUnlisted": false, "isFamilySafe": false`, "private", 18},
		{"needs auth beats unlisted", `, "isPrivate": false`, `"isUnlisted": true, "isFamilySafe": false`, "needs_auth", 18},
		{"public stays absent", `, "isPrivate": false`, `"isUnlisted": false, "isFamilySafe": true`, "", 0},
		{"absent flags", ``, ``, "", 0},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			player := fmt.Sprintf(youtubeMinimalPlayerMetadata, test.microFlags)
			player = strings.Replace(player, `"author": "A"`, `"author": "A"`+test.videoFlags, 1)
			info := extractYouTubePlayerMetadata(t, youtubeMetadataPlayer(player))
			if got := youtubeMetadataString(info, "availability"); got != test.wantAvailability {
				t.Fatalf("availability = %q; want %q", got, test.wantAvailability)
			}
			ageLimit, ok := info.Lookup("age_limit").Int()
			if !ok || int(ageLimit) != test.wantAgeLimit {
				t.Fatalf("age_limit = %d, %v; want %d", ageLimit, ok, test.wantAgeLimit)
			}
		})
	}
}

func TestYouTubePlayerMetadataMediaType(t *testing.T) {
	tests := []struct {
		name          string
		videoFlags    string
		microFlags    string
		wantMediaType string
	}{
		{"video", `"isLiveContent": false`, `"isShortsEligible": false`, "video"},
		{"short", `"isLiveContent": false`, `"isShortsEligible": true`, "short"},
		{"livestream", `"isLiveContent": true`, `"isShortsEligible": false`, "livestream"},
		{"livestream beats short", `"isLiveContent": true`, `"isShortsEligible": true`, "livestream"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			player := fmt.Sprintf(youtubeMinimalPlayerMetadata, test.microFlags)
			player = strings.Replace(player, `"isLiveContent": false`, test.videoFlags, 1)
			info := extractYouTubePlayerMetadata(t, youtubeMetadataPlayer(player))
			if got := youtubeMetadataString(info, "media_type"); got != test.wantMediaType {
				t.Fatalf("media_type = %q; want %q", got, test.wantMediaType)
			}
		})
	}
}

func TestYouTubePlayerMetadataThumbnails(t *testing.T) {
	t.Run("ordering and best original", func(t *testing.T) {
		player := fmt.Sprintf(youtubeMinimalPlayerMetadata, `"isFamilySafe": true, "thumbnail": {"thumbnails": [{"url": "https://img.example/micro.jpg", "width": 640, "height": 360}]}`)
		player = strings.Replace(player, `"isLiveContent": false`, `"isLiveContent": false, "thumbnail": {"thumbnails": [{"url": "https://img.example/small.jpg", "width": 160, "height": 90}, {"url": "https://img.example/large.jpg", "width": 1280, "height": 720}]}`, 1)
		info := extractYouTubePlayerMetadata(t, youtubeMetadataPlayer(player))
		list, ok := info.Lookup("thumbnails").ListValue()
		wantCount := 3 + 2*len(youtubeThumbnailNames)
		if !ok || len(list) != wantCount {
			t.Fatalf("thumbnails length = %d (%v); want %d", len(list), ok, wantCount)
		}
		for index, want := range []string{"https://img.example/small.jpg", "https://img.example/large.jpg", "https://img.example/micro.jpg"} {
			object, _ := list[index].Object()
			if got, _ := object.Lookup("url").StringValue(); got != want {
				t.Fatalf("thumbnails[%d] = %q; want %q", index, got, want)
			}
		}
		widthObject, _ := list[0].Object()
		width, _ := widthObject.Lookup("width").Int()
		if width != 160 {
			t.Fatalf("thumbnails[0].width = %d", width)
		}
		if got := youtubeMetadataString(info, "thumbnail"); got != "https://img.example/micro.jpg" {
			t.Fatalf("thumbnail = %q; want micro.jpg (last original)", got)
		}
		wantPreferences := []int64{0, -1, -2, -3, -36, -37}
		wantPositions := []int{3, 4, 5, 6, len(list) - 2, len(list) - 1}
		for index, want := range wantPreferences {
			object, _ := list[wantPositions[index]].Object()
			got, _ := object.Lookup("preference").Int()
			if got != want {
				t.Fatalf("ladder position %d preference = %d; want %d", wantPositions[index], got, want)
			}
		}
	})

	t.Run("og image becomes best original", func(t *testing.T) {
		player := `{"playabilityStatus": {"status": "OK"}, "videoDetails": {"videoId": "fixture0002", "title": "T", "lengthSeconds": "10", "author": "A", "channelId": "UCfixture", "shortDescription": "D", "viewCount": "1", "isLiveContent": false}, "streamingData": {"formats": [{"itag": 18, "url": "https://media.example/v.mp4", "mimeType": "video/mp4; codecs=\"avc1.42001E\"", "bitrate": 100000, "contentLength": "100"}]}}`
		page := []byte(`<!doctype html><html><head><meta property="og:image" content="https://img.example/og.jpg"></head><body><script>var ytInitialPlayerResponse = ` + player + `;</script></body></html>`)
		info := extractYouTubePlayerMetadata(t, page)
		if got := youtubeMetadataString(info, "thumbnail"); got != "https://img.example/og.jpg" {
			t.Fatalf("thumbnail = %q; want og.jpg", got)
		}
		list, _ := info.Lookup("thumbnails").ListValue()
		object, _ := list[0].Object()
		first, _ := object.Lookup("url").StringValue()
		if first != "https://img.example/og.jpg" {
			t.Fatalf("thumbnails[0] = %q; want og.jpg", first)
		}
	})

	t.Run("deduplication keeps first occurrence", func(t *testing.T) {
		player := fmt.Sprintf(youtubeMinimalPlayerMetadata, ``)
		player = strings.Replace(player, `"isLiveContent": false`, `"isLiveContent": false, "thumbnail": {"thumbnails": [{"url": "https://img.example/same.jpg"}, {"url": "https://img.example/same.jpg"}]}`, 1)
		info := extractYouTubePlayerMetadata(t, youtubeMetadataPlayer(player))
		list, _ := info.Lookup("thumbnails").ListValue()
		count := 0
		for _, item := range list {
			object, ok := item.Object()
			if !ok {
				continue
			}
			if url, _ := object.Lookup("url").StringValue(); url == "https://img.example/same.jpg" {
				count++
			}
		}
		if count != 1 {
			t.Fatalf("duplicate URL retained %d times", count)
		}
	})
}

func TestYouTubePlayerMetadataThumbnailsMore(t *testing.T) {
	t.Run("hostile and oversized URLs omitted", func(t *testing.T) {
		player := fmt.Sprintf(youtubeMinimalPlayerMetadata, ``)
		player = strings.Replace(player, `"isLiveContent": false`, `"isLiveContent": false, "thumbnail": {"thumbnails": [{"url": "file:///etc/passwd"}, {"url": "javascript:alert(1)"}, {"url": "https://img.example/ok.jpg"}, {"url": "https://img.example/`+strings.Repeat("a", 2049)+`.jpg"}]}`, 1)
		info := extractYouTubePlayerMetadata(t, youtubeMetadataPlayer(player))
		list, _ := info.Lookup("thumbnails").ListValue()
		if len(list) != 1+2*len(youtubeThumbnailNames) {
			t.Fatalf("thumbnails length = %d; want %d", len(list), 1+2*len(youtubeThumbnailNames))
		}
		object, _ := list[0].Object()
		first, _ := object.Lookup("url").StringValue()
		if first != "https://img.example/ok.jpg" {
			t.Fatalf("thumbnails[0] = %q", first)
		}
	})

	t.Run("live ladder suffix", func(t *testing.T) {
		player := fmt.Sprintf(youtubeMinimalPlayerMetadata, ``)
		player = strings.Replace(player, `"isLiveContent": false`, `"isLiveContent": false, "thumbnail": {"thumbnails": [{"url": "https://img.example/a.jpg"}]}`, 1)
		player = strings.Replace(player, `"viewCount": "1"`, `"viewCount": "1", "isLive": true`, 1)
		info := extractYouTubePlayerMetadata(t, youtubeMetadataPlayer(player))
		if got := youtubeMetadataString(info, "live_status"); got != "is_live" {
			t.Fatalf("live_status = %q", got)
		}
		list, _ := info.Lookup("thumbnails").ListValue()
		want := "https://i.ytimg.com/vi_webp/fixture0002/maxresdefault_live.webp"
		object, _ := list[1].Object()
		if got, _ := object.Lookup("url").StringValue(); got != want {
			t.Fatalf("ladder[1] = %q; want %q", got, want)
		}
	})

	t.Run("cap total thumbnails", func(t *testing.T) {
		var raw strings.Builder
		raw.WriteString("[")
		for index := 0; index < 70; index++ {
			if index > 0 {
				raw.WriteString(",")
			}
			fmt.Fprintf(&raw, `{"url": "https://img.example/t%d.jpg"}`, index)
		}
		raw.WriteString("]")
		player := fmt.Sprintf(youtubeMinimalPlayerMetadata, ``)
		player = strings.Replace(player, `"isLiveContent": false`, `"isLiveContent": false, "thumbnail": {"thumbnails": `+raw.String()+`}`, 1)
		info := extractYouTubePlayerMetadata(t, youtubeMetadataPlayer(player))
		list, _ := info.Lookup("thumbnails").ListValue()
		if len(list) != youtubeMaxThumbnails {
			t.Fatalf("thumbnails length = %d; want %d", len(list), youtubeMaxThumbnails)
		}
	})
}

func TestYouTubePlayerMetadataTagsCategoryRating(t *testing.T) {
	player := fmt.Sprintf(youtubeMinimalPlayerMetadata, `"category": "Science & Technology"`)
	player = strings.Replace(player, `"viewCount": "1"`, `"viewCount": "1", "keywords": ["ok", "`+strings.Repeat("x", 1025)+`", "bad\u0000", ""], "averageRating": 0`, 1)
	info := extractYouTubePlayerMetadata(t, youtubeMetadataPlayer(player))
	tags, _ := info.Lookup("tags").ListValue()
	categories, _ := info.Lookup("categories").ListValue()
	if len(tags) != 1 || len(categories) != 1 {
		t.Fatalf("tags = %#v categories = %#v", tags, categories)
	}
	if category, _ := categories[0].StringValue(); category != "Science & Technology" {
		t.Fatalf("categories[0] = %q", category)
	}
	if rating, ok := info.Lookup("average_rating").Float(); !ok || rating != 0 {
		t.Fatalf("average_rating = %v, %v", rating, ok)
	}

	// Tags are capped at youtubeMaxTags.
	var keywords strings.Builder
	keywords.WriteString("[")
	for index := 0; index < 66; index++ {
		if index > 0 {
			keywords.WriteString(",")
		}
		fmt.Fprintf(&keywords, `"tag%d"`, index)
	}
	keywords.WriteString("]")
	player = fmt.Sprintf(youtubeMinimalPlayerMetadata, ``)
	player = strings.Replace(player, `"viewCount": "1"`, `"viewCount": "1", "keywords": `+keywords.String(), 1)
	info = extractYouTubePlayerMetadata(t, youtubeMetadataPlayer(player))
	tags, _ = info.Lookup("tags").ListValue()
	if len(tags) != youtubeMaxTags {
		t.Fatalf("tags length = %d; want %d", len(tags), youtubeMaxTags)
	}

	// Empty keywords and absent rating stay absent.
	info = extractYouTubePlayerMetadata(t, youtubeMetadataPlayer(fmt.Sprintf(youtubeMinimalPlayerMetadata, `"category": "bad\u0000category"`)))
	if youtubeMetadataHas(info, "tags") || youtubeMetadataHas(info, "categories") || youtubeMetadataHas(info, "average_rating") {
		t.Fatalf("empty tags/category/rating must be absent: %#v", info.Fields())
	}
}

func TestYouTubePlayerMetadataStretchedRatio(t *testing.T) {
	tests := []struct {
		name     string
		keywords string
		want     float64
		hasWant  bool
	}{
		{"valid 4:3", `["yt:stretch=4:3"]`, 4.0 / 3.0, true},
		{"first valid wins", `["yt:stretch=16:9", "yt:stretch=4:3"]`, 16.0 / 9.0, true},
		{"unanchored search", `["yt:stretch=abc1:2x"]`, 0.5, true},
		{"zero width", `["yt:stretch=0:5"]`, 0, false},
		{"zero height", `["yt:stretch=5:0"]`, 0, false},
		{"no digits", `["yt:stretch=abc"]`, 0, false},
		{"overflow", `["yt:stretch=99999999999999999999999:2"]`, 0, false},
		{"wrong prefix", `["notstretch=4:3"]`, 0, false},
		{"empty", `[]`, 0, false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			player := fmt.Sprintf(youtubeMinimalPlayerMetadata, ``)
			player = strings.Replace(player, `"viewCount": "1"`, `"viewCount": "1", "keywords": `+test.keywords, 1)
			info := extractYouTubePlayerMetadata(t, youtubeMetadataPlayer(player))
			formats, _ := info.Lookup("formats").ListValue()
			video, _ := formats[0].Object()
			ratio, present := video.Lookup("stretched_ratio").Float()
			if present != test.hasWant || (test.hasWant && math.Abs(ratio-test.want) > 1e-12) {
				t.Fatalf("stretched_ratio = %v, %v; want %v, %v", ratio, present, test.want, test.hasWant)
			}
		})
	}
}

func TestYouTubePlayerMetadataOGImage(t *testing.T) {
	tests := []struct {
		name string
		page string
		want string
	}{
		{"property first", `<meta property="og:image" content="https://img.example/og.jpg?a=1&amp;b=2">`, "https://img.example/og.jpg?a=1&b=2"},
		{"content first", `<meta content="https://img.example/og.jpg" property="og:image">`, "https://img.example/og.jpg"},
		{"single quotes", `<meta property='og:image' content='https://img.example/og.jpg'>`, "https://img.example/og.jpg"},
		{"twitter fallback", `<meta name="twitter:image" content="https://img.example/tw.jpg">`, "https://img.example/tw.jpg"},
		{"og beats twitter", `<meta name="twitter:image" content="https://img.example/tw.jpg"><meta property="og:image" content="https://img.example/og.jpg">`, "https://img.example/og.jpg"},
		{"javascript rejected", `<meta property="og:image" content="javascript:alert(1)">`, ""},
		{"file rejected", `<meta property="og:image" content="file:///etc/passwd">`, ""},
		{"oversized content", `<meta property="og:image" content="https://img.example/` + strings.Repeat("a", 2049) + `.jpg">`, ""},
		{"missing", `<meta property="og:description" content="d">`, ""},
		{"empty content", `<meta property="og:image" content="">`, ""},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := youtubeOGImage([]byte(`<html><head>` + test.page + `</head><body></body></html>`)); got != test.want {
				t.Fatalf("youtubeOGImage = %q; want %q", got, test.want)
			}
		})
	}
	if got := youtubeOGImage([]byte(strings.Repeat("x", youtubeMaxOGImageScanBytes) + `<meta property="og:image" content="https://img.example/late.jpg">`)); got != "" {
		t.Fatalf("scan must be bounded to the head region, got %q", got)
	}
}

func TestYouTubePlayerMetadataJSONFieldSurvival(t *testing.T) {
	watch := readYouTubePlayerMetadataFixture(t, "watch.html")
	transport := &memoryTransport{pages: map[string][]byte{youtubeMetadataFixtureURL: watch}}
	result, err := NewYouTube().Extract(context.Background(), Request{URL: youtubeMetadataFixtureURL, Transport: transport})
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(result.Info.Fields())
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]any
	if err := json.Unmarshal(encoded, &document); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"channel_url", "uploader_id", "uploader_url", "upload_date", "timestamp", "age_limit",
		"categories", "tags", "average_rating", "playable_in_embed", "media_type", "thumbnail", "thumbnails"} {
		if _, ok := document[key]; !ok {
			t.Fatalf("field %q did not survive JSON encoding", key)
		}
	}
	formats, ok := document["formats"].([]any)
	if !ok || len(formats) != 2 {
		t.Fatalf("formats = %#v", document["formats"])
	}
	video := formats[0].(map[string]any)
	if ratio, ok := video["stretched_ratio"].(float64); !ok || math.Abs(ratio-4.0/3.0) > 1e-12 {
		t.Fatalf("format 18 stretched_ratio = %v, %v", ratio, ok)
	}
	if _, ok := formats[1].(map[string]any)["stretched_ratio"]; ok {
		t.Fatal("audio format must not carry stretched_ratio")
	}
	if _, ok := document["availability"]; ok {
		t.Fatal("public availability must stay absent in PR1 (badge data pending)")
	}
}

func TestYouTubePlayerMetadataConcurrentDeterminism(t *testing.T) {
	watch := readYouTubePlayerMetadataFixture(t, "watch.html")
	var first *value.Object
	var once sync.Once
	var wg sync.WaitGroup
	for worker := 0; worker < 8; worker++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			transport := &memoryTransport{pages: map[string][]byte{youtubeMetadataFixtureURL: watch}}
			result, err := NewYouTube().Extract(context.Background(), Request{URL: youtubeMetadataFixtureURL, Transport: transport})
			if err != nil {
				t.Errorf("concurrent extraction failed: %v", err)
				return
			}
			encoded, err := json.Marshal(result.Info.Fields())
			if err != nil {
				t.Errorf("encode failed: %v", err)
				return
			}
			once.Do(func() {
				first = result.Info.Fields()
			})
			if !reflect.DeepEqual(encoded, mustMarshal(t, first)) {
				t.Errorf("concurrent extraction produced divergent metadata")
			}
		}()
	}
	wg.Wait()
}

func mustMarshal(t *testing.T, object *value.Object) []byte {
	t.Helper()
	encoded, err := json.Marshal(object)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

func FuzzYouTubePlayerMetadata(f *testing.F) {
	f.Add("2024-01-15T18:30:00-08:00")
	f.Add("2024-01-15")
	f.Add("garbage")
	f.Add("https://www.youtube.com/@fixturechannel")
	f.Add("https://evil.example/@foo")
	f.Add("yt:stretch=4:3")
	f.Add("yt:stretch=0:0")
	f.Add("https://img.example/thumb.jpg")
	f.Add("javascript:alert(1)")
	f.Fuzz(func(t *testing.T, raw string) {
		uploadDate, timestamp, hasTimestamp, ok := youtubeUploadDate(raw)
		if ok && (len(uploadDate) != 8 || timestamp < 0) {
			t.Fatalf("invalid upload date result %q %d %v", uploadDate, timestamp, ok)
		}
		if hasTimestamp && !ok {
			t.Fatalf("timestamp without ok for %q", raw)
		}
		handle := youtubeOwnerHandle(raw)
		if handle != "" && !youtubeHandlePattern.MatchString(handle) {
			t.Fatalf("invalid handle %q", handle)
		}
		ratio, hasRatio := youtubeStretchedRatio([]string{raw})
		if hasRatio && !(ratio > 0) {
			t.Fatalf("invalid ratio %v", ratio)
		}
		thumb := youtubeThumbnailURL(raw)
		if thumb != "" && len(thumb) > youtubeMaxThumbnailBytes {
			t.Fatalf("oversized thumbnail URL accepted")
		}
		og := youtubeOGImage([]byte(`<meta property="og:image" content="` + raw + `">`))
		if og != "" && len(og) > youtubeMaxThumbnailBytes {
			t.Fatalf("oversized og:image accepted")
		}
	})
}
