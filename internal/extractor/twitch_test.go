package extractor

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ytdlp-go/ytdlp/internal/events"
	"github.com/ytdlp-go/ytdlp/internal/protocol/hls"
	"github.com/ytdlp-go/ytdlp/internal/value"
)

const twitchFixtureRoot = "../../conformance/extractors/twitch"

type twitchRecordedRequest struct {
	header http.Header
	body   []byte
}

type twitchFixtureTransport struct {
	mu               sync.Mutex
	metadata         []byte
	token            []byte
	metadataStatus   int
	tokenStatus      int
	graphQLRequests  []twitchRecordedRequest
	graphQLFixtures  []twitchGraphQLFixture
	videosPages      map[string][]byte
	collectionsPages map[string][]byte
	clipsPages       map[string][]byte
	mediaPolls       int
}

type twitchGraphQLFixture struct {
	body   []byte
	status int
	err    error
}

func (transport *twitchFixtureTransport) Do(ctx context.Context, request *http.Request) (*http.Response, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if request.URL.String() == twitchGraphQLURL {
		body, err := io.ReadAll(request.Body)
		if err != nil {
			return nil, err
		}
		transport.mu.Lock()
		transport.graphQLRequests = append(transport.graphQLRequests, twitchRecordedRequest{header: request.Header.Clone(), body: body})
		if transport.videosPages != nil {
			cursor := twitchVideosRequestCursor(body)
			responseBody, ok := transport.videosPages[cursor]
			transport.mu.Unlock()
			if !ok {
				return nil, fmt.Errorf("unexpected videos cursor %q", cursor)
			}
			return twitchHTTPResponse(http.StatusOK, responseBody), nil
		}
		if transport.collectionsPages != nil {
			cursor := twitchChannelCollectionsRequestCursor(body)
			responseBody, ok := transport.collectionsPages[cursor]
			transport.mu.Unlock()
			if !ok {
				return nil, fmt.Errorf("unexpected collections cursor %q", cursor)
			}
			return twitchHTTPResponse(http.StatusOK, responseBody), nil
		}
		if transport.clipsPages != nil {
			cursor := twitchChannelClipsRequestCursor(body)
			responseBody, ok := transport.clipsPages[cursor]
			transport.mu.Unlock()
			if !ok {
				return nil, fmt.Errorf("unexpected clips cursor %q", cursor)
			}
			return twitchHTTPResponse(http.StatusOK, responseBody), nil
		}
		index := len(transport.graphQLRequests) - 1
		if len(transport.graphQLFixtures) != 0 {
			if index >= len(transport.graphQLFixtures) {
				transport.mu.Unlock()
				return nil, errors.New("unexpected extra GraphQL request")
			}
			fixture := transport.graphQLFixtures[index]
			transport.mu.Unlock()
			if fixture.err != nil {
				return nil, fixture.err
			}
			if fixture.status == 0 {
				fixture.status = http.StatusOK
			}
			return twitchHTTPResponse(fixture.status, fixture.body), nil
		}
		responseBody, status := transport.metadata, transport.metadataStatus
		if index > 0 {
			responseBody, status = transport.token, transport.tokenStatus
		}
		transport.mu.Unlock()
		if status == 0 {
			status = http.StatusOK
		}
		return twitchHTTPResponse(status, responseBody), nil
	}

	switch request.URL.Path {
	case "/api/channel/hls/chunked/10.ts":
		return twitchHTTPResponse(http.StatusOK, []byte("ten-")), nil
	case "/api/channel/hls/chunked/11.ts":
		return twitchHTTPResponse(http.StatusOK, []byte("eleven")), nil
	default:
		return twitchHTTPResponse(http.StatusNotFound, nil), nil
	}
}

func (transport *twitchFixtureTransport) ReadPage(ctx context.Context, rawURL string) ([]byte, http.Header, error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return nil, nil, err
	}
	if parsed.Host != "usher.ttvnw.net" {
		return nil, nil, fmt.Errorf("unexpected fixture host %q", parsed.Host)
	}
	switch parsed.Path {
	case "/api/channel/hls/fixture_channel.m3u8":
		if parsed.Query().Get("sig") != "fixture-signature-do-not-log" || !strings.Contains(parsed.Query().Get("token"), `"channel":"fixture_channel"`) {
			return nil, nil, errors.New("missing signed Twitch query")
		}
		return []byte("#EXTM3U\n#EXT-X-STREAM-INF:BANDWIDTH=1000\nchunked/index.m3u8\n"), make(http.Header), nil
	case "/api/channel/hls/chunked/index.m3u8":
		transport.mu.Lock()
		transport.mediaPolls++
		poll := transport.mediaPolls
		transport.mu.Unlock()
		if poll == 1 {
			return []byte("#EXTM3U\n#EXT-X-MEDIA-SEQUENCE:10\n#EXTINF:1,\n10.ts\n"), make(http.Header), nil
		}
		return []byte("#EXTM3U\n#EXT-X-MEDIA-SEQUENCE:10\n#EXTINF:1,\n10.ts\n#EXTINF:1,\n11.ts\n#EXT-X-ENDLIST\n"), make(http.Header), nil
	default:
		return nil, nil, fmt.Errorf("unexpected fixture page path %q", parsed.Path)
	}
}

func twitchHTTPResponse(status int, body []byte) *http.Response {
	return &http.Response{
		StatusCode: status,
		Header:     make(http.Header),
		Body:       io.NopCloser(bytes.NewReader(body)),
	}
}

func twitchFixture(t testing.TB, name string) []byte {
	t.Helper()
	body, err := os.ReadFile(filepath.Join(twitchFixtureRoot, name))
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func twitchChannelCollectionsRequestCursor(body []byte) string {
	var operations []struct {
		Variables struct {
			Cursor string `json:"cursor"`
		} `json:"variables"`
	}
	if err := json.Unmarshal(body, &operations); err != nil || len(operations) == 0 {
		return ""
	}
	return operations[0].Variables.Cursor
}

func twitchCollectionRequestID(body []byte) string {
	var operations []struct {
		Variables struct {
			CollectionID string `json:"collectionID"`
		} `json:"variables"`
	}
	if err := json.Unmarshal(body, &operations); err != nil || len(operations) == 0 {
		return ""
	}
	return operations[0].Variables.CollectionID
}

func twitchVideosRequestCursor(body []byte) string {
	var operations []struct {
		Variables struct {
			Cursor string `json:"cursor"`
		} `json:"variables"`
	}
	if err := json.Unmarshal(body, &operations); err != nil || len(operations) == 0 {
		return ""
	}
	return operations[0].Variables.Cursor
}

func twitchChannelClipsRequestCursor(body []byte) string {
	var operations []struct {
		Variables struct {
			Cursor string `json:"cursor"`
		} `json:"variables"`
	}
	if err := json.Unmarshal(body, &operations); err != nil || len(operations) == 0 {
		return ""
	}
	return operations[0].Variables.Cursor
}

func TestTwitchSuitable(t *testing.T) {
	t.Parallel()
	tests := []struct {
		rawURL string
		want   bool
	}{
		{"http://www.twitch.tv/shroomztv", true},
		{"https://go.twitch.tv/food#profile-0", true},
		{"https://m.twitch.tv/fixture_channel", true},
		{"https://player.twitch.tv/?channel=lotsofs", true},
		{"https://www.twitch.tv/videos/123", true},
		{"https://www.twitch.tv/fixture_channel/videos", true},
		{"https://www.twitch.tv/fixture_channel/videos/all", true},
		{"https://m.twitch.tv/fixture_channel/profile", true},
		{"https://go.twitch.tv/fixture_channel/videos?filter=archives&sort=views", true},
		{"https://www.twitch.tv/collections/FixtureCollection01", true},
		{"https://www.twitch.tv/collections/FixtureCollection01?feature=share", true},
		{"https://m.twitch.tv/collections/FixtureCollection01", true},
		{"https://go.twitch.tv/collections/FixtureCollection01", true},
		{"https://www.twitch.tv/fixture_channel/videos?filter=collections", true},
		{"https://www.twitch.tv/fixture_channel/profile?filter=collections", false},
		{"https://www.twitch.tv/fixture_channel/videos/all?filter=collections", false},
		{"https://www.twitch.tv/fixture_channel/videos?filter=clips", true},
		{"https://www.twitch.tv/fixture_channel/clips", true},
		{"https://www.twitch.tv/fixture_channel/clips?range=all", true},
		{"https://m.twitch.tv/fixture_channel/clips?range=7d", true},
		{"https://go.twitch.tv/fixture_channel/videos?filter=clips&range=30d", true},
		{"https://www.twitch.tv/fixture_channel/profile?filter=clips", false},
		{"https://www.twitch.tv/fixture_channel/videos/all?filter=clips", false},
		{"https://www.twitch.tv/fixture_channel/clips/extra", false},
		{"https://www.twitch.tv/fixture_channel/clips#fragment", false},
		{"https://www.twitch.tv/fixture_channel/videos#fragment", false},
		{"https://www.twitch.tv/collections/FixtureCollection01#fragment", false},
		{"https://www.twitch.tv/collections/FixtureCollection01?x=%zz", false},
		{"https://www.twitch.tv/collections/FixtureCollection01?x=%00", false},
		{"https://www.twitch.tv/collections/FixtureCollection01?x=%01share", false},
		{"https://www.twitch.tv/collections/FixtureCollection01?x=%FF", false},
		{"https://www.twitch.tv/collections/bad%2Fid", false},
		{"https://www.twitch.tv/fixture_channel/videos/all/extra", false},
		{"https://www.twitch.tv/directory", false},
		{"https://www.twitch.tv/channel/clips", true},
		{"https://www.twitch.tv/channel//", false},
		{"https://player.twitch.tv/?video=v123", true},
		{"https://www.twitch.tv/videos/123?t=5m10s", true},
		{"https://www.twitch.tv/channel/video/123", true},
		{"https://www.twitch.tv/channel/schedule?vodID=123", true},
		{"https://clips.twitch.tv/CulturedFixtureSlug-abc_123", true},
		{"https://clips.twitch.tv/embed?clip=CulturedFixtureSlug-abc_123", true},
		{"https://www.twitch.tv/channel/clip/CulturedFixtureSlug-abc_123", true},
		{"https://www.twitch.tv/clip/CulturedFixtureSlug-abc_123", true},
		{"ftp://www.twitch.tv/channel", false},
		{"https://example.com/channel", false},
		{"https://user:pass@www.twitch.tv/videos/123", false},
		{"https://www.twitch.tv:443/videos/123", false},
		{"https://www.twitch.tv/videos/not-numeric", false},
		{"https://clips.twitch.tv/bad%2Fslug", false},
	}
	for _, test := range tests {
		test := test
		t.Run(test.rawURL, func(t *testing.T) {
			parsed, err := url.Parse(test.rawURL)
			if err != nil {
				t.Fatal(err)
			}
			if got := NewTwitch().Suitable(parsed); got != test.want {
				t.Fatalf("Suitable(%q) = %t, want %t", test.rawURL, got, test.want)
			}
		})
	}
}

func TestTwitchVODMetadataReplayManifestChaptersAndStartOffset(t *testing.T) {
	transport := &twitchFixtureTransport{graphQLFixtures: []twitchGraphQLFixture{
		{body: twitchFixture(t, "vod_metadata.json")},
		{body: twitchFixture(t, "vod_access_token.json")},
	}}
	result, err := NewTwitch().Extract(context.Background(), Request{
		URL: "https://www.twitch.tv/videos/1234567890?t=5m10s", Transport: transport,
	})
	if err != nil {
		t.Fatal(err)
	}
	var expected struct {
		ID            string   `json:"id"`
		Title         string   `json:"title"`
		Description   string   `json:"description"`
		Uploader      string   `json:"uploader"`
		UploaderID    string   `json:"uploader_id"`
		LiveStatus    string   `json:"live_status"`
		Thumbnail     string   `json:"thumbnail"`
		Duration      int64    `json:"duration"`
		Timestamp     int64    `json:"timestamp"`
		ViewCount     int64    `json:"view_count"`
		StartTime     int64    `json:"start_time"`
		IsLive        bool     `json:"is_live"`
		WasLive       bool     `json:"was_live"`
		ChapterTitles []string `json:"chapter_titles"`
	}
	if err := json.Unmarshal(twitchFixture(t, "vod_expected.json"), &expected); err != nil {
		t.Fatal(err)
	}
	for key, want := range map[string]string{
		"id": expected.ID, "title": expected.Title, "description": expected.Description,
		"uploader": expected.Uploader, "uploader_id": expected.UploaderID, "live_status": expected.LiveStatus,
	} {
		assertTwitchString(t, result, key, want)
	}
	for key, want := range map[string]int64{"duration": expected.Duration, "timestamp": expected.Timestamp, "view_count": expected.ViewCount, "start_time": expected.StartTime} {
		if got, ok := result.Info.Lookup(key).Int(); !ok || got != want {
			t.Fatalf("%s = %d, %t; want %d", key, got, ok, want)
		}
	}
	if live, ok := result.Info.Lookup("is_live").Bool(); !ok || live != expected.IsLive {
		t.Fatalf("is_live = %v, %v", live, ok)
	}
	if wasLive, ok := result.Info.Lookup("was_live").Bool(); !ok || wasLive != expected.WasLive {
		t.Fatalf("was_live = %v, %v", wasLive, ok)
	}
	thumbnails, _ := result.Info.Lookup("thumbnails").ListValue()
	full, _ := thumbnails[0].Object()
	if thumbnail, _ := full.Lookup("url").StringValue(); thumbnail != expected.Thumbnail {
		t.Fatalf("full thumbnail = %q", thumbnail)
	}
	chapters, _ := result.Info.Lookup("chapters").ListValue()
	if len(chapters) != len(expected.ChapterTitles) {
		t.Fatalf("chapters = %#v", chapters)
	}
	for index, rawChapter := range chapters {
		chapter, _ := rawChapter.Object()
		if title, _ := chapter.Lookup("title").StringValue(); title != expected.ChapterTitles[index] {
			t.Fatalf("chapter %d title = %q", index, title)
		}
	}
	formats, _ := result.Info.Formats()
	format, _ := formats[0].Object()
	manifest, _ := format.Lookup("url").StringValue()
	parsed, err := url.Parse(manifest)
	if err != nil || parsed.Host != "usher.ttvnw.net" || parsed.Path != "/vod/1234567890.m3u8" || parsed.Query().Get("sig") != "fixture-vod-signature-do-not-log" || !strings.Contains(parsed.Query().Get("token"), `"vod_id":"1234567890"`) {
		t.Fatalf("VOD manifest = %s", parsed.Redacted())
	}
	assertTwitchVODRequests(t, transport.graphQLRequests)
}

func TestTwitchClipDirectLandscapePortraitAndMetadata(t *testing.T) {
	transport := &twitchFixtureTransport{graphQLFixtures: []twitchGraphQLFixture{{body: twitchFixture(t, "clip_metadata.json")}}}
	result, err := NewTwitch().Extract(context.Background(), Request{
		URL: "https://www.twitch.tv/fixture/clip/CulturedFixtureSlug-abc_123", Transport: transport,
	})
	if err != nil {
		t.Fatal(err)
	}
	var expected map[string]any
	if err := json.Unmarshal(twitchFixture(t, "clip_expected.json"), &expected); err != nil {
		t.Fatal(err)
	}
	projection := twitchClipProjection(t, result)
	if actual, _ := json.Marshal(projection); !reflect.DeepEqual(projection, expected) {
		t.Fatalf("clip metadata mismatch\nactual: %s\nexpected: %s", actual, twitchFixture(t, "clip_expected.json"))
	}
	formats, _ := result.Info.Formats()
	for _, rawFormat := range formats {
		format, _ := rawFormat.Object()
		rawURL, _ := format.Lookup("url").StringValue()
		parsed, err := url.Parse(rawURL)
		if err != nil || parsed.Query().Get("sig") != "fixture-clip-signature-do-not-log" || !strings.Contains(parsed.Query().Get("token"), `"clip":"CulturedFixtureSlug-abc_123"`) {
			t.Fatalf("signed clip format = %s", parsed.Redacted())
		}
	}
	if len(transport.graphQLRequests) != 1 || !bytes.Contains(transport.graphQLRequests[0].body, []byte(twitchOperationHashes["ShareClipRenderStatus"])) {
		t.Fatalf("clip GraphQL request = %#v", transport.graphQLRequests)
	}
	for _, rawFormat := range formats {
		format, _ := rawFormat.Object()
		formatID, _ := format.Lookup("format_id").StringValue()
		quality, ok := format.Lookup("quality").Int()
		if strings.HasPrefix(formatID, "portrait") {
			if !ok || quality != -2 {
				t.Fatalf("portrait format %q quality = %d, %t; want -2", formatID, quality, ok)
			}
		} else if ok {
			t.Fatalf("landscape format %q unexpectedly carries quality = %d", formatID, quality)
		}
	}
	thumbnails, _ := result.Info.Lookup("thumbnails").ListValue()
	wantThumbnails := []struct {
		id         string
		preference int64
	}{{"default", 0}, {"portrait", -1}, {"small", -2}}
	if len(thumbnails) != len(wantThumbnails) {
		t.Fatalf("thumbnail count = %d, want %d", len(thumbnails), len(wantThumbnails))
	}
	for index, want := range wantThumbnails {
		thumbnail, _ := thumbnails[index].Object()
		id, _ := thumbnail.Lookup("id").StringValue()
		preference, ok := thumbnail.Lookup("preference").Int()
		if id != want.id || !ok || preference != want.preference {
			t.Fatalf("thumbnail %d = (%q, %d, %t); want (%q, %d)", index, id, preference, ok, want.id, want.preference)
		}
	}
	archiveIDs, ok := result.Info.Lookup("_old_archive_ids").ListValue()
	if !ok || len(archiveIDs) != 1 {
		t.Fatalf("_old_archive_ids = %#v, %t", archiveIDs, ok)
	}
	if got, _ := archiveIDs[0].StringValue(); got != "twitchclips 246810" {
		t.Fatalf("_old_archive_ids[0] = %q, want %q", got, "twitchclips 246810")
	}
}

func TestTwitchClipLegacyArchiveIDDerivation(t *testing.T) {
	for _, test := range []struct {
		name     string
		finalURL string
		want     string
	}{
		{"encoded-marker", "https://clips-media.example.test/portrait-%7C246810-12.mp4", "twitchclips 246810"},
		{"no-suffix", "https://clips-media.example.test/portrait-%7C246810.mp4", "twitchclips 246810"},
		{"signed-query-ignored", "https://clips-media.example.test/portrait-%7C246810.mp4?sig=secret&token=do-not-log", "twitchclips 246810"},
		{"missing-marker", "https://clips-media.example.test/video-123456.mp4", ""},
		{"nonnumeric-marker", "https://clips-media.example.test/%7Cabc.mp4", ""},
		{"empty", "", ""},
		{"not-decoded", "https://clips-media.example.test/portrait-|246810.mp4", ""},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			if got := twitchClipLegacyArchiveID(test.finalURL); got != test.want {
				t.Fatalf("twitchClipLegacyArchiveID(%q) = %q, want %q", test.finalURL, got, test.want)
			}
		})
	}
}

func TestTwitchClipDuplicateSmallThumbnailSuppressed(t *testing.T) {
	var response []twitchClipResponse
	if err := json.Unmarshal(twitchFixture(t, "clip_metadata.json"), &response); err != nil {
		t.Fatal(err)
	}
	response[0].Data.Clip.ThumbnailURL = response[0].Data.Clip.Assets[0].ThumbnailURL
	body, _ := json.Marshal(response)
	transport := &twitchFixtureTransport{graphQLFixtures: []twitchGraphQLFixture{{body: body}}}
	result, err := NewTwitch().Extract(context.Background(), Request{
		URL: "https://www.twitch.tv/fixture/clip/CulturedFixtureSlug-abc_123", Transport: transport,
	})
	if err != nil {
		t.Fatal(err)
	}
	thumbnails, _ := result.Info.Lookup("thumbnails").ListValue()
	ids := make([]string, 0, len(thumbnails))
	for _, rawThumbnail := range thumbnails {
		thumbnail, _ := rawThumbnail.Object()
		id, _ := thumbnail.Lookup("id").StringValue()
		ids = append(ids, id)
	}
	if !reflect.DeepEqual(ids, []string{"default", "portrait"}) {
		t.Fatalf("thumbnail ids = %#v; duplicate small thumbnail must be suppressed", ids)
	}
}

func TestTwitchClipLegacyArchiveIDUsesFinalFormatAndRejectsUnsafe(t *testing.T) {
	load := func() []twitchClipResponse {
		var response []twitchClipResponse
		if err := json.Unmarshal(twitchFixture(t, "clip_metadata.json"), &response); err != nil {
			t.Fatal(err)
		}
		return response
	}
	extract := func(response []twitchClipResponse) (Extraction, error) {
		body, _ := json.Marshal(response)
		transport := &twitchFixtureTransport{graphQLFixtures: []twitchGraphQLFixture{{body: body}}}
		return NewTwitch().Extract(context.Background(), Request{
			URL: "https://www.twitch.tv/fixture/clip/CulturedFixtureSlug-abc_123", Transport: transport,
		})
	}
	archiveID := func(result Extraction) (string, bool) {
		items, ok := result.Info.Lookup("_old_archive_ids").ListValue()
		if !ok || len(items) == 0 {
			return "", false
		}
		id, _ := items[0].StringValue()
		return id, true
	}

	// A matching earlier format followed by a nonmatching final format yields no ID.
	response := load()
	response[0].Data.Clip.Assets[0].VideoQualities[0].SourceURL = "https://clips-media.example.test/720-%7C111222.mp4"
	response[0].Data.Clip.Assets[1].VideoQualities[0].SourceURL = "https://clips-media.example.test/portrait-plain.mp4"
	if result, err := extract(response); err != nil {
		t.Fatal(err)
	} else if id, present := archiveID(result); present {
		t.Fatalf("expected no archive id from nonmatching final format, got %q", id)
	}

	// Rejected/unsafe final source cannot create an archive ID.
	response = load()
	response[0].Data.Clip.Assets[1].VideoQualities[0].SourceURL = "https://127.0.0.1/portrait-%7C333444.mp4"
	if result, err := extract(response); err != nil {
		t.Fatal(err)
	} else if id, present := archiveID(result); present {
		t.Fatalf("expected no archive id from unsafe final format, got %q", id)
	}

	// No match means the field is absent, not an empty list.
	response = load()
	response[0].Data.Clip.Assets[0].VideoQualities[0].SourceURL = "https://clips-media.example.test/720.mp4"
	response[0].Data.Clip.Assets[0].VideoQualities[1].SourceURL = "https://clips-media.example.test/480.mp4"
	response[0].Data.Clip.Assets[1].VideoQualities[0].SourceURL = "https://clips-media.example.test/portrait-720.mp4"
	if result, err := extract(response); err != nil {
		t.Fatal(err)
	} else if !result.Info.Lookup("_old_archive_ids").IsMissing() {
		t.Fatalf("_old_archive_ids should be absent when no match exists")
	}
}

func TestTwitchVODAndClipFailuresAreBoundedCategorizedAndRedacted(t *testing.T) {
	const secret = "transport-secret-must-not-leak"
	for _, rawURL := range []string{
		"https://www.twitch.tv/videos/1234567890",
		"https://clips.twitch.tv/CulturedFixtureSlug-abc_123",
	} {
		transport := &twitchFixtureTransport{graphQLFixtures: []twitchGraphQLFixture{{err: errors.New(secret)}}}
		_, err := NewTwitch().Extract(context.Background(), Request{URL: rawURL, Transport: transport})
		if !errors.Is(err, ErrTwitchNetwork) || strings.Contains(fmt.Sprint(err), secret) {
			t.Fatalf("network error for %s = %v", rawURL, err)
		}
	}
	missingVOD := &twitchFixtureTransport{graphQLFixtures: []twitchGraphQLFixture{{body: []byte(`[ {"data":{"video":null}}, {"data":{}}, {"data":{}} ]`)}}}
	if _, err := NewTwitch().Extract(context.Background(), Request{URL: "https://twitch.tv/videos/123", Transport: missingVOD}); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("missing VOD error = %v", err)
	}
	missingClip := &twitchFixtureTransport{graphQLFixtures: []twitchGraphQLFixture{{body: []byte(`[{"data":{"clip":null}}]`)}}}
	if _, err := NewTwitch().Extract(context.Background(), Request{URL: "https://clips.twitch.tv/CulturedFixtureSlug-abc_123", Transport: missingClip}); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("missing clip error = %v", err)
	}
	vodAuth := &twitchFixtureTransport{graphQLFixtures: []twitchGraphQLFixture{
		{body: twitchFixture(t, "vod_metadata.json")},
		{body: []byte(`{"data":{"videoPlaybackAccessToken":null}}`)},
	}}
	if _, err := NewTwitch().Extract(context.Background(), Request{URL: "https://twitch.tv/videos/1234567890", Transport: vodAuth}); !errors.Is(err, ErrAuthentication) {
		t.Fatalf("VOD auth error = %v", err)
	}
	var vodResponses []twitchVODResponse
	if err := json.Unmarshal(twitchFixture(t, "vod_metadata.json"), &vodResponses); err != nil {
		t.Fatal(err)
	}
	vodResponses[1].Data.Video.Moments.Edges = make([]twitchMomentEdge, twitchMaxMoments+1)
	oversizedVODBody, _ := json.Marshal(vodResponses)
	oversizedVOD := &twitchFixtureTransport{graphQLFixtures: []twitchGraphQLFixture{{body: oversizedVODBody}}}
	if _, err := NewTwitch().Extract(context.Background(), Request{URL: "https://twitch.tv/videos/1234567890", Transport: oversizedVOD}); !errors.Is(err, ErrInvalidMetadata) {
		t.Fatalf("oversized VOD error = %v", err)
	}
	geo := &twitchFixtureTransport{graphQLFixtures: []twitchGraphQLFixture{{status: http.StatusUnavailableForLegalReasons, body: []byte(secret)}}}
	if _, err := NewTwitch().Extract(context.Background(), Request{URL: "https://clips.twitch.tv/CulturedFixtureSlug-abc_123", Transport: geo}); !errors.Is(err, ErrRegionRestricted) || strings.Contains(fmt.Sprint(err), secret) {
		t.Fatalf("clip geo error = %v", err)
	}

	var response []twitchClipResponse
	if err := json.Unmarshal(twitchFixture(t, "clip_metadata.json"), &response); err != nil {
		t.Fatal(err)
	}
	response[0].Data.Clip.PlaybackAccessToken = twitchAccessToken{}
	missingTokenBody, _ := json.Marshal(response)
	missingToken := &twitchFixtureTransport{graphQLFixtures: []twitchGraphQLFixture{{body: missingTokenBody}}}
	if _, err := NewTwitch().Extract(context.Background(), Request{URL: "https://clips.twitch.tv/CulturedFixtureSlug-abc_123", Transport: missingToken}); !errors.Is(err, ErrAuthentication) {
		t.Fatalf("clip auth error = %v", err)
	}
	if err := json.Unmarshal(twitchFixture(t, "clip_metadata.json"), &response); err != nil {
		t.Fatal(err)
	}
	response[0].Data.Clip.Assets[0].VideoQualities[0].SourceURL = "https://127.0.0.1/private.mp4"
	response[0].Data.Clip.Assets[0].VideoQualities[1].SourceURL = "https://metadata.internal/private.mp4"
	response[0].Data.Clip.Assets[1].VideoQualities[0].SourceURL = "https://clips-media.example.test:8443/private.mp4"
	unsafeBody, _ := json.Marshal(response)
	unsafeClip := &twitchFixtureTransport{graphQLFixtures: []twitchGraphQLFixture{{body: unsafeBody}}}
	if _, err := NewTwitch().Extract(context.Background(), Request{URL: "https://clips.twitch.tv/CulturedFixtureSlug-abc_123", Transport: unsafeClip}); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("unsafe clip asset error = %v", err)
	}

	response[0].Data.Clip.Assets = make([]twitchClipAsset, twitchMaxAssets+1)
	oversizedBody, _ := json.Marshal(response)
	oversized := &twitchFixtureTransport{graphQLFixtures: []twitchGraphQLFixture{{body: oversizedBody}}}
	if _, err := NewTwitch().Extract(context.Background(), Request{URL: "https://clips.twitch.tv/CulturedFixtureSlug-abc_123", Transport: oversized}); !errors.Is(err, ErrInvalidMetadata) {
		t.Fatalf("oversized clip error = %v", err)
	}
}

func TestTwitchRerunLiveStateAndMissingOptionalFields(t *testing.T) {
	transport := &twitchFixtureTransport{
		metadata: []byte(`[
          {"data":{"user":{"stream":{"id":"rerun-id","type":"rerun"}}}},
          {"data":{"user":{"displayName":"Fixture Rerun","broadcastSettings":{"title":"Replay"}}}},
          {"data":{"user":{"stream":{}}}}
        ]`),
		token: twitchFixture(t, "access_token.json"),
	}
	result, err := NewTwitch().Extract(context.Background(), Request{URL: "https://twitch.tv/rerun_channel", Transport: transport})
	if err != nil {
		t.Fatal(err)
	}
	assertTwitchString(t, result, "title", "Fixture Rerun (rerun)")
	assertTwitchString(t, result, "live_status", "not_live")
	if got, ok := result.Info.Lookup("is_live").Bool(); !ok || got {
		t.Fatalf("is_live = %t, %t; want false, true", got, ok)
	}
	if !result.Info.Lookup("view_count").IsMissing() || !result.Info.Lookup("timestamp").IsMissing() {
		t.Fatalf("absent optional fields were materialized: %#v", result.Info.Fields().Fields())
	}
}

func TestTwitchManifestURLIncludesReferenceParameters(t *testing.T) {
	token := twitchAccessToken{Value: `{"channel":"fixture/channel","expires":4102444800}`, Signature: "sig+/="}
	rawURL := twitchManifestURL("fixture_channel", token)
	parsed, err := url.Parse(rawURL)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Scheme != "https" || parsed.Host != "usher.ttvnw.net" || parsed.Path != "/api/channel/hls/fixture_channel.m3u8" {
		t.Fatalf("manifest endpoint = %s", parsed.Redacted())
	}
	want := map[string]string{
		"allow_source": "true", "allow_audio_only": "true", "allow_spectre": "true",
		"platform": "web", "player": "twitchweb", "supported_codecs": "av1,h265,h264",
		"playlist_include_framerate": "true", "sig": token.Signature, "token": token.Value,
	}
	for key, value := range want {
		if got := parsed.Query().Get(key); got != value {
			t.Fatalf("manifest query %s = %q, want %q", key, got, value)
		}
	}
	cacheBuster, err := strconv.ParseInt(parsed.Query().Get("p"), 10, 64)
	if err != nil || cacheBuster < 1_000_000 || cacheBuster > 10_000_000 {
		t.Fatalf("manifest cache-buster p = %q", parsed.Query().Get("p"))
	}
}

func TestTwitchExtractAndDownloadLiveHLS(t *testing.T) {
	transport := &twitchFixtureTransport{
		metadata: twitchFixture(t, "metadata.json"),
		token:    twitchFixture(t, "access_token.json"),
	}
	result, err := NewTwitch().Extract(context.Background(), Request{
		URL: "https://www.twitch.tv/Fixture_Channel", Transport: transport,
	})
	if err != nil {
		t.Fatal(err)
	}

	var expected struct {
		ID          string `json:"id"`
		DisplayID   string `json:"display_id"`
		Title       string `json:"title"`
		Description string `json:"description"`
		Uploader    string `json:"uploader"`
		UploaderID  string `json:"uploader_id"`
		Timestamp   int64  `json:"timestamp"`
		ViewCount   int64  `json:"view_count"`
		IsLive      bool   `json:"is_live"`
		LiveStatus  string `json:"live_status"`
		Thumbnail   string `json:"thumbnail"`
	}
	if err := json.Unmarshal(twitchFixture(t, "expected.json"), &expected); err != nil {
		t.Fatal(err)
	}
	assertTwitchString(t, result, "id", expected.ID)
	assertTwitchString(t, result, "display_id", expected.DisplayID)
	assertTwitchString(t, result, "title", expected.Title)
	assertTwitchString(t, result, "description", expected.Description)
	assertTwitchString(t, result, "uploader", expected.Uploader)
	assertTwitchString(t, result, "uploader_id", expected.UploaderID)
	assertTwitchString(t, result, "live_status", expected.LiveStatus)
	if got, ok := result.Info.Lookup("timestamp").Int(); !ok || got != expected.Timestamp {
		t.Fatalf("timestamp = %d, %t; want %d", got, ok, expected.Timestamp)
	}
	if got, ok := result.Info.Lookup("view_count").Int(); !ok || got != expected.ViewCount {
		t.Fatalf("view_count = %d, %t; want %d", got, ok, expected.ViewCount)
	}
	if got, ok := result.Info.Lookup("is_live").Bool(); !ok || got != expected.IsLive {
		t.Fatalf("is_live = %t, %t; want %t", got, ok, expected.IsLive)
	}
	thumbnails, ok := result.Info.Lookup("thumbnails").ListValue()
	if !ok || len(thumbnails) != 2 {
		t.Fatalf("thumbnails = %#v", thumbnails)
	}
	full, _ := thumbnails[0].Object()
	if got, _ := full.Lookup("url").StringValue(); got != expected.Thumbnail {
		t.Fatalf("full-size thumbnail = %q, want %q", got, expected.Thumbnail)
	}

	formats, ok := result.Info.Formats()
	if !ok || len(formats) != 1 {
		t.Fatalf("formats = %#v", formats)
	}
	format, ok := formats[0].Object()
	if !ok {
		t.Fatal("format is not an object")
	}
	manifestURL, ok := format.Lookup("url").StringValue()
	if !ok {
		t.Fatal("format URL missing")
	}
	root := t.TempDir()
	destination := filepath.Join(root, "live.ts")
	var emitted []events.Event
	sink := events.SinkFunc(func(_ context.Context, event events.Event) error {
		emitted = append(emitted, event)
		return nil
	})
	download, err := hls.NewDownloader(transport, hls.Config{PollInterval: time.Millisecond, MaxPolls: 3}).Download(
		context.Background(), manifestURL, root, destination, false, sink)
	if err != nil {
		t.Fatal(err)
	}
	contents, err := os.ReadFile(destination)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(contents), "ten-eleven"; got != want {
		t.Fatalf("downloaded content = %q, want %q", got, want)
	}
	if download.Bytes != int64(len(contents)) || transport.mediaPolls != 2 {
		t.Fatalf("download = %#v, polls = %d", download, transport.mediaPolls)
	}
	for _, event := range emitted {
		serialized, err := json.Marshal(event)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(serialized), "fixture-signature-do-not-log") || strings.Contains(string(serialized), "4102444800") {
			t.Fatalf("event exposed signed manifest credentials: %s", serialized)
		}
	}
	assertTwitchGraphQLRequests(t, transport.graphQLRequests)
}

func TestTwitchCategorizesFailures(t *testing.T) {
	validMetadata := twitchFixture(t, "metadata.json")
	validToken := twitchFixture(t, "access_token.json")
	tests := []struct {
		name           string
		metadata       string
		token          []byte
		metadataStatus int
		tokenStatus    int
		want           error
	}{
		{name: "user missing", metadata: `[{"data":{"user":null}},{"data":{}},{"data":{}}]`, token: validToken, want: ErrUnavailable},
		{name: "not live", metadata: `[{"data":{"user":{"stream":null}}},{"data":{}},{"data":{}}]`, token: validToken, want: ErrUnavailable},
		{name: "metadata authentication", metadata: `{}`, metadataStatus: http.StatusUnauthorized, want: ErrAuthentication},
		{name: "metadata unavailable", metadata: `{}`, metadataStatus: http.StatusNotFound, want: ErrUnavailable},
		{name: "metadata malformed", metadata: `{`, want: ErrInvalidMetadata},
		{name: "metadata operation error", metadata: `[{"data":{"user":{}},"errors":[{"message":"bad"}]},{"data":{}},{"data":{}}]`, want: ErrInvalidMetadata},
		{name: "token authentication", metadata: string(validMetadata), tokenStatus: http.StatusForbidden, want: ErrAuthentication},
		{name: "token missing", metadata: string(validMetadata), token: []byte(`{"data":{"streamPlaybackAccessToken":null}}`), want: ErrAuthentication},
		{name: "token graphql error", metadata: string(validMetadata), token: []byte(`{"errors":[{"message":"denied"}]}`), want: ErrAuthentication},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			transport := &twitchFixtureTransport{
				metadata: []byte(test.metadata), token: test.token,
				metadataStatus: test.metadataStatus, tokenStatus: test.tokenStatus,
			}
			_, err := NewTwitch().Extract(context.Background(), Request{URL: "https://twitch.tv/fixture_channel", Transport: transport})
			if !errors.Is(err, test.want) {
				t.Fatalf("Extract() error = %v, want category %v", err, test.want)
			}
		})
	}
}

func TestTwitchErrorsDoNotExposeResponseSecrets(t *testing.T) {
	const secret = "fixture-response-secret"
	transport := &twitchFixtureTransport{
		metadata: twitchFixture(t, "metadata.json"),
		token:    []byte(`{"data":{"streamPlaybackAccessToken":{"value":"` + secret + `","signature":"signature"}}} trailing`),
	}
	_, err := NewTwitch().Extract(context.Background(), Request{URL: "https://twitch.tv/fixture_channel", Transport: transport})
	if !errors.Is(err, ErrInvalidMetadata) {
		t.Fatalf("Extract() error = %v", err)
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("error exposed response secret: %v", err)
	}
}

func TestTwitchExtractHonorsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for _, rawURL := range []string{
		"https://twitch.tv/fixture_channel",
		"https://twitch.tv/videos/1234567890",
		"https://clips.twitch.tv/CulturedFixtureSlug-abc_123",
		"https://www.twitch.tv/fixture_channel/videos",
		"https://www.twitch.tv/collections/FixtureCollection01",
		"https://www.twitch.tv/fixture_channel/videos?filter=collections",
		"https://www.twitch.tv/fixture_channel/clips",
		"https://www.twitch.tv/fixture_channel/videos?filter=clips",
	} {
		_, err := NewTwitch().Extract(ctx, Request{URL: rawURL, Transport: &twitchFixtureTransport{}})
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Extract(%q) error = %v, want context.Canceled", rawURL, err)
		}
	}
}

func assertTwitchString(t *testing.T, result Extraction, key, want string) {
	t.Helper()
	got, ok := result.Info.Lookup(key).StringValue()
	if !ok || got != want {
		t.Fatalf("%s = %q, %t; want %q", key, got, ok, want)
	}
}

func assertTwitchGraphQLRequests(t *testing.T, requests []twitchRecordedRequest) {
	t.Helper()
	if len(requests) != 2 {
		t.Fatalf("GraphQL request count = %d, want 2", len(requests))
	}
	for index, request := range requests {
		if got := request.header.Get("Client-ID"); got != twitchClientID {
			t.Fatalf("request %d Client-ID = %q", index, got)
		}
		if got := request.header.Get("Content-Type"); got != "text/plain;charset=UTF-8" {
			t.Fatalf("request %d Content-Type = %q", index, got)
		}
	}
	var operations []struct {
		OperationName string `json:"operationName"`
		Variables     struct {
			ChannelLogin string `json:"channelLogin"`
			IncludeIsDJ  bool   `json:"includeIsDJ"`
		} `json:"variables"`
		Extensions struct {
			PersistedQuery struct {
				Version    int    `json:"version"`
				SHA256Hash string `json:"sha256Hash"`
			} `json:"persistedQuery"`
		} `json:"extensions"`
	}
	if err := json.Unmarshal(requests[0].body, &operations); err != nil {
		t.Fatal(err)
	}
	wantNames := []string{"StreamMetadata", "ComscoreStreamingQuery", "VideoPreviewOverlay"}
	if len(operations) != len(wantNames) {
		t.Fatalf("operation count = %d", len(operations))
	}
	for index, operation := range operations {
		if operation.OperationName != wantNames[index] || operation.Extensions.PersistedQuery.Version != 1 || operation.Extensions.PersistedQuery.SHA256Hash != twitchOperationHashes[operation.OperationName] {
			t.Fatalf("operation %d = %#v", index, operation)
		}
	}
	if operations[0].Variables.ChannelLogin != "fixture_channel" || !operations[0].Variables.IncludeIsDJ {
		t.Fatalf("StreamMetadata variables = %#v", operations[0].Variables)
	}
	var tokenRequest map[string]string
	if err := json.Unmarshal(requests[1].body, &tokenRequest); err != nil {
		t.Fatal(err)
	}
	query := tokenRequest["query"]
	for _, required := range []string{"streamPlaybackAccessToken", `channelName: "fixture_channel"`, `platform: "web"`, `playerBackend: "mediaplayer"`, `playerType: "site"`} {
		if !strings.Contains(query, required) {
			t.Fatalf("token query missing %q: %s", required, query)
		}
	}
}

func assertTwitchVODRequests(t *testing.T, requests []twitchRecordedRequest) {
	t.Helper()
	if len(requests) != 2 {
		t.Fatalf("VOD GraphQL request count = %d", len(requests))
	}
	var operations []struct {
		OperationName string `json:"operationName"`
		Extensions    struct {
			PersistedQuery struct {
				Version    int    `json:"version"`
				SHA256Hash string `json:"sha256Hash"`
			} `json:"persistedQuery"`
		} `json:"extensions"`
	}
	if err := json.Unmarshal(requests[0].body, &operations); err != nil {
		t.Fatal(err)
	}
	want := []string{"VideoMetadata", "VideoPlayer_ChapterSelectButtonVideo", "VideoPlayer_VODSeekbarPreviewVideo"}
	if len(operations) != len(want) {
		t.Fatalf("VOD operations = %#v", operations)
	}
	for index, operation := range operations {
		if operation.OperationName != want[index] || operation.Extensions.PersistedQuery.Version != 1 || operation.Extensions.PersistedQuery.SHA256Hash != twitchOperationHashes[want[index]] {
			t.Fatalf("VOD operation %d = %#v", index, operation)
		}
	}
	var token map[string]string
	if err := json.Unmarshal(requests[1].body, &token); err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{"videoPlaybackAccessToken", `id: "1234567890"`, `platform: "web"`} {
		if !strings.Contains(token["query"], required) {
			t.Fatalf("VOD token query missing %q: %s", required, token["query"])
		}
	}
}

func twitchClipProjection(t *testing.T, result Extraction) map[string]any {
	t.Helper()
	data, err := json.Marshal(result.Info.Fields())
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err := json.Unmarshal(data, &fields); err != nil {
		t.Fatal(err)
	}
	formats, _ := fields["formats"].([]any)
	formatIDs := make([]string, 0, len(formats))
	for _, raw := range formats {
		format := raw.(map[string]any)
		formatIDs = append(formatIDs, format["format_id"].(string))
	}
	thumbnails, _ := fields["thumbnails"].([]any)
	thumbnailIDs := make([]string, 0, len(thumbnails))
	for _, raw := range thumbnails {
		thumbnail := raw.(map[string]any)
		thumbnailIDs = append(thumbnailIDs, thumbnail["id"].(string))
	}
	delete(fields, "formats")
	delete(fields, "thumbnails")
	delete(fields, "webpage_url")
	delete(fields, "ext")
	delete(fields, "is_live")
	fields["format_ids"] = formatIDs
	fields["thumbnail_ids"] = thumbnailIDs
	normalized, _ := json.Marshal(fields)
	if err := json.Unmarshal(normalized, &fields); err != nil {
		t.Fatal(err)
	}
	return fields
}

func FuzzTwitchMetadataResponse(f *testing.F) {
	f.Add(twitchFixture(f, "metadata.json"))
	f.Add([]byte(`[{"data":{"user":null}},{"data":{}},{"data":{}}]`))
	f.Add([]byte(`{"malformed":`))
	f.Fuzz(func(t *testing.T, body []byte) {
		transport := &twitchFixtureTransport{metadata: body}
		_, _ = requestTwitchMetadata(context.Background(), transport, "fixture_channel")
	})
}

func FuzzTwitchRouting(f *testing.F) {
	for _, seed := range []string{
		"https://www.twitch.tv/channel",
		"https://www.twitch.tv/videos/1234567890?t=5m10s",
		"https://clips.twitch.tv/CulturedFixtureSlug-abc_123",
		"https://www.twitch.tv/channel/clip/CulturedFixtureSlug-abc_123",
		"https://www.twitch.tv/fixture_channel/videos",
		"https://www.twitch.tv/fixture_channel/videos/all?filter=archives&sort=views",
		"https://www.twitch.tv/fixture_channel/profile",
		"https://user:pass@www.twitch.tv/fixture_channel/videos",
		"https://www.twitch.tv:443/fixture_channel/videos",
		"https://www.twitch.tv/fixture_channel/videos?filter=clips",
		"https://www.twitch.tv/fixture_channel/videos#x",
		"https://www.twitch.tv/fixture_channel/videos?filter=%zz",
		"https://www.twitch.tv/fixture_channel/videos?filter=archives%00",
		"https://www.twitch.tv/fixture_channel/videos?filter=archives&filter=highlights",
		"https://www.twitch.tv/fixture_channel/videos?filter=archives&feature=share",
		"https://www.twitch.tv/fixture_channel/videos?filter=",
		"https://www.twitch.tv/fixture_channel/videos?sort=",
		"https://www.twitch.tv/fixture_channel/videos?filter=%FF",
		"https://www.twitch.tv/fixture_channel/videos?sort=%FF",
		"https://www.twitch.tv/collections/FixtureCollection01",
		"https://www.twitch.tv/collections/FixtureCollection01?feature=share",
		"https://www.twitch.tv/collections/bad%2Fid",
		"https://www.twitch.tv/collections/FixtureCollection01#x",
		"https://www.twitch.tv/collections/FixtureCollection01?x=%zz",
		"https://www.twitch.tv/collections/FixtureCollection01?x=%00",
		"https://www.twitch.tv/collections/FixtureCollection01?x=%FF",
		"https://www.twitch.tv/fixture_channel/videos?filter=collections",
		"https://www.twitch.tv/fixture_channel/videos?filter=collections&feature=share",
		"https://www.twitch.tv/fixture_channel/videos?filter=collections&filter=clips",
		"https://www.twitch.tv/fixture_channel/profile?filter=collections",
		"https://www.twitch.tv/fixture_channel/videos/all?filter=collections",
		"https://www.twitch.tv/fixture_channel/videos?filter=collections&sort=views",
		"https://www.twitch.tv/fixture_channel/videos?filter=collections&sort=time&sort=views",
		"https://www.twitch.tv/fixture_channel/videos?filter=collections&sort=%FF",
		"https://www.twitch.tv/fixture_channel/videos?filter=collections&sort=time%00",
		"https://www.twitch.tv/fixture_channel/clips",
		"https://www.twitch.tv/fixture_channel/clips?range=all",
		"https://www.twitch.tv/fixture_channel/videos?filter=clips&range=7d",
		"https://www.twitch.tv/fixture_channel/clips?range=24hr&range=7d",
		"https://www.twitch.tv/fixture_channel/clips#x",
		"https://www.twitch.tv/fixture_channel/clips?range=%zz",
		"https://www.twitch.tv/fixture_channel/clips?range=%FF",
		"https://www.twitch.tv/fixture_channel/clips?range=all%00",
		"https://www.twitch.tv/fixture_channel/profile?filter=clips",
		"https://www.twitch.tv/fixture_channel/videos/all?filter=clips",
		"https://www.twitch.tv/directory/clips",
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, rawURL string) {
		if len(rawURL) > 1<<20 {
			t.Skip()
		}
		parsed, err := url.Parse(rawURL)
		if err != nil {
			return
		}
		target, ok := classifyTwitchURL(parsed)
		if !ok {
			return
		}
		if parsed.User != nil || parsed.Port() != "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
			t.Fatalf("accepted hostile URL %q as %#v", rawURL, target)
		}
		host := strings.ToLower(parsed.Hostname())
		switch host {
		case "twitch.tv", "www.twitch.tv", "go.twitch.tv", "m.twitch.tv", "player.twitch.tv", "clips.twitch.tv":
		default:
			t.Fatalf("accepted lookalike host %q", host)
		}
		switch target.kind {
		case twitchKindLive, twitchKindVideos, twitchKindChannelCollections, twitchKindChannelClips:
			if !twitchChannelPattern.MatchString(target.id) {
				t.Fatalf("accepted malformed channel %q", target.id)
			}
			if _, reserved := twitchReservedPaths[target.id]; reserved {
				t.Fatalf("accepted reserved channel %q", target.id)
			}
			if (target.kind == twitchKindVideos || target.kind == twitchKindChannelCollections || target.kind == twitchKindChannelClips) && parsed.Fragment != "" {
				t.Fatalf("accepted videos fragment URL %q", rawURL)
			}
			if target.kind == twitchKindVideos || target.kind == twitchKindChannelCollections || target.kind == twitchKindChannelClips {
				if _, err := url.ParseQuery(parsed.RawQuery); err != nil {
					t.Fatalf("accepted malformed RawQuery %q", rawURL)
				}
			}
			if target.kind == twitchKindChannelClips {
				if target.clips.filter == "" || target.clips.label == "" {
					t.Fatalf("clips query missing %#v", target.clips)
				}
			}
		case twitchKindCollection:
			if !twitchCollectionPattern.MatchString(target.id) {
				t.Fatalf("accepted malformed collection %q", target.id)
			}
			if parsed.Fragment != "" {
				t.Fatalf("accepted collection fragment URL %q", rawURL)
			}
			if !twitchRouteQuerySafe(parsed) {
				t.Fatalf("accepted collection with hostile query %q", rawURL)
			}
		case twitchKindVOD:
			if !twitchVODPattern.MatchString(target.id) {
				t.Fatalf("accepted malformed VOD %q", target.id)
			}
		case twitchKindClip:
			if !twitchClipPattern.MatchString(target.id) {
				t.Fatalf("accepted malformed clip %q", target.id)
			}
		default:
			t.Fatalf("unknown kind %#v", target)
		}
	})
}

func FuzzTwitchVODMetadataResponse(f *testing.F) {
	f.Add(twitchFixture(f, "vod_metadata.json"))
	f.Add([]byte(`[{"data":{"video":null}},{"data":{}},{"data":{}}]`))
	f.Add([]byte(`{"malformed":`))
	f.Fuzz(func(t *testing.T, body []byte) {
		if len(body) > 1<<20 {
			t.Skip()
		}
		transport := &twitchFixtureTransport{graphQLFixtures: []twitchGraphQLFixture{{body: body}}}
		_, _ = requestTwitchVODMetadata(context.Background(), transport, "1234567890")
	})
}

func FuzzTwitchClipResponse(f *testing.F) {
	f.Add(twitchFixture(f, "clip_metadata.json"))
	f.Add([]byte(`[{"data":{"clip":null}}]`))
	f.Add([]byte(`[]`))
	f.Fuzz(func(t *testing.T, body []byte) {
		if len(body) > 1<<20 {
			t.Skip()
		}
		transport := &twitchFixtureTransport{graphQLFixtures: []twitchGraphQLFixture{{body: body}}}
		_, _ = extractTwitchClip(context.Background(), transport, twitchTarget{kind: twitchKindClip, id: "CulturedFixtureSlug-abc_123"})
	})
}

func FuzzTwitchVideosPageResponse(f *testing.F) {
	f.Add(twitchFixture(f, "videos_page1.json"))
	f.Add(twitchFixture(f, "videos_page2.json"))
	f.Add(twitchFixture(f, "videos_empty.json"))
	f.Add(twitchFixture(f, "videos_not_found.json"))
	f.Add(twitchFixture(f, "videos_malformed.json"))
	f.Add([]byte(`[{"data":{"user":null}}]`))
	f.Add([]byte(`[]`))
	f.Fuzz(func(t *testing.T, body []byte) {
		if len(body) > 1<<20 {
			t.Skip()
		}
		var response []twitchVideosPageResponse
		if err := json.Unmarshal(body, &response); err != nil {
			return
		}
		entries, cursor, err := parseTwitchVideosPage(response)
		if err != nil {
			if strings.Contains(err.Error(), string(body)) {
				t.Fatalf("error exposed body: %v", err)
			}
			return
		}
		if len(cursor) > twitchVideosMaxCursor {
			t.Fatalf("cursor exceeds bound: %d", len(cursor))
		}
		if len(entries) > twitchVideosMaxEdges {
			t.Fatalf("entries exceed bound: %d", len(entries))
		}
		for _, entry := range entries {
			if !strings.HasPrefix(entry.URL, "https://www.twitch.tv/videos/") {
				t.Fatalf("hostile entry URL %q", entry.URL)
			}
			id := strings.TrimPrefix(entry.ID, "v")
			if !twitchVODPattern.MatchString(id) || entry.ExtractorKey != "twitch" || !entry.Transparent {
				t.Fatalf("unsafe entry %#v", entry)
			}
			if len(entry.Title) > twitchVideosMaxString {
				t.Fatalf("title exceeds bound")
			}
		}
	})
}

func TestParseTwitchStartTime(t *testing.T) {
	tests := map[string]int64{"0": 0, "310": 310, "5m10s": 310, "1h2m3s": 3723}
	keys := make([]string, 0, len(tests))
	for input := range tests {
		keys = append(keys, input)
	}
	sort.Strings(keys)
	for _, input := range keys {
		if got, ok := parseTwitchStartTime(input); !ok || got != tests[input] {
			t.Errorf("parseTwitchStartTime(%q) = %d, %t", input, got, ok)
		}
	}
	for _, input := range []string{"", "5mjunk", "1s2m", "-1", "999999999999s"} {
		if got, ok := parseTwitchStartTime(input); ok {
			t.Errorf("parseTwitchStartTime(%q) = %d, true", input, got)
		}
	}
}

func TestTwitchVideosPlaylistRoutingFiltersSortsAndHostiles(t *testing.T) {
	t.Parallel()
	tests := []struct {
		rawURL string
		kind   twitchKind
		id     string
		label  string
		sort   string
		ok     bool
	}{
		{"https://www.twitch.tv/Fixture_Channel/videos", twitchKindVideos, "fixture_channel", "All Videos", "TIME", true},
		{"https://www.twitch.tv/fixture_channel/videos/all", twitchKindVideos, "fixture_channel", "All Videos", "TIME", true},
		{"https://www.twitch.tv/fixture_channel/profile", twitchKindVideos, "fixture_channel", "All Videos", "TIME", true},
		{"https://m.twitch.tv/fixture_channel/videos?filter=all", twitchKindVideos, "fixture_channel", "All Videos", "TIME", true},
		{"https://go.twitch.tv/fixture_channel/videos?filter=archives", twitchKindVideos, "fixture_channel", "Past Broadcasts", "TIME", true},
		{"https://www.twitch.tv/fixture_channel/videos?filter=highlights&sort=time", twitchKindVideos, "fixture_channel", "Highlights", "TIME", true},
		{"https://www.twitch.tv/fixture_channel/videos?filter=uploads&sort=views", twitchKindVideos, "fixture_channel", "Uploads", "VIEWS", true},
		{"https://www.twitch.tv/fixture_channel/videos?filter=past_premieres", twitchKindVideos, "fixture_channel", "Past Premieres", "TIME", true},
		{"https://www.twitch.tv/fixture_channel/videos?filter=unknown_filter", twitchKindVideos, "fixture_channel", "All Videos", "TIME", true},
		{"https://www.twitch.tv/fixture_channel/videos?sort=views", twitchKindVideos, "fixture_channel", "All Videos", "VIEWS", true},
		{"https://www.twitch.tv/fixture_channel/videos?sort=weird", twitchKindVideos, "fixture_channel", "All Videos", "WEIRD", true},
		{"https://www.twitch.tv/fixture_channel/videos?filter=archives&feature=share", twitchKindVideos, "fixture_channel", "Past Broadcasts", "TIME", true},
		{"https://www.twitch.tv/fixture_channel/videos?filter=", twitchKindVideos, "fixture_channel", "All Videos", "TIME", true},
		{"https://www.twitch.tv/fixture_channel/videos?sort=", twitchKindVideos, "fixture_channel", "All Videos", "TIME", true},
		{"https://www.twitch.tv/fixture_channel/videos?filter=&sort=", twitchKindVideos, "fixture_channel", "All Videos", "TIME", true},
		{"https://www.twitch.tv/videos/1000000001", twitchKindVOD, "1000000001", "", "", true},
		{"https://www.twitch.tv/fixture_channel", twitchKindLive, "fixture_channel", "", "", true},
		{"https://www.twitch.tv/fixture_channel/videos?filter=clips", twitchKindChannelClips, "fixture_channel", "", "", true},
		{"https://www.twitch.tv/fixture_channel/clips", twitchKindChannelClips, "fixture_channel", "", "", true},
		{"https://www.twitch.tv/fixture_channel/clips?range=24hr", twitchKindChannelClips, "fixture_channel", "", "", true},
		{"https://www.twitch.tv/fixture_channel/videos?filter=clips&range=all", twitchKindChannelClips, "fixture_channel", "", "", true},
		{"https://m.twitch.tv/fixture_channel/videos?filter=clips&range=7d", twitchKindChannelClips, "fixture_channel", "", "", true},
		{"https://www.twitch.tv/fixture_channel/profile?filter=clips", 0, "", "", "", false},
		{"https://www.twitch.tv/fixture_channel/videos/all?filter=clips", 0, "", "", "", false},
		{"https://www.twitch.tv/fixture_channel/clips/extra", 0, "", "", "", false},
		{"https://www.twitch.tv/fixture_channel/clips#x", 0, "", "", "", false},
		{"https://www.twitch.tv/fixture_channel/videos?filter=clips&range=24hr&range=7d", 0, "", "", "", false},
		{"https://www.twitch.tv/fixture_channel/videos?filter=clips&filter=archives", 0, "", "", "", false},
		{"https://www.twitch.tv/fixture_channel/clips?range=%zz", 0, "", "", "", false},
		{"https://www.twitch.tv/fixture_channel/clips?range=all%00", 0, "", "", "", false},
		{"https://www.twitch.tv/fixture_channel/clips?range=%FF", 0, "", "", "", false},
		{"https://www.twitch.tv/directory/clips", 0, "", "", "", false},
		{"https://www.twitch.tv/fixture%2Fchannel/clips", 0, "", "", "", false},
		{"https://www.twitch.tv/fixture_channel/videos?filter=collections", twitchKindChannelCollections, "fixture_channel", "", "", true},
		{"https://m.twitch.tv/fixture_channel/videos?filter=collections", twitchKindChannelCollections, "fixture_channel", "", "", true},
		{"https://www.twitch.tv/fixture_channel/videos?filter=collections&sort=views", twitchKindChannelCollections, "fixture_channel", "", "", true},
		{"https://www.twitch.tv/fixture_channel/profile?filter=collections", 0, "", "", "", false},
		{"https://www.twitch.tv/fixture_channel/videos/all?filter=collections", 0, "", "", "", false},
		{"https://www.twitch.tv/collections/FixtureCollection01", twitchKindCollection, "FixtureCollection01", "", "", true},
		{"https://www.twitch.tv/collections/FixtureCollection01?feature=share", twitchKindCollection, "FixtureCollection01", "", "", true},
		{"https://go.twitch.tv/collections/FixtureCollection01", twitchKindCollection, "FixtureCollection01", "", "", true},
		{"https://www.twitch.tv/collections/FixtureCollection01/extra", 0, "", "", "", false},
		{"https://www.twitch.tv/collections/bad%2Fid", 0, "", "", "", false},
		{"https://www.twitch.tv/collections/FixtureCollection01#x", 0, "", "", "", false},
		{"https://www.twitch.tv/collections/FixtureCollection01?x=%zz", 0, "", "", "", false},
		{"https://www.twitch.tv/collections/FixtureCollection01?x=%00", 0, "", "", "", false},
		{"https://www.twitch.tv/collections/FixtureCollection01?x=%01share", 0, "", "", "", false},
		{"https://www.twitch.tv/collections/FixtureCollection01?x=%FF", 0, "", "", "", false},
		{"https://www.twitch.tv/collections/FixtureCollection01?x=" + strings.Repeat("a", twitchVideosMaxString+1), 0, "", "", "", false},
		{"https://www.twitch.tv/fixture_channel/videos?filter=collections&feature=share", twitchKindChannelCollections, "fixture_channel", "", "", true},
		{"https://www.twitch.tv/fixture_channel/videos?filter=collections&filter=clips", 0, "", "", "", false},
		{"https://www.twitch.tv/fixture_channel/videos?filter=collections&sort=time&sort=views", 0, "", "", "", false},
		{"https://www.twitch.tv/fixture_channel/videos?filter=collections&sort=%zz", 0, "", "", "", false},
		{"https://www.twitch.tv/fixture_channel/videos?filter=collections&sort=time%00", 0, "", "", "", false},
		{"https://www.twitch.tv/fixture_channel/videos?filter=collections&sort=%FF", 0, "", "", "", false},
		{"https://www.twitch.tv/fixture_channel/videos?filter=%zz", 0, "", "", "", false},
		{"https://www.twitch.tv/fixture_channel/videos#x", 0, "", "", "", false},
		{"https://user@www.twitch.tv/fixture_channel/videos", 0, "", "", "", false},
		{"https://www.twitch.tv:443/fixture_channel/videos", 0, "", "", "", false},
		{"https://evil-twitch.tv/fixture_channel/videos", 0, "", "", "", false},
		{"https://www.twitch.tv/videos/fixture_channel/videos", 0, "", "", "", false},
		{"https://www.twitch.tv/directory/videos", 0, "", "", "", false},
		{"https://www.twitch.tv/fixture_channel/videos/all/extra", 0, "", "", "", false},
		{"https://www.twitch.tv/fixture%2Fchannel/videos", 0, "", "", "", false},
		{"https://www.twitch.tv/fixture_channel/videos?filter=%zz", 0, "", "", "", false},
		{"https://www.twitch.tv/fixture_channel/videos?sort=%zzarchives", 0, "", "", "", false},
		{"https://www.twitch.tv/fixture_channel/videos?filter=archives%00", 0, "", "", "", false},
		{"https://www.twitch.tv/fixture_channel/videos?sort=time%0a", 0, "", "", "", false},
		{"https://www.twitch.tv/fixture_channel/videos?filter=%01archives", 0, "", "", "", false},
		{"https://www.twitch.tv/fixture_channel/videos?filter=%FF", 0, "", "", "", false},
		{"https://www.twitch.tv/fixture_channel/videos?sort=%FF", 0, "", "", "", false},
		{"https://www.twitch.tv/fixture_channel/videos?filter=archives&filter=highlights", 0, "", "", "", false},
		{"https://www.twitch.tv/fixture_channel/videos?sort=time&sort=views", 0, "", "", "", false},
	}
	for _, test := range tests {
		test := test
		t.Run(test.rawURL, func(t *testing.T) {
			parsed, err := url.Parse(test.rawURL)
			if err != nil {
				t.Fatal(err)
			}
			target, ok := classifyTwitchURL(parsed)
			if ok != test.ok {
				t.Fatalf("ok=%t want %t target=%#v", ok, test.ok, target)
			}
			if !test.ok {
				return
			}
			if target.kind != test.kind || target.id != test.id {
				t.Fatalf("target=%#v", target)
			}
			if test.kind == twitchKindVideos {
				if target.videos.broadcastLabel != test.label || target.videos.videoSort != test.sort {
					t.Fatalf("videos query=%#v", target.videos)
				}
			}
			if test.kind == twitchKindChannelClips && strings.Contains(test.rawURL, "range=24hr") {
				if target.clips.filter != "LAST_DAY" || target.clips.label != "Top 24H" {
					t.Fatalf("clips query=%#v", target.clips)
				}
			}
			if test.kind == twitchKindChannelClips && strings.Contains(test.rawURL, "range=all") && !strings.Contains(test.rawURL, "range=24hr") {
				if target.clips.filter != "ALL_TIME" || target.clips.label != "Top All" {
					t.Fatalf("clips query=%#v", target.clips)
				}
			}
		})
	}
}

func TestTwitchVideosPlaylistLazyContinuationReusableAndContract(t *testing.T) {
	transport := &twitchFixtureTransport{videosPages: map[string][]byte{
		"":                   twitchFixture(t, "videos_page1.json"),
		"cursor-page1-final": twitchFixture(t, "videos_page2.json"),
	}}
	result, err := NewTwitch().Extract(context.Background(), Request{
		URL: "https://www.twitch.tv/fixture_channel/videos?filter=archives&sort=views", Transport: transport,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.IsPlaylist() {
		t.Fatal("expected playlist")
	}
	assertTwitchString(t, result, "id", "fixture_channel")
	assertTwitchString(t, result, "title", "fixture_channel - Past Broadcasts sorted by Popular")
	if len(transport.graphQLRequests) != 0 {
		t.Fatal("playlist page fetched eagerly")
	}

	iterator := result.Entries.Iterator()
	first, ok, err := iterator.Next(context.Background())
	if err != nil || !ok {
		t.Fatalf("first entry = %#v ok=%t err=%v", first, ok, err)
	}
	if len(transport.graphQLRequests) != 1 {
		t.Fatalf("first page requests = %d", len(transport.graphQLRequests))
	}
	assertTwitchVideosGraphQLRequest(t, transport.graphQLRequests[0], "fixture_channel", "ARCHIVE", "VIEWS", "")
	if first.ID != "v1000000001" || first.URL != "https://www.twitch.tv/videos/1000000001" || !first.Transparent || first.ExtractorKey != "twitch" {
		t.Fatalf("first entry = %#v", first)
	}
	second, ok, err := iterator.Next(context.Background())
	if err != nil || !ok || second.ID != "v1000000002" {
		t.Fatalf("second entry = %#v ok=%t err=%v", second, ok, err)
	}
	if len(transport.graphQLRequests) != 1 {
		t.Fatal("continuation over-fetched before page exhaustion")
	}
	third, ok, err := iterator.Next(context.Background())
	if err != nil || !ok || third.ID != "v1000000003" {
		t.Fatalf("third entry = %#v ok=%t err=%v", third, ok, err)
	}
	if len(transport.graphQLRequests) != 2 {
		t.Fatalf("continuation requests = %d", len(transport.graphQLRequests))
	}
	assertTwitchVideosGraphQLRequest(t, transport.graphQLRequests[1], "fixture_channel", "ARCHIVE", "VIEWS", "cursor-page1-final")
	if _, ok, err := iterator.Next(context.Background()); err != nil || ok {
		t.Fatalf("expected end, ok=%t err=%v", ok, err)
	}

	again, err := CollectEntries(context.Background(), result.Entries, 10)
	if err != nil || len(again) != 3 || again[0].ID != "v1000000001" || again[2].ID != "v1000000003" {
		t.Fatalf("reusable iteration = %#v err=%v", again, err)
	}

	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			entries, collectErr := CollectEntries(context.Background(), result.Entries, 10)
			if collectErr != nil {
				errs <- collectErr
				return
			}
			if len(entries) != 3 || entries[1].ID != "v1000000002" {
				errs <- fmt.Errorf("concurrent entries = %#v", entries)
				return
			}
			errs <- nil
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
}

func TestTwitchVideosPlaylistFailuresCancellationAndBounds(t *testing.T) {
	t.Run("empty channel", func(t *testing.T) {
		transport := &twitchFixtureTransport{graphQLFixtures: []twitchGraphQLFixture{{body: twitchFixture(t, "videos_empty.json")}}}
		result, err := NewTwitch().Extract(context.Background(), Request{
			URL: "https://www.twitch.tv/fixture_channel/videos", Transport: transport,
		})
		if err != nil {
			t.Fatal(err)
		}
		entries, err := CollectEntries(context.Background(), result.Entries, 10)
		if err != nil || len(entries) != 0 {
			t.Fatalf("empty = %#v err=%v", entries, err)
		}
	})
	t.Run("absent channel", func(t *testing.T) {
		transport := &twitchFixtureTransport{graphQLFixtures: []twitchGraphQLFixture{{body: twitchFixture(t, "videos_not_found.json")}}}
		result, err := NewTwitch().Extract(context.Background(), Request{
			URL: "https://www.twitch.tv/missing_channel/videos", Transport: transport,
		})
		if err != nil {
			t.Fatal(err)
		}
		_, _, err = result.Entries.Iterator().Next(context.Background())
		if !errors.Is(err, ErrUnavailable) {
			t.Fatalf("absent channel err=%v", err)
		}
	})
	t.Run("malformed envelope", func(t *testing.T) {
		transport := &twitchFixtureTransport{graphQLFixtures: []twitchGraphQLFixture{{body: twitchFixture(t, "videos_malformed.json")}}}
		result, err := NewTwitch().Extract(context.Background(), Request{
			URL: "https://www.twitch.tv/fixture_channel/videos", Transport: transport,
		})
		if err != nil {
			t.Fatal(err)
		}
		_, _, err = result.Entries.Iterator().Next(context.Background())
		if !errors.Is(err, ErrInvalidMetadata) {
			t.Fatalf("malformed err=%v", err)
		}
	})
	t.Run("http categories", func(t *testing.T) {
		cases := []struct {
			status int
			want   error
		}{
			{http.StatusUnauthorized, ErrAuthentication},
			{http.StatusForbidden, ErrAuthentication},
			{http.StatusNotFound, ErrUnavailable},
			{http.StatusGone, ErrUnavailable},
			{http.StatusTooManyRequests, ErrTwitchRateLimited},
			{http.StatusBadGateway, ErrTwitchNetwork},
		}
		for _, test := range cases {
			transport := &twitchFixtureTransport{graphQLFixtures: []twitchGraphQLFixture{{status: test.status, body: []byte(`[]`)}}}
			result, err := NewTwitch().Extract(context.Background(), Request{
				URL: "https://www.twitch.tv/fixture_channel/videos", Transport: transport,
			})
			if err != nil {
				t.Fatal(err)
			}
			_, _, err = result.Entries.Iterator().Next(context.Background())
			if !errors.Is(err, test.want) {
				t.Fatalf("status %d err=%v want %v", test.status, err, test.want)
			}
		}
	})
	t.Run("secret safe errors", func(t *testing.T) {
		const secret = "fixture-videos-secret-token"
		transport := &twitchFixtureTransport{graphQLFixtures: []twitchGraphQLFixture{
			{body: []byte(`[{"data":{"user":{"id":"1","videos":{"edges":[]}}},"errors":[{"message":"` + secret + `"}]}]`)},
		}}
		result, err := NewTwitch().Extract(context.Background(), Request{
			URL: "https://www.twitch.tv/fixture_channel/videos", Transport: transport,
		})
		if err != nil {
			t.Fatal(err)
		}
		_, _, err = result.Entries.Iterator().Next(context.Background())
		if !errors.Is(err, ErrInvalidMetadata) {
			t.Fatalf("err=%v", err)
		}
		if strings.Contains(err.Error(), secret) {
			t.Fatalf("error exposed secret: %v", err)
		}
	})
	t.Run("cancellation before initial fetch", func(t *testing.T) {
		transport := &twitchFixtureTransport{graphQLFixtures: []twitchGraphQLFixture{{body: twitchFixture(t, "videos_page1.json")}}}
		result, err := NewTwitch().Extract(context.Background(), Request{
			URL: "https://www.twitch.tv/fixture_channel/videos", Transport: transport,
		})
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, _, err = result.Entries.Iterator().Next(ctx)
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err=%v", err)
		}
		if len(transport.graphQLRequests) != 0 {
			t.Fatal("canceled iterator still fetched")
		}
	})
	t.Run("cancellation during continuation", func(t *testing.T) {
		transport := &twitchFixtureTransport{graphQLFixtures: []twitchGraphQLFixture{
			{body: twitchFixture(t, "videos_page1.json")},
			{body: twitchFixture(t, "videos_page2.json")},
		}}
		result, err := NewTwitch().Extract(context.Background(), Request{
			URL: "https://www.twitch.tv/fixture_channel/videos", Transport: transport,
		})
		if err != nil {
			t.Fatal(err)
		}
		iterator := result.Entries.Iterator()
		if _, _, err := iterator.Next(context.Background()); err != nil {
			t.Fatal(err)
		}
		if _, _, err := iterator.Next(context.Background()); err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, _, err = iterator.Next(ctx)
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("continuation cancel err=%v", err)
		}
		if len(transport.graphQLRequests) != 1 {
			t.Fatalf("requests after cancel = %d", len(transport.graphQLRequests))
		}
	})
	t.Run("repeated cursor stops", func(t *testing.T) {
		loop := []byte(`[{"data":{"user":{"id":"9001","videos":{"edges":[{"__typename":"VideoEdge","cursor":"loop","node":{"__typename":"Video","id":"1000000001","title":"Loop"}}]}}}}]`)
		transport := &twitchFixtureTransport{graphQLFixtures: []twitchGraphQLFixture{{body: loop}, {body: loop}, {body: loop}}}
		result, err := NewTwitch().Extract(context.Background(), Request{
			URL: "https://www.twitch.tv/fixture_channel/videos", Transport: transport,
		})
		if err != nil {
			t.Fatal(err)
		}
		entries, err := CollectEntries(context.Background(), result.Entries, 100)
		if err != nil {
			t.Fatal(err)
		}
		if len(entries) != 2 {
			t.Fatalf("repeated cursor entries = %d", len(entries))
		}
		if len(transport.graphQLRequests) != 2 {
			t.Fatalf("repeated cursor requests = %d", len(transport.graphQLRequests))
		}
	})
	t.Run("no over-fetch at bound", func(t *testing.T) {
		transport := &twitchFixtureTransport{graphQLFixtures: []twitchGraphQLFixture{
			{body: twitchFixture(t, "videos_page1.json")},
			{body: twitchFixture(t, "videos_page2.json")},
		}}
		result, err := NewTwitch().Extract(context.Background(), Request{
			URL: "https://www.twitch.tv/fixture_channel/videos", Transport: transport,
		})
		if err != nil {
			t.Fatal(err)
		}
		iterator := result.Entries.Iterator()
		for i := 0; i < 2; i++ {
			if _, ok, nextErr := iterator.Next(context.Background()); nextErr != nil || !ok {
				t.Fatalf("entry %d ok=%t err=%v", i, ok, nextErr)
			}
		}
		if len(transport.graphQLRequests) != 1 {
			t.Fatalf("over-fetched: %d", len(transport.graphQLRequests))
		}
	})
	t.Run("default all sends null broadcastType", func(t *testing.T) {
		transport := &twitchFixtureTransport{graphQLFixtures: []twitchGraphQLFixture{{body: twitchFixture(t, "videos_empty.json")}}}
		result, err := NewTwitch().Extract(context.Background(), Request{
			URL: "https://www.twitch.tv/fixture_channel/profile", Transport: transport,
		})
		if err != nil {
			t.Fatal(err)
		}
		assertTwitchString(t, result, "title", "fixture_channel - All Videos sorted by Date")
		if _, _, err := result.Entries.Iterator().Next(context.Background()); err != nil {
			t.Fatal(err)
		}
		assertTwitchVideosGraphQLRequest(t, transport.graphQLRequests[0], "fixture_channel", "", "TIME", "")
	})
	t.Run("blank filter and sort query defaults", func(t *testing.T) {
		cases := []struct {
			rawURL string
		}{
			{"https://www.twitch.tv/fixture_channel/videos?filter="},
			{"https://www.twitch.tv/fixture_channel/videos?sort="},
			{"https://www.twitch.tv/fixture_channel/videos?filter=&sort="},
		}
		for _, test := range cases {
			transport := &twitchFixtureTransport{graphQLFixtures: []twitchGraphQLFixture{{body: twitchFixture(t, "videos_empty.json")}}}
			result, err := NewTwitch().Extract(context.Background(), Request{URL: test.rawURL, Transport: transport})
			if err != nil {
				t.Fatalf("%s: %v", test.rawURL, err)
			}
			assertTwitchString(t, result, "title", "fixture_channel - All Videos sorted by Date")
			if _, _, err := result.Entries.Iterator().Next(context.Background()); err != nil {
				t.Fatalf("%s: %v", test.rawURL, err)
			}
			assertTwitchVideosGraphQLRequest(t, transport.graphQLRequests[0], "fixture_channel", "", "TIME", "")
		}
	})
	t.Run("missing continuation cursor stops", func(t *testing.T) {
		page := []byte(`[{"data":{"user":{"id":"9001","videos":{"edges":[{"__typename":"VideoEdge","node":{"__typename":"Video","id":"1000000001","title":"Only"}}]}}}}]`)
		transport := &twitchFixtureTransport{graphQLFixtures: []twitchGraphQLFixture{{body: page}, {body: twitchFixture(t, "videos_page2.json")}}}
		result, err := NewTwitch().Extract(context.Background(), Request{
			URL: "https://www.twitch.tv/fixture_channel/videos", Transport: transport,
		})
		if err != nil {
			t.Fatal(err)
		}
		entries, err := CollectEntries(context.Background(), result.Entries, 10)
		if err != nil || len(entries) != 1 {
			t.Fatalf("entries=%#v err=%v", entries, err)
		}
		if len(transport.graphQLRequests) != 1 {
			t.Fatalf("missing cursor still continued: %d", len(transport.graphQLRequests))
		}
	})
}

func TestTwitchVideosPageCursorSemantics(t *testing.T) {
	t.Parallel()
	oversized := strings.Repeat("c", twitchVideosMaxCursor+1)
	mustParse := func(raw string) []twitchVideosPageResponse {
		t.Helper()
		var response []twitchVideosPageResponse
		if err := json.Unmarshal([]byte(raw), &response); err != nil {
			t.Fatal(err)
		}
		return response
	}

	entries, cursor, err := parseTwitchVideosPage(mustParse(`[{"data":{"user":{"id":"9001","videos":{"edges":[
			{"__typename":"VideoEdge","cursor":"keep-me","node":{"__typename":"Video","id":"1000000001","title":"First"}},
			{"__typename":"NotVideoEdge","cursor":"skip-edge","node":{"__typename":"Video","id":"1000000099","title":"Bad edge"}},
			{"__typename":"VideoEdge","cursor":"skip-node","node":{"__typename":"Clip","id":"1000000002","title":"Bad node"}},
			{"__typename":"VideoEdge","cursor":"skip-id","node":{"__typename":"Video","id":"not-numeric","title":"Hostile"}},
			{"__typename":"VideoEdge","cursor":"","node":{"__typename":"Video","id":"1000000002","title":"Empty cursor terminates"}}
		]}}}}]`))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 || entries[0].ID != "v1000000001" || entries[1].ID != "v1000000002" {
		t.Fatalf("entries = %#v", entries)
	}
	if cursor != "" {
		t.Fatalf("empty later cursor should terminate, got %q", cursor)
	}

	entries, cursor, err = parseTwitchVideosPage(mustParse(`[{"data":{"user":{"id":"9001","videos":{"edges":[
			{"__typename":"VideoEdge","cursor":"keep-me","node":{"__typename":"Video","id":"1000000001","title":"First"}},
			{"__typename":"NotVideoEdge","cursor":"noise","node":{"__typename":"Video","id":"1000000099","title":"Noise"}},
			"not-an-object"
		]}}}}]`))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].ID != "v1000000001" || cursor != "keep-me" {
		t.Fatalf("skipped edges disturbed cursor: entries=%#v cursor=%q", entries, cursor)
	}

	entries, cursor, err = parseTwitchVideosPage(mustParse(`[{"data":{"user":{"id":"9001","videos":{"edges":[
			{"__typename":"VideoEdge","cursor":"keep-me","node":{"__typename":"Video","id":"1000000001","title":"First"}},
			{"__typename":"VideoEdge","cursor":` + strconv.Quote(oversized) + `,"node":{"__typename":"Video","id":"1000000002","title":"Oversized cursor terminates"}}
		]}}}}]`))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 || cursor != "" {
		t.Fatalf("oversized later cursor should terminate: entries=%#v cursor=%q", entries, cursor)
	}
}

func assertTwitchVideosGraphQLRequest(t *testing.T, request twitchRecordedRequest, channel, broadcastType, videoSort, cursor string) {
	t.Helper()
	if got := request.header.Get("Client-ID"); got != twitchClientID {
		t.Fatalf("Client-ID = %q", got)
	}
	if got := request.header.Get("Content-Type"); got != "text/plain;charset=UTF-8" {
		t.Fatalf("Content-Type = %q", got)
	}
	var operations []struct {
		OperationName string `json:"operationName"`
		Variables     struct {
			ChannelOwnerLogin string  `json:"channelOwnerLogin"`
			BroadcastType     *string `json:"broadcastType"`
			VideoSort         string  `json:"videoSort"`
			Limit             int     `json:"limit"`
			Cursor            string  `json:"cursor"`
		} `json:"variables"`
		Extensions struct {
			PersistedQuery struct {
				Version    int    `json:"version"`
				SHA256Hash string `json:"sha256Hash"`
			} `json:"persistedQuery"`
		} `json:"extensions"`
	}
	if err := json.Unmarshal(request.body, &operations); err != nil {
		t.Fatal(err)
	}
	if len(operations) != 1 {
		t.Fatalf("operations = %#v", operations)
	}
	operation := operations[0]
	if operation.OperationName != twitchVideosOperation ||
		operation.Extensions.PersistedQuery.Version != 1 ||
		operation.Extensions.PersistedQuery.SHA256Hash != twitchOperationHashes[twitchVideosOperation] {
		t.Fatalf("operation = %#v", operation)
	}
	if operation.Variables.ChannelOwnerLogin != channel || operation.Variables.VideoSort != videoSort || operation.Variables.Limit != twitchVideosPageLimit {
		t.Fatalf("variables = %#v", operation.Variables)
	}
	if broadcastType == "" {
		if operation.Variables.BroadcastType != nil {
			t.Fatalf("broadcastType = %#v", operation.Variables.BroadcastType)
		}
	} else if operation.Variables.BroadcastType == nil || *operation.Variables.BroadcastType != broadcastType {
		t.Fatalf("broadcastType = %#v", operation.Variables.BroadcastType)
	}
	var raw []map[string]any
	if err := json.Unmarshal(request.body, &raw); err != nil {
		t.Fatal(err)
	}
	variables := raw[0]["variables"].(map[string]any)
	if cursor == "" {
		if _, present := variables["cursor"]; present {
			t.Fatalf("unexpected cursor in %#v", variables)
		}
	} else if got, _ := variables["cursor"].(string); got != cursor {
		t.Fatalf("cursor = %#v", variables["cursor"])
	}
}

func TestTwitchDirectCollectionSuccessContractAndSkipping(t *testing.T) {
	transport := &twitchFixtureTransport{graphQLFixtures: []twitchGraphQLFixture{{body: twitchFixture(t, "collection_direct.json")}}}
	result, err := NewTwitch().Extract(context.Background(), Request{
		URL: "https://www.twitch.tv/collections/FixtureCollection01", Transport: transport,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.IsPlaylist() {
		t.Fatal("expected playlist")
	}
	assertTwitchString(t, result, "id", "FixtureCollection01")
	assertTwitchString(t, result, "title", "Synthetic Playthrough Archives")
	entries, err := CollectEntries(context.Background(), result.Entries, 10)
	if err != nil || len(entries) != 2 {
		t.Fatalf("entries=%#v err=%v", entries, err)
	}
	if entries[0].ID != "v1000000001" || entries[0].URL != "https://www.twitch.tv/videos/1000000001" || !entries[0].Transparent || entries[0].ExtractorKey != "twitch" {
		t.Fatalf("first entry=%#v", entries[0])
	}
	if entries[1].ID != "v1000000002" {
		t.Fatalf("second entry=%#v", entries[1])
	}
	assertTwitchCollectionGraphQLRequest(t, transport.graphQLRequests[0], "FixtureCollection01")
}

func TestTwitchDirectCollectionVideoNodeTypenameSemantics(t *testing.T) {
	t.Parallel()
	mustEntry := func(raw string) Entry {
		t.Helper()
		entry, ok := twitchCollectionVideoEntry(json.RawMessage(raw))
		if !ok {
			t.Fatalf("expected valid entry for %s", raw)
		}
		return entry
	}
	if entry := mustEntry(`{"id":"1000000001","title":"No typename"}`); entry.ID != "v1000000001" {
		t.Fatalf("entry=%#v", entry)
	}
	if entry := mustEntry(`{"__typename":"Video","id":"1000000002","title":"Explicit video"}`); entry.ID != "v1000000002" {
		t.Fatalf("entry=%#v", entry)
	}
	for _, raw := range []string{
		`{"__typename":"Clip","id":"1000000003","title":"Conflicting typename"}`,
		`{"__typename":"Video","id":"not-numeric","title":"Hostile id"}`,
		`{"id":"","title":"Missing id"}`,
	} {
		if _, ok := twitchCollectionVideoEntry(json.RawMessage(raw)); ok {
			t.Fatalf("accepted hostile node %s", raw)
		}
	}
}

func TestTwitchCollectionRequestMarshalingValidatesIDs(t *testing.T) {
	t.Parallel()
	if _, err := marshalTwitchCollectionRequest(""); !errors.Is(err, ErrInvalidMetadata) {
		t.Fatalf("empty id err=%v", err)
	}
	if _, err := marshalTwitchCollectionRequest("bad/id"); !errors.Is(err, ErrInvalidMetadata) {
		t.Fatalf("hostile id err=%v", err)
	}
	if _, err := marshalTwitchChannelCollectionsRequest("bad/channel", ""); !errors.Is(err, ErrInvalidMetadata) {
		t.Fatalf("hostile channel err=%v", err)
	}
	if body, err := marshalTwitchCollectionRequest("FixtureCollection01"); err != nil || len(body) == 0 {
		t.Fatalf("valid id err=%v body=%d", err, len(body))
	}
}

func TestTwitchBoundedJSONArrayPolicy(t *testing.T) {
	t.Parallel()

	buildEdges := func(count int, pad int) json.RawMessage {
		t.Helper()
		var builder strings.Builder
		builder.WriteString("[")
		for i := 0; i < count; i++ {
			if i > 0 {
				builder.WriteByte(',')
			}
			builder.WriteString(`{"__typename":"CollectionsItemEdge","cursor":"cursor-`)
			builder.WriteString(strconv.Itoa(i))
			builder.WriteString(`","node":{"__typename":"Collection","id":"FixtureCollection`)
			builder.WriteString(strconv.Itoa(i))
			builder.WriteString(`","title":"Synthetic collection","padding":"`)
			builder.WriteString(strings.Repeat("x", pad))
			builder.WriteString(`"}}`)
		}
		builder.WriteString("]")
		return json.RawMessage(builder.String())
	}

	hundred := buildEdges(twitchCollectionsMaxEdges, 120)
	if len(hundred) <= twitchMaxURL {
		t.Fatalf("fixture size = %d, want > %d", len(hundred), twitchMaxURL)
	}
	if len(hundred) > twitchCollectionsMaxEdgeArrayBytes {
		t.Fatalf("fixture size = %d, want <= %d", len(hundred), twitchCollectionsMaxEdgeArrayBytes)
	}
	edges, err := twitchBoundedJSONArray(hundred, twitchCollectionsMaxEdges)
	if err != nil {
		t.Fatalf("hundred-edge page err=%v", err)
	}
	if len(edges) != twitchCollectionsMaxEdges {
		t.Fatalf("edges = %d", len(edges))
	}

	entries, _, err := parseTwitchChannelCollectionsPage([]twitchChannelCollectionsPageResponse{{
		Data: struct {
			User *twitchChannelCollectionsUser `json:"user"`
		}{User: &twitchChannelCollectionsUser{
			ID: json.RawMessage(`"9001"`),
			Collections: &struct {
				Edges json.RawMessage `json:"edges"`
			}{Edges: hundred},
		}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != twitchCollectionsMaxEdges {
		t.Fatalf("parsed entries = %d", len(entries))
	}

	oversizedCount := buildEdges(twitchCollectionsMaxEdges+1, 0)
	_, err = twitchBoundedJSONArray(oversizedCount, twitchCollectionsMaxEdges)
	if !errors.Is(err, ErrInvalidPlaylist) {
		t.Fatalf("101-edge err=%v", err)
	}

	oversizedPayload := buildEdges(twitchCollectionsMaxEdges, twitchMaxURL)
	if len(oversizedPayload) <= twitchCollectionsMaxEdgeArrayBytes {
		t.Fatalf("payload size = %d, want > %d", len(oversizedPayload), twitchCollectionsMaxEdgeArrayBytes)
	}
	_, err = twitchBoundedJSONArray(oversizedPayload, twitchCollectionsMaxEdges)
	if !errors.Is(err, ErrInvalidPlaylist) {
		t.Fatalf("oversized payload err=%v", err)
	}

	skipsOversizedEdge := json.RawMessage(`[{"__typename":"CollectionsItemEdge","cursor":"keep","node":{"__typename":"Collection","id":"FixtureCollection01","title":"Valid"}},{"__typename":"CollectionsItemEdge","cursor":"skip","padding":"` + strings.Repeat("x", twitchMaxURL) + `"}]`)
	entries, _, err = parseTwitchChannelCollectionsPage([]twitchChannelCollectionsPageResponse{{
		Data: struct {
			User *twitchChannelCollectionsUser `json:"user"`
		}{User: &twitchChannelCollectionsUser{
			ID: json.RawMessage(`"9001"`),
			Collections: &struct {
				Edges json.RawMessage `json:"edges"`
			}{Edges: skipsOversizedEdge},
		}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].ID != "FixtureCollection01" {
		t.Fatalf("entries=%#v", entries)
	}
}

func TestTwitchDirectCollectionEmptyUnavailableMalformed(t *testing.T) {
	t.Run("empty", func(t *testing.T) {
		transport := &twitchFixtureTransport{graphQLFixtures: []twitchGraphQLFixture{{body: twitchFixture(t, "collection_direct_empty.json")}}}
		result, err := NewTwitch().Extract(context.Background(), Request{
			URL: "https://m.twitch.tv/collections/FixtureCollectionEmpty", Transport: transport,
		})
		if err != nil {
			t.Fatal(err)
		}
		entries, err := CollectEntries(context.Background(), result.Entries, 10)
		if err != nil || len(entries) != 0 {
			t.Fatalf("entries=%#v err=%v", entries, err)
		}
		assertTwitchString(t, result, "title", "Synthetic Empty Collection")
	})
	t.Run("unavailable", func(t *testing.T) {
		transport := &twitchFixtureTransport{graphQLFixtures: []twitchGraphQLFixture{{body: twitchFixture(t, "collection_direct_unavailable.json")}}}
		_, err := NewTwitch().Extract(context.Background(), Request{
			URL: "https://go.twitch.tv/collections/MissingCollection", Transport: transport,
		})
		if !errors.Is(err, ErrUnavailable) {
			t.Fatalf("err=%v", err)
		}
	})
	t.Run("malformed", func(t *testing.T) {
		transport := &twitchFixtureTransport{graphQLFixtures: []twitchGraphQLFixture{{body: twitchFixture(t, "collection_direct_malformed.json")}}}
		_, err := NewTwitch().Extract(context.Background(), Request{
			URL: "https://twitch.tv/collections/FixtureCollection01", Transport: transport,
		})
		if !errors.Is(err, ErrInvalidMetadata) {
			t.Fatalf("err=%v", err)
		}
	})
}

func TestTwitchDirectCollectionFailuresCancellationAndBounds(t *testing.T) {
	t.Run("http categories", func(t *testing.T) {
		cases := []struct {
			status int
			want   error
		}{
			{http.StatusUnauthorized, ErrAuthentication},
			{http.StatusForbidden, ErrAuthentication},
			{http.StatusNotFound, ErrUnavailable},
			{http.StatusGone, ErrUnavailable},
			{http.StatusTooManyRequests, ErrTwitchRateLimited},
			{http.StatusBadGateway, ErrTwitchNetwork},
		}
		for _, test := range cases {
			transport := &twitchFixtureTransport{graphQLFixtures: []twitchGraphQLFixture{{status: test.status, body: []byte(`[]`)}}}
			_, err := NewTwitch().Extract(context.Background(), Request{
				URL: "https://www.twitch.tv/collections/FixtureCollection01", Transport: transport,
			})
			if !errors.Is(err, test.want) {
				t.Fatalf("status %d err=%v want %v", test.status, err, test.want)
			}
		}
	})
	t.Run("secret safe errors", func(t *testing.T) {
		const secret = "fixture-collection-direct-secret-token"
		transport := &twitchFixtureTransport{graphQLFixtures: []twitchGraphQLFixture{
			{body: []byte(`[{"data":{"collection":{"title":"Synthetic","items":{"edges":[]}}},"errors":[{"message":"` + secret + `"}]}]`)},
		}}
		_, err := NewTwitch().Extract(context.Background(), Request{
			URL: "https://www.twitch.tv/collections/FixtureCollection01", Transport: transport,
		})
		if !errors.Is(err, ErrInvalidMetadata) {
			t.Fatalf("err=%v", err)
		}
		if strings.Contains(err.Error(), secret) {
			t.Fatalf("error exposed secret: %v", err)
		}
	})
	t.Run("cancellation before fetch", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		transport := &twitchFixtureTransport{graphQLFixtures: []twitchGraphQLFixture{{body: twitchFixture(t, "collection_direct.json")}}}
		_, err := NewTwitch().Extract(ctx, Request{
			URL: "https://www.twitch.tv/collections/FixtureCollection01", Transport: transport,
		})
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err=%v", err)
		}
		if len(transport.graphQLRequests) != 0 {
			t.Fatal("canceled extract still fetched")
		}
	})
	t.Run("edge bound", func(t *testing.T) {
		var builder strings.Builder
		builder.WriteString(`[`)
		for i := 0; i < twitchCollectionsMaxEdges+1; i++ {
			if i > 0 {
				builder.WriteByte(',')
			}
			builder.WriteString(`{"node":{"__typename":"Video","id":"`)
			builder.WriteString(strconv.Itoa(1000000000 + i))
			builder.WriteString(`","title":"VOD"}}`)
		}
		builder.WriteString(`]`)
		_, _, err := parseTwitchCollection([]twitchCollectionResponse{{
			Data: struct {
				Collection *twitchCollectionData `json:"collection"`
			}{Collection: &twitchCollectionData{
				Title: "Oversized",
				Items: struct {
					Edges json.RawMessage `json:"edges"`
				}{Edges: json.RawMessage(builder.String())},
			}},
		}})
		if !errors.Is(err, ErrInvalidPlaylist) {
			t.Fatalf("edge-bound err=%v", err)
		}
	})
}

func TestTwitchChannelCollectionsLazyContinuationReusableAndContract(t *testing.T) {
	transport := &twitchFixtureTransport{collectionsPages: map[string][]byte{
		"":                               twitchFixture(t, "collections_page1.json"),
		"cursor-collections-page1-final": twitchFixture(t, "collections_page2.json"),
	}}
	result, err := NewTwitch().Extract(context.Background(), Request{
		URL: "https://www.twitch.tv/fixture_channel/videos?filter=collections", Transport: transport,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.IsPlaylist() {
		t.Fatal("expected playlist")
	}
	assertTwitchString(t, result, "id", "fixture_channel")
	assertTwitchString(t, result, "title", "fixture_channel - Collections")
	if len(transport.graphQLRequests) != 0 {
		t.Fatal("playlist page fetched eagerly")
	}

	iterator := result.Entries.Iterator()
	first, ok, err := iterator.Next(context.Background())
	if err != nil || !ok {
		t.Fatalf("first entry = %#v ok=%t err=%v", first, ok, err)
	}
	if len(transport.graphQLRequests) != 1 {
		t.Fatalf("first page requests = %d", len(transport.graphQLRequests))
	}
	assertTwitchChannelCollectionsGraphQLRequest(t, transport.graphQLRequests[0], "fixture_channel", "")
	if first.ID != "FixtureCollection01" || first.URL != "https://www.twitch.tv/collections/FixtureCollection01" || !first.Transparent || first.ExtractorKey != "twitch" {
		t.Fatalf("first entry = %#v", first)
	}
	second, ok, err := iterator.Next(context.Background())
	if err != nil || !ok || second.ID != "FixtureCollection02" {
		t.Fatalf("second entry = %#v ok=%t err=%v", second, ok, err)
	}
	if len(transport.graphQLRequests) != 1 {
		t.Fatal("continuation over-fetched before page exhaustion")
	}
	third, ok, err := iterator.Next(context.Background())
	if err != nil || !ok || third.ID != "FixtureCollection03" {
		t.Fatalf("third entry = %#v ok=%t err=%v", third, ok, err)
	}
	if len(transport.graphQLRequests) != 2 {
		t.Fatalf("continuation requests = %d", len(transport.graphQLRequests))
	}
	assertTwitchChannelCollectionsGraphQLRequest(t, transport.graphQLRequests[1], "fixture_channel", "cursor-collections-page1-final")
	if _, ok, err := iterator.Next(context.Background()); err != nil || ok {
		t.Fatalf("expected end, ok=%t err=%v", ok, err)
	}

	again, err := CollectEntries(context.Background(), result.Entries, 10)
	if err != nil || len(again) != 3 || again[0].ID != "FixtureCollection01" || again[2].ID != "FixtureCollection03" {
		t.Fatalf("reusable iteration = %#v err=%v", again, err)
	}

	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			entries, collectErr := CollectEntries(context.Background(), result.Entries, 10)
			if collectErr != nil {
				errs <- collectErr
				return
			}
			if len(entries) != 3 || entries[1].ID != "FixtureCollection02" {
				errs <- fmt.Errorf("concurrent entries = %#v", entries)
				return
			}
			errs <- nil
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
}

func TestTwitchChannelCollectionsFailuresCancellationAndBounds(t *testing.T) {
	t.Run("empty channel", func(t *testing.T) {
		transport := &twitchFixtureTransport{graphQLFixtures: []twitchGraphQLFixture{{body: twitchFixture(t, "collections_empty.json")}}}
		result, err := NewTwitch().Extract(context.Background(), Request{
			URL: "https://www.twitch.tv/fixture_channel/videos?filter=collections", Transport: transport,
		})
		if err != nil {
			t.Fatal(err)
		}
		entries, err := CollectEntries(context.Background(), result.Entries, 10)
		if err != nil || len(entries) != 0 {
			t.Fatalf("empty = %#v err=%v", entries, err)
		}
	})
	t.Run("absent channel", func(t *testing.T) {
		transport := &twitchFixtureTransport{graphQLFixtures: []twitchGraphQLFixture{{body: twitchFixture(t, "collections_not_found.json")}}}
		result, err := NewTwitch().Extract(context.Background(), Request{
			URL: "https://www.twitch.tv/missing_channel/videos?filter=collections", Transport: transport,
		})
		if err != nil {
			t.Fatal(err)
		}
		_, _, err = result.Entries.Iterator().Next(context.Background())
		if !errors.Is(err, ErrUnavailable) {
			t.Fatalf("absent channel err=%v", err)
		}
	})
	t.Run("malformed envelope", func(t *testing.T) {
		transport := &twitchFixtureTransport{graphQLFixtures: []twitchGraphQLFixture{{body: twitchFixture(t, "collections_malformed.json")}}}
		result, err := NewTwitch().Extract(context.Background(), Request{
			URL: "https://www.twitch.tv/fixture_channel/videos?filter=collections", Transport: transport,
		})
		if err != nil {
			t.Fatal(err)
		}
		_, _, err = result.Entries.Iterator().Next(context.Background())
		if !errors.Is(err, ErrInvalidMetadata) {
			t.Fatalf("malformed err=%v", err)
		}
	})
	t.Run("http categories", func(t *testing.T) {
		cases := []struct {
			status int
			want   error
		}{
			{http.StatusUnauthorized, ErrAuthentication},
			{http.StatusForbidden, ErrAuthentication},
			{http.StatusNotFound, ErrUnavailable},
			{http.StatusGone, ErrUnavailable},
			{http.StatusTooManyRequests, ErrTwitchRateLimited},
			{http.StatusBadGateway, ErrTwitchNetwork},
		}
		for _, test := range cases {
			transport := &twitchFixtureTransport{graphQLFixtures: []twitchGraphQLFixture{{status: test.status, body: []byte(`[]`)}}}
			result, err := NewTwitch().Extract(context.Background(), Request{
				URL: "https://www.twitch.tv/fixture_channel/videos?filter=collections", Transport: transport,
			})
			if err != nil {
				t.Fatal(err)
			}
			_, _, err = result.Entries.Iterator().Next(context.Background())
			if !errors.Is(err, test.want) {
				t.Fatalf("status %d err=%v want %v", test.status, err, test.want)
			}
		}
	})
	t.Run("secret safe errors", func(t *testing.T) {
		const secret = "fixture-collections-secret-token"
		transport := &twitchFixtureTransport{graphQLFixtures: []twitchGraphQLFixture{
			{body: []byte(`[{"data":{"user":{"id":"1","collections":{"edges":[]}}},"errors":[{"message":"` + secret + `"}]}]`)},
		}}
		result, err := NewTwitch().Extract(context.Background(), Request{
			URL: "https://www.twitch.tv/fixture_channel/videos?filter=collections", Transport: transport,
		})
		if err != nil {
			t.Fatal(err)
		}
		_, _, err = result.Entries.Iterator().Next(context.Background())
		if !errors.Is(err, ErrInvalidMetadata) {
			t.Fatalf("err=%v", err)
		}
		if strings.Contains(err.Error(), secret) {
			t.Fatalf("error exposed secret: %v", err)
		}
	})
	t.Run("cancellation before initial fetch", func(t *testing.T) {
		transport := &twitchFixtureTransport{graphQLFixtures: []twitchGraphQLFixture{{body: twitchFixture(t, "collections_page1.json")}}}
		result, err := NewTwitch().Extract(context.Background(), Request{
			URL: "https://www.twitch.tv/fixture_channel/videos?filter=collections", Transport: transport,
		})
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, _, err = result.Entries.Iterator().Next(ctx)
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err=%v", err)
		}
		if len(transport.graphQLRequests) != 0 {
			t.Fatal("canceled iterator still fetched")
		}
	})
	t.Run("cancellation during continuation", func(t *testing.T) {
		transport := &twitchFixtureTransport{graphQLFixtures: []twitchGraphQLFixture{
			{body: twitchFixture(t, "collections_page1.json")},
			{body: twitchFixture(t, "collections_page2.json")},
		}}
		result, err := NewTwitch().Extract(context.Background(), Request{
			URL: "https://www.twitch.tv/fixture_channel/videos?filter=collections", Transport: transport,
		})
		if err != nil {
			t.Fatal(err)
		}
		iterator := result.Entries.Iterator()
		if _, _, err := iterator.Next(context.Background()); err != nil {
			t.Fatal(err)
		}
		if _, _, err := iterator.Next(context.Background()); err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, _, err = iterator.Next(ctx)
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("continuation cancel err=%v", err)
		}
		if len(transport.graphQLRequests) != 1 {
			t.Fatalf("requests after cancel = %d", len(transport.graphQLRequests))
		}
	})
	t.Run("repeated cursor stops", func(t *testing.T) {
		loop := []byte(`[{"data":{"user":{"id":"9001","collections":{"edges":[{"__typename":"CollectionsItemEdge","cursor":"loop","node":{"__typename":"Collection","id":"FixtureCollection01","title":"Loop"}}]}}}}]`)
		transport := &twitchFixtureTransport{graphQLFixtures: []twitchGraphQLFixture{{body: loop}, {body: loop}, {body: loop}}}
		result, err := NewTwitch().Extract(context.Background(), Request{
			URL: "https://www.twitch.tv/fixture_channel/videos?filter=collections", Transport: transport,
		})
		if err != nil {
			t.Fatal(err)
		}
		entries, err := CollectEntries(context.Background(), result.Entries, 100)
		if err != nil {
			t.Fatal(err)
		}
		if len(entries) != 2 {
			t.Fatalf("repeated cursor entries = %d", len(entries))
		}
		if len(transport.graphQLRequests) != 2 {
			t.Fatalf("repeated cursor requests = %d", len(transport.graphQLRequests))
		}
	})
	t.Run("no over-fetch at bound", func(t *testing.T) {
		transport := &twitchFixtureTransport{graphQLFixtures: []twitchGraphQLFixture{
			{body: twitchFixture(t, "collections_page1.json")},
			{body: twitchFixture(t, "collections_page2.json")},
		}}
		result, err := NewTwitch().Extract(context.Background(), Request{
			URL: "https://www.twitch.tv/fixture_channel/videos?filter=collections", Transport: transport,
		})
		if err != nil {
			t.Fatal(err)
		}
		iterator := result.Entries.Iterator()
		for i := 0; i < 2; i++ {
			if _, ok, nextErr := iterator.Next(context.Background()); nextErr != nil || !ok {
				t.Fatalf("entry %d ok=%t err=%v", i, ok, nextErr)
			}
		}
		if len(transport.graphQLRequests) != 1 {
			t.Fatalf("over-fetched: %d", len(transport.graphQLRequests))
		}
	})
	t.Run("missing continuation cursor stops", func(t *testing.T) {
		page := []byte(`[{"data":{"user":{"id":"9001","collections":{"edges":[{"__typename":"CollectionsItemEdge","node":{"__typename":"Collection","id":"FixtureCollection01","title":"Only"}}]}}}}]`)
		transport := &twitchFixtureTransport{graphQLFixtures: []twitchGraphQLFixture{{body: page}, {body: twitchFixture(t, "collections_page2.json")}}}
		result, err := NewTwitch().Extract(context.Background(), Request{
			URL: "https://www.twitch.tv/fixture_channel/videos?filter=collections", Transport: transport,
		})
		if err != nil {
			t.Fatal(err)
		}
		entries, err := CollectEntries(context.Background(), result.Entries, 10)
		if err != nil || len(entries) != 1 {
			t.Fatalf("entries=%#v err=%v", entries, err)
		}
		if len(transport.graphQLRequests) != 1 {
			t.Fatalf("missing cursor still continued: %d", len(transport.graphQLRequests))
		}
	})
}

func TestTwitchChannelCollectionsPageCursorSemantics(t *testing.T) {
	t.Parallel()
	oversized := strings.Repeat("c", twitchCollectionsMaxCursor+1)
	mustParse := func(raw string) []twitchChannelCollectionsPageResponse {
		t.Helper()
		var response []twitchChannelCollectionsPageResponse
		if err := json.Unmarshal([]byte(raw), &response); err != nil {
			t.Fatal(err)
		}
		return response
	}

	entries, cursor, err := parseTwitchChannelCollectionsPage(mustParse(`[{"data":{"user":{"id":"9001","collections":{"edges":[
			{"__typename":"CollectionsItemEdge","cursor":"keep-me","node":{"__typename":"Collection","id":"FixtureCollection01","title":"First"}},
			{"__typename":"NotCollectionsItemEdge","cursor":"skip-edge","node":{"__typename":"Collection","id":"FixtureCollection99","title":"Bad edge"}},
			{"__typename":"CollectionsItemEdge","cursor":"skip-node","node":{"__typename":"Video","id":"1000000001","title":"Bad node"}},
			{"__typename":"CollectionsItemEdge","cursor":"skip-id","node":{"__typename":"Collection","id":"bad/id","title":"Hostile"}},
			{"__typename":"CollectionsItemEdge","cursor":"","node":{"__typename":"Collection","id":"FixtureCollection02","title":"Empty cursor terminates"}}
		]}}}}]`))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 || entries[0].ID != "FixtureCollection01" || entries[1].ID != "FixtureCollection02" {
		t.Fatalf("entries = %#v", entries)
	}
	if cursor != "" {
		t.Fatalf("empty later cursor should terminate, got %q", cursor)
	}

	entries, cursor, err = parseTwitchChannelCollectionsPage(mustParse(`[{"data":{"user":{"id":"9001","collections":{"edges":[
			{"__typename":"CollectionsItemEdge","cursor":"keep-me","node":{"__typename":"Collection","id":"FixtureCollection01","title":"First"}},
			"not-an-object"
		]}}}}]`))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].ID != "FixtureCollection01" || cursor != "keep-me" {
		t.Fatalf("skipped edges disturbed cursor: entries=%#v cursor=%q", entries, cursor)
	}

	entries, cursor, err = parseTwitchChannelCollectionsPage(mustParse(`[{"data":{"user":{"id":"9001","collections":{"edges":[
			{"__typename":"CollectionsItemEdge","cursor":"keep-me","node":{"__typename":"Collection","id":"FixtureCollection01","title":"First"}},
			{"__typename":"CollectionsItemEdge","cursor":` + strconv.Quote(oversized) + `,"node":{"__typename":"Collection","id":"FixtureCollection02","title":"Oversized cursor terminates"}}
		]}}}}]`))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 || cursor != "" {
		t.Fatalf("oversized later cursor should terminate: entries=%#v cursor=%q", entries, cursor)
	}
}

func FuzzTwitchCollectionResponse(f *testing.F) {
	f.Add(twitchFixture(f, "collection_direct.json"))
	f.Add(twitchFixture(f, "collection_direct_empty.json"))
	f.Add(twitchFixture(f, "collection_direct_unavailable.json"))
	f.Add(twitchFixture(f, "collection_direct_malformed.json"))
	f.Add([]byte(`[{"data":{"collection":null}}]`))
	f.Add([]byte(`[]`))
	f.Fuzz(func(t *testing.T, body []byte) {
		if len(body) > 1<<20 {
			t.Skip()
		}
		var response []twitchCollectionResponse
		if err := json.Unmarshal(body, &response); err != nil {
			return
		}
		entries, _, err := parseTwitchCollection(response)
		if err != nil {
			if strings.Contains(err.Error(), string(body)) {
				t.Fatalf("error exposed body: %v", err)
			}
			return
		}
		if len(entries) > twitchCollectionsMaxEdges {
			t.Fatalf("entries exceed bound: %d", len(entries))
		}
		for _, entry := range entries {
			if !strings.HasPrefix(entry.URL, "https://www.twitch.tv/videos/") {
				t.Fatalf("hostile entry URL %q", entry.URL)
			}
			id := strings.TrimPrefix(entry.ID, "v")
			if !twitchVODPattern.MatchString(id) || entry.ExtractorKey != "twitch" || !entry.Transparent {
				t.Fatalf("unsafe entry %#v", entry)
			}
			if len(entry.Title) > twitchCollectionsMaxString {
				t.Fatalf("title exceeds bound")
			}
		}
	})
}

func FuzzTwitchChannelCollectionsPageResponse(f *testing.F) {
	f.Add(twitchFixture(f, "collections_page1.json"))
	f.Add(twitchFixture(f, "collections_page2.json"))
	f.Add(twitchFixture(f, "collections_empty.json"))
	f.Add(twitchFixture(f, "collections_not_found.json"))
	f.Add(twitchFixture(f, "collections_malformed.json"))
	f.Add([]byte(`[{"data":{"user":null}}]`))
	f.Add([]byte(`[]`))
	f.Fuzz(func(t *testing.T, body []byte) {
		if len(body) > 1<<20 {
			t.Skip()
		}
		var response []twitchChannelCollectionsPageResponse
		if err := json.Unmarshal(body, &response); err != nil {
			return
		}
		entries, cursor, err := parseTwitchChannelCollectionsPage(response)
		if err != nil {
			if strings.Contains(err.Error(), string(body)) {
				t.Fatalf("error exposed body: %v", err)
			}
			return
		}
		if len(cursor) > twitchCollectionsMaxCursor {
			t.Fatalf("cursor exceeds bound: %d", len(cursor))
		}
		if len(entries) > twitchCollectionsMaxEdges {
			t.Fatalf("entries exceed bound: %d", len(entries))
		}
		for _, entry := range entries {
			if !strings.HasPrefix(entry.URL, "https://www.twitch.tv/collections/") {
				t.Fatalf("hostile entry URL %q", entry.URL)
			}
			if !twitchCollectionPattern.MatchString(entry.ID) || entry.ExtractorKey != "twitch" || !entry.Transparent {
				t.Fatalf("unsafe entry %#v", entry)
			}
			if len(entry.Title) > twitchCollectionsMaxString {
				t.Fatalf("title exceeds bound")
			}
		}
	})
}

func assertTwitchCollectionGraphQLRequest(t *testing.T, request twitchRecordedRequest, collectionID string) {
	t.Helper()
	if got := request.header.Get("Client-ID"); got != twitchClientID {
		t.Fatalf("Client-ID = %q", got)
	}
	if got := request.header.Get("Content-Type"); got != "text/plain;charset=UTF-8" {
		t.Fatalf("Content-Type = %q", got)
	}
	if got := twitchCollectionRequestID(request.body); got != collectionID {
		t.Fatalf("collectionID = %q", got)
	}
	var operations []struct {
		OperationName string `json:"operationName"`
		Extensions    struct {
			PersistedQuery struct {
				Version    int    `json:"version"`
				SHA256Hash string `json:"sha256Hash"`
			} `json:"persistedQuery"`
		} `json:"extensions"`
	}
	if err := json.Unmarshal(request.body, &operations); err != nil {
		t.Fatal(err)
	}
	if len(operations) != 1 {
		t.Fatalf("operations = %#v", operations)
	}
	operation := operations[0]
	if operation.OperationName != twitchCollectionDirectOperation ||
		operation.Extensions.PersistedQuery.Version != 1 ||
		operation.Extensions.PersistedQuery.SHA256Hash != twitchOperationHashes[twitchCollectionDirectOperation] {
		t.Fatalf("operation = %#v", operation)
	}
}

func assertTwitchChannelCollectionsGraphQLRequest(t *testing.T, request twitchRecordedRequest, channel, cursor string) {
	t.Helper()
	if got := request.header.Get("Client-ID"); got != twitchClientID {
		t.Fatalf("Client-ID = %q", got)
	}
	if got := request.header.Get("Content-Type"); got != "text/plain;charset=UTF-8" {
		t.Fatalf("Content-Type = %q", got)
	}
	var operations []struct {
		OperationName string `json:"operationName"`
		Variables     struct {
			OwnerLogin string `json:"ownerLogin"`
			Limit      int    `json:"limit"`
			Cursor     string `json:"cursor"`
		} `json:"variables"`
		Extensions struct {
			PersistedQuery struct {
				Version    int    `json:"version"`
				SHA256Hash string `json:"sha256Hash"`
			} `json:"persistedQuery"`
		} `json:"extensions"`
	}
	if err := json.Unmarshal(request.body, &operations); err != nil {
		t.Fatal(err)
	}
	if len(operations) != 1 {
		t.Fatalf("operations = %#v", operations)
	}
	operation := operations[0]
	if operation.OperationName != twitchCollectionsOperation ||
		operation.Extensions.PersistedQuery.Version != 1 ||
		operation.Extensions.PersistedQuery.SHA256Hash != twitchOperationHashes[twitchCollectionsOperation] {
		t.Fatalf("operation = %#v", operation)
	}
	if operation.Variables.OwnerLogin != channel || operation.Variables.Limit != twitchCollectionsPageLimit {
		t.Fatalf("variables = %#v", operation.Variables)
	}
	var raw []map[string]any
	if err := json.Unmarshal(request.body, &raw); err != nil {
		t.Fatal(err)
	}
	variables := raw[0]["variables"].(map[string]any)
	if cursor == "" {
		if _, present := variables["cursor"]; present {
			t.Fatalf("unexpected cursor in %#v", variables)
		}
	} else if got, _ := variables["cursor"].(string); got != cursor {
		t.Fatalf("cursor = %#v", variables["cursor"])
	}
}

func TestTwitchChannelClipsRangeMappingsAndRoutes(t *testing.T) {
	t.Parallel()
	tests := []struct {
		rawURL string
		filter string
		label  string
		ok     bool
	}{
		{"https://www.twitch.tv/fixture_channel/clips", "LAST_WEEK", "Top 7D", true},
		{"https://www.twitch.tv/fixture_channel/clips?range=7d", "LAST_WEEK", "Top 7D", true},
		{"https://www.twitch.tv/fixture_channel/clips?range=", "LAST_WEEK", "Top 7D", true},
		{"https://www.twitch.tv/fixture_channel/clips?range=unknown", "LAST_WEEK", "Top 7D", true},
		{"https://www.twitch.tv/fixture_channel/clips?range=24hr", "LAST_DAY", "Top 24H", true},
		{"https://www.twitch.tv/fixture_channel/clips?range=30d", "LAST_MONTH", "Top 30D", true},
		{"https://www.twitch.tv/fixture_channel/clips?range=all", "ALL_TIME", "Top All", true},
		{"https://www.twitch.tv/fixture_channel/clips?filter=clips&range=all", "ALL_TIME", "Top All", true},
		{"https://www.twitch.tv/fixture_channel/videos?filter=clips", "LAST_WEEK", "Top 7D", true},
		{"https://www.twitch.tv/fixture_channel/videos?filter=clips&range=24hr", "LAST_DAY", "Top 24H", true},
		{"https://m.twitch.tv/fixture_channel/videos?filter=clips&range=30d", "LAST_MONTH", "Top 30D", true},
		{"https://go.twitch.tv/fixture_channel/videos?filter=clips&range=all", "ALL_TIME", "Top All", true},
		{"https://www.twitch.tv/fixture_channel/videos?filter=clips&range=7d&feature=share", "LAST_WEEK", "Top 7D", true},
		{"https://www.twitch.tv/Fixture_Channel/clips?range=24hr", "LAST_DAY", "Top 24H", true},
		{"https://www.twitch.tv/fixture_channel/clips?range=24hr&range=7d", "", "", false},
		{"https://www.twitch.tv/fixture_channel/videos?filter=clips&filter=archives", "", "", false},
		{"https://www.twitch.tv/fixture_channel/clips#frag", "", "", false},
		{"https://www.twitch.tv/fixture_channel/clips?range=%zz", "", "", false},
		{"https://www.twitch.tv/fixture_channel/clips?range=all%00", "", "", false},
		{"https://www.twitch.tv/fixture_channel/clips?range=%FF", "", "", false},
		{"https://www.twitch.tv/fixture_channel/clips/extra", "", "", false},
		{"https://www.twitch.tv/directory/clips", "", "", false},
		{"https://www.twitch.tv/fixture%2Fchannel/clips", "", "", false},
		{"https://user@www.twitch.tv/fixture_channel/clips", "", "", false},
		{"https://www.twitch.tv:443/fixture_channel/clips", "", "", false},
		{"https://www.twitch.tv/fixture_channel/profile?filter=clips", "", "", false},
		{"https://www.twitch.tv/fixture_channel/videos/all?filter=clips", "", "", false},
		{"https://www.twitch.tv/fixture_channel/videos?filter=clips&range=" + strings.Repeat("a", twitchClipsMaxString+1), "", "", false},
	}
	for _, test := range tests {
		test := test
		t.Run(test.rawURL, func(t *testing.T) {
			parsed, err := url.Parse(test.rawURL)
			if err != nil {
				t.Fatal(err)
			}
			target, ok := classifyTwitchURL(parsed)
			if ok != test.ok {
				t.Fatalf("ok=%t want %t target=%#v", ok, test.ok, target)
			}
			if !test.ok {
				return
			}
			if target.kind != twitchKindChannelClips || target.id != "fixture_channel" {
				t.Fatalf("target=%#v", target)
			}
			if target.clips.filter != test.filter || target.clips.label != test.label {
				t.Fatalf("clips=%#v want filter=%q label=%q", target.clips, test.filter, test.label)
			}
		})
	}
}

func TestTwitchChannelClipsLazyContinuationReusableAndContract(t *testing.T) {
	transport := &twitchFixtureTransport{clipsPages: map[string][]byte{
		"":                         twitchFixture(t, "clips_page1.json"),
		"cursor-clips-page1-final": twitchFixture(t, "clips_page2.json"),
	}}
	result, err := NewTwitch().Extract(context.Background(), Request{
		URL: "https://www.twitch.tv/fixture_channel/clips?range=24hr", Transport: transport,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.IsPlaylist() {
		t.Fatal("expected playlist")
	}
	assertTwitchString(t, result, "id", "fixture_channel")
	assertTwitchString(t, result, "title", "fixture_channel - Clips Top 24H")
	if len(transport.graphQLRequests) != 0 {
		t.Fatal("playlist page fetched eagerly")
	}

	iterator := result.Entries.Iterator()
	first, ok, err := iterator.Next(context.Background())
	if err != nil || !ok {
		t.Fatalf("first entry = %#v ok=%t err=%v", first, ok, err)
	}
	if len(transport.graphQLRequests) != 1 {
		t.Fatalf("first page requests = %d", len(transport.graphQLRequests))
	}
	assertTwitchChannelClipsGraphQLRequest(t, transport.graphQLRequests[0], "fixture_channel", "LAST_DAY", "")
	if first.ID != "4123456789" || first.URL != "https://clips.twitch.tv/CulturedFixtureSlug-abc_123" || !first.Transparent || first.ExtractorKey != "twitch" {
		t.Fatalf("first entry = %#v", first)
	}
	if first.Title != "Synthetic clip one" {
		t.Fatalf("first title = %q", first.Title)
	}
	if first.Thumbnail != "https://static-cdn.example.test/clip1-320x180.jpg" || !first.HasDuration || first.Duration != 32.5 ||
		!first.HasTimestamp || first.Timestamp != 1705320000 || !first.HasViewCount || first.ViewCount != 101 || first.Language != "en" {
		t.Fatalf("first soft metadata = %#v", first)
	}
	encoded, err := value.ObjectValue(first.Object()).MarshalJSON()
	if err != nil {
		t.Fatal(err)
	}
	wantJSON := `{"_type":"url_transparent","url":"https://clips.twitch.tv/CulturedFixtureSlug-abc_123","ie_key":"twitch","id":"4123456789","title":"Synthetic clip one","thumbnail":"https://static-cdn.example.test/clip1-320x180.jpg","duration":32.5,"timestamp":1705320000,"view_count":101,"language":"en"}`
	if string(encoded) != wantJSON {
		t.Fatalf("first entry JSON = %s", encoded)
	}
	second, ok, err := iterator.Next(context.Background())
	if err != nil || !ok || second.ID != "4123456790" {
		t.Fatalf("second entry = %#v ok=%t err=%v", second, ok, err)
	}
	if second.URL != "https://www.twitch.tv/fixture_channel/clip/StormyThankfulSproutFutureMan" {
		t.Fatalf("second url = %q", second.URL)
	}
	if len(transport.graphQLRequests) != 1 {
		t.Fatal("continuation over-fetched before page exhaustion")
	}
	third, ok, err := iterator.Next(context.Background())
	if err != nil || !ok || third.ID != "4123456791" {
		t.Fatalf("third entry = %#v ok=%t err=%v", third, ok, err)
	}
	if len(transport.graphQLRequests) != 2 {
		t.Fatalf("continuation requests = %d", len(transport.graphQLRequests))
	}
	assertTwitchChannelClipsGraphQLRequest(t, transport.graphQLRequests[1], "fixture_channel", "LAST_DAY", "cursor-clips-page1-final")
	if _, ok, err := iterator.Next(context.Background()); err != nil || ok {
		t.Fatalf("expected end, ok=%t err=%v", ok, err)
	}

	again, err := CollectEntries(context.Background(), result.Entries, 10)
	if err != nil || len(again) != 3 || again[0].ID != "4123456789" || again[2].ID != "4123456791" {
		t.Fatalf("reusable iteration = %#v err=%v", again, err)
	}

	videosRoute := &twitchFixtureTransport{graphQLFixtures: []twitchGraphQLFixture{{body: twitchFixture(t, "clips_empty.json")}}}
	videosResult, err := NewTwitch().Extract(context.Background(), Request{
		URL: "https://www.twitch.tv/fixture_channel/videos?filter=clips&range=all", Transport: videosRoute,
	})
	if err != nil {
		t.Fatal(err)
	}
	assertTwitchString(t, videosResult, "title", "fixture_channel - Clips Top All")
	if _, _, err := videosResult.Entries.Iterator().Next(context.Background()); err != nil {
		t.Fatal(err)
	}
	assertTwitchChannelClipsGraphQLRequest(t, videosRoute.graphQLRequests[0], "fixture_channel", "ALL_TIME", "")
}

func TestTwitchChannelClipsFailuresCancellationAndBounds(t *testing.T) {
	t.Run("empty channel", func(t *testing.T) {
		transport := &twitchFixtureTransport{graphQLFixtures: []twitchGraphQLFixture{{body: twitchFixture(t, "clips_empty.json")}}}
		result, err := NewTwitch().Extract(context.Background(), Request{
			URL: "https://www.twitch.tv/fixture_channel/clips", Transport: transport,
		})
		if err != nil {
			t.Fatal(err)
		}
		entries, err := CollectEntries(context.Background(), result.Entries, 10)
		if err != nil || len(entries) != 0 {
			t.Fatalf("empty = %#v err=%v", entries, err)
		}
	})
	t.Run("absent channel", func(t *testing.T) {
		transport := &twitchFixtureTransport{graphQLFixtures: []twitchGraphQLFixture{{body: twitchFixture(t, "clips_not_found.json")}}}
		result, err := NewTwitch().Extract(context.Background(), Request{
			URL: "https://www.twitch.tv/missing_channel/clips", Transport: transport,
		})
		if err != nil {
			t.Fatal(err)
		}
		_, _, err = result.Entries.Iterator().Next(context.Background())
		if !errors.Is(err, ErrUnavailable) {
			t.Fatalf("absent channel err=%v", err)
		}
	})
	t.Run("malformed envelope", func(t *testing.T) {
		transport := &twitchFixtureTransport{graphQLFixtures: []twitchGraphQLFixture{{body: twitchFixture(t, "clips_malformed.json")}}}
		result, err := NewTwitch().Extract(context.Background(), Request{
			URL: "https://www.twitch.tv/fixture_channel/clips", Transport: transport,
		})
		if err != nil {
			t.Fatal(err)
		}
		_, _, err = result.Entries.Iterator().Next(context.Background())
		if !errors.Is(err, ErrInvalidMetadata) {
			t.Fatalf("malformed err=%v", err)
		}
	})
	t.Run("http categories", func(t *testing.T) {
		cases := []struct {
			status int
			want   error
		}{
			{http.StatusUnauthorized, ErrAuthentication},
			{http.StatusForbidden, ErrAuthentication},
			{http.StatusNotFound, ErrUnavailable},
			{http.StatusGone, ErrUnavailable},
			{http.StatusTooManyRequests, ErrTwitchRateLimited},
			{http.StatusBadGateway, ErrTwitchNetwork},
		}
		for _, test := range cases {
			transport := &twitchFixtureTransport{graphQLFixtures: []twitchGraphQLFixture{{status: test.status, body: []byte(`[]`)}}}
			result, err := NewTwitch().Extract(context.Background(), Request{
				URL: "https://www.twitch.tv/fixture_channel/clips", Transport: transport,
			})
			if err != nil {
				t.Fatal(err)
			}
			_, _, err = result.Entries.Iterator().Next(context.Background())
			if !errors.Is(err, test.want) {
				t.Fatalf("status %d err=%v want %v", test.status, err, test.want)
			}
		}
	})
	t.Run("secret safe errors", func(t *testing.T) {
		const secret = "fixture-clips-secret-token"
		transport := &twitchFixtureTransport{graphQLFixtures: []twitchGraphQLFixture{
			{body: []byte(`[{"data":{"user":{"id":"1","clips":{"edges":[]}}},"errors":[{"message":"` + secret + `"}]}]`)},
		}}
		result, err := NewTwitch().Extract(context.Background(), Request{
			URL: "https://www.twitch.tv/fixture_channel/clips", Transport: transport,
		})
		if err != nil {
			t.Fatal(err)
		}
		_, _, err = result.Entries.Iterator().Next(context.Background())
		if !errors.Is(err, ErrInvalidMetadata) {
			t.Fatalf("err=%v", err)
		}
		if strings.Contains(err.Error(), secret) {
			t.Fatalf("error exposed secret: %v", err)
		}
	})
	t.Run("cancellation before initial fetch", func(t *testing.T) {
		transport := &twitchFixtureTransport{graphQLFixtures: []twitchGraphQLFixture{{body: twitchFixture(t, "clips_page1.json")}}}
		result, err := NewTwitch().Extract(context.Background(), Request{
			URL: "https://www.twitch.tv/fixture_channel/clips", Transport: transport,
		})
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, _, err = result.Entries.Iterator().Next(ctx)
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err=%v", err)
		}
		if len(transport.graphQLRequests) != 0 {
			t.Fatal("canceled iterator still fetched")
		}
	})
	t.Run("cancellation during continuation", func(t *testing.T) {
		transport := &twitchFixtureTransport{graphQLFixtures: []twitchGraphQLFixture{
			{body: twitchFixture(t, "clips_page1.json")},
			{body: twitchFixture(t, "clips_page2.json")},
		}}
		result, err := NewTwitch().Extract(context.Background(), Request{
			URL: "https://www.twitch.tv/fixture_channel/clips", Transport: transport,
		})
		if err != nil {
			t.Fatal(err)
		}
		iterator := result.Entries.Iterator()
		if _, _, err := iterator.Next(context.Background()); err != nil {
			t.Fatal(err)
		}
		if _, _, err := iterator.Next(context.Background()); err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, _, err = iterator.Next(ctx)
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("continuation cancel err=%v", err)
		}
		if len(transport.graphQLRequests) != 1 {
			t.Fatalf("requests after cancel = %d", len(transport.graphQLRequests))
		}
	})
	t.Run("repeated cursor stops", func(t *testing.T) {
		loop := []byte(`[{"data":{"user":{"id":"9001","clips":{"edges":[{"__typename":"ClipEdge","cursor":"loop","node":{"__typename":"Clip","id":"4123456789","url":"https://clips.twitch.tv/CulturedFixtureSlug-abc_123","title":"Loop"}}]}}}}]`)
		transport := &twitchFixtureTransport{graphQLFixtures: []twitchGraphQLFixture{{body: loop}, {body: loop}, {body: loop}}}
		result, err := NewTwitch().Extract(context.Background(), Request{
			URL: "https://www.twitch.tv/fixture_channel/clips", Transport: transport,
		})
		if err != nil {
			t.Fatal(err)
		}
		entries, err := CollectEntries(context.Background(), result.Entries, 100)
		if err != nil {
			t.Fatal(err)
		}
		if len(entries) != 2 {
			t.Fatalf("repeated cursor entries = %d", len(entries))
		}
		if len(transport.graphQLRequests) != 2 {
			t.Fatalf("repeated cursor requests = %d", len(transport.graphQLRequests))
		}
	})
	t.Run("no over-fetch at bound", func(t *testing.T) {
		transport := &twitchFixtureTransport{graphQLFixtures: []twitchGraphQLFixture{
			{body: twitchFixture(t, "clips_page1.json")},
			{body: twitchFixture(t, "clips_page2.json")},
		}}
		result, err := NewTwitch().Extract(context.Background(), Request{
			URL: "https://www.twitch.tv/fixture_channel/clips", Transport: transport,
		})
		if err != nil {
			t.Fatal(err)
		}
		iterator := result.Entries.Iterator()
		for i := 0; i < 2; i++ {
			if _, ok, nextErr := iterator.Next(context.Background()); nextErr != nil || !ok {
				t.Fatalf("entry %d ok=%t err=%v", i, ok, nextErr)
			}
		}
		if len(transport.graphQLRequests) != 1 {
			t.Fatalf("over-fetched: %d", len(transport.graphQLRequests))
		}
	})
	t.Run("missing continuation cursor stops", func(t *testing.T) {
		page := []byte(`[{"data":{"user":{"id":"9001","clips":{"edges":[{"__typename":"ClipEdge","node":{"__typename":"Clip","id":"4123456789","url":"https://clips.twitch.tv/CulturedFixtureSlug-abc_123","title":"Only"}}]}}}}]`)
		transport := &twitchFixtureTransport{graphQLFixtures: []twitchGraphQLFixture{{body: page}, {body: twitchFixture(t, "clips_page2.json")}}}
		result, err := NewTwitch().Extract(context.Background(), Request{
			URL: "https://www.twitch.tv/fixture_channel/clips", Transport: transport,
		})
		if err != nil {
			t.Fatal(err)
		}
		entries, err := CollectEntries(context.Background(), result.Entries, 10)
		if err != nil || len(entries) != 1 {
			t.Fatalf("entries=%#v err=%v", entries, err)
		}
		if len(transport.graphQLRequests) != 1 {
			t.Fatalf("missing cursor still continued: %d", len(transport.graphQLRequests))
		}
	})
	t.Run("edge bound", func(t *testing.T) {
		var builder strings.Builder
		builder.WriteString(`[`)
		for i := 0; i < twitchClipsMaxEdges+1; i++ {
			if i > 0 {
				builder.WriteByte(',')
			}
			builder.WriteString(`{"__typename":"ClipEdge","cursor":"c`)
			builder.WriteString(strconv.Itoa(i))
			builder.WriteString(`","node":{"__typename":"Clip","id":"412345`)
			builder.WriteString(strconv.Itoa(6000 + i))
			builder.WriteString(`","url":"https://clips.twitch.tv/Slug`)
			builder.WriteString(strconv.Itoa(i))
			builder.WriteString(`","title":"Clip"}}`)
		}
		builder.WriteString(`]`)
		_, _, err := parseTwitchChannelClipsPage([]twitchChannelClipsPageResponse{{
			Data: struct {
				User *twitchChannelClipsUser `json:"user"`
			}{User: &twitchChannelClipsUser{
				ID: json.RawMessage(`"9001"`),
				Clips: &struct {
					Edges json.RawMessage `json:"edges"`
				}{Edges: json.RawMessage(builder.String())},
			}},
		}})
		if !errors.Is(err, ErrInvalidPlaylist) {
			t.Fatalf("edge-bound err=%v", err)
		}
	})
	t.Run("page limit exactly 20", func(t *testing.T) {
		if twitchClipsPageLimit != 20 || twitchClipsMaxEdges != 20 {
			t.Fatalf("page=%d edges=%d", twitchClipsPageLimit, twitchClipsMaxEdges)
		}
		transport := &twitchFixtureTransport{graphQLFixtures: []twitchGraphQLFixture{{body: twitchFixture(t, "clips_empty.json")}}}
		result, err := NewTwitch().Extract(context.Background(), Request{
			URL: "https://www.twitch.tv/fixture_channel/clips", Transport: transport,
		})
		if err != nil {
			t.Fatal(err)
		}
		if _, _, err := result.Entries.Iterator().Next(context.Background()); err != nil {
			t.Fatal(err)
		}
		assertTwitchChannelClipsGraphQLRequest(t, transport.graphQLRequests[0], "fixture_channel", "LAST_WEEK", "")
	})
}

func TestTwitchChannelClipsPageCursorSemanticsAndDedup(t *testing.T) {
	t.Parallel()
	oversized := strings.Repeat("c", twitchClipsMaxCursor+1)
	mustParse := func(raw string) []twitchChannelClipsPageResponse {
		t.Helper()
		var response []twitchChannelClipsPageResponse
		if err := json.Unmarshal([]byte(raw), &response); err != nil {
			t.Fatal(err)
		}
		return response
	}

	entries, cursor, err := parseTwitchChannelClipsPage(mustParse(`[{"data":{"user":{"id":"9001","clips":{"edges":[
			{"__typename":"ClipEdge","cursor":"keep-me","node":{"__typename":"Clip","id":"4123456789","url":"https://clips.twitch.tv/CulturedFixtureSlug-abc_123","title":"First","thumbnailURL":"https://static-cdn.example.test/a.jpg","durationSeconds":10,"createdAt":"2024-01-01T00:00:00Z","viewCount":5,"language":"en"}},
			{"__typename":"NotClipEdge","cursor":"skip-edge","node":{"__typename":"Clip","id":"4999999999","url":"https://clips.twitch.tv/Skipped","title":"Bad edge"}},
			{"__typename":"ClipEdge","cursor":"skip-node","node":{"__typename":"Video","id":"1000000001","url":"https://www.twitch.tv/videos/1000000001","title":"Bad node"}},
			{"__typename":"ClipEdge","cursor":"skip-url","node":{"__typename":"Clip","id":"4888888888","url":"https://evil.example/x","title":"Hostile"}},
			{"__typename":"ClipEdge","cursor":"","node":{"__typename":"Clip","id":"4123456790","url":"https://clips.twitch.tv/ConfidentBraveHumanChefFrank","title":"Empty cursor terminates"}}
		]}}}}]`))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 || entries[0].ID != "4123456789" || entries[1].ID != "4123456790" {
		t.Fatalf("entries = %#v", entries)
	}
	if cursor != "" {
		t.Fatalf("empty later cursor should terminate, got %q", cursor)
	}

	entries, cursor, err = parseTwitchChannelClipsPage(mustParse(`[{"data":{"user":{"id":"9001","clips":{"edges":[
			{"__typename":"ClipEdge","cursor":"keep-me","node":{"__typename":"Clip","id":"4123456789","url":"https://clips.twitch.tv/CulturedFixtureSlug-abc_123","title":"First"}},
			{"__typename":"ClipEdge","cursor":"dup-id","node":{"__typename":"Clip","id":"4123456789","url":"https://clips.twitch.tv/OtherSlugName","title":"Duplicate id"}},
			{"__typename":"ClipEdge","cursor":"dup-url","node":{"__typename":"Clip","id":"4123456799","url":"https://clips.twitch.tv/CulturedFixtureSlug-abc_123","title":"Duplicate url"}},
			{"__typename":"ClipEdge","cursor":"keep-two","node":{"__typename":"Clip","id":"4123456790","url":"https://clips.twitch.tv/StormyThankfulSproutFutureMan","title":"Second"}}
		]}}}}]`))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 || entries[0].ID != "4123456789" || entries[1].ID != "4123456790" || cursor != "keep-two" {
		t.Fatalf("dedup entries=%#v cursor=%q", entries, cursor)
	}

	entries, cursor, err = parseTwitchChannelClipsPage(mustParse(`[{"data":{"user":{"id":"9001","clips":{"edges":[
			{"__typename":"ClipEdge","cursor":"keep-me","node":{"__typename":"Clip","id":"4123456789","url":"https://clips.twitch.tv/CulturedFixtureSlug-abc_123","title":"First"}},
			{"__typename":"ClipEdge","cursor":` + strconv.Quote(oversized) + `,"node":{"__typename":"Clip","id":"4123456790","url":"https://clips.twitch.tv/ConfidentBraveHumanChefFrank","title":"Oversized cursor terminates"}}
		]}}}}]`))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 || cursor != "" {
		t.Fatalf("oversized later cursor should terminate: entries=%#v cursor=%q", entries, cursor)
	}
}

func TestTwitchChannelClipEntryRejectsHostilePayloads(t *testing.T) {
	t.Parallel()
	for _, raw := range []string{
		`{"__typename":"Clip","id":"4123456789","url":"file:///etc/passwd","title":"Bad scheme"}`,
		`{"__typename":"Clip","id":"4123456789","url":"https://user:pass@clips.twitch.tv/Slug","title":"Creds"}`,
		`{"__typename":"Clip","id":"4123456789","url":"https://clips.twitch.tv:443/Slug","title":"Port"}`,
		`{"__typename":"Clip","id":"4123456789","url":"https://clips.twitch.tv/bad%2Fslug","title":"Encoded"}`,
		`{"__typename":"Clip","id":"bad/id","url":"https://evil.example/x","title":"Hostile"}`,
		`{"__typename":"Clip","id":"4123456789","url":"https://clips.twitch.tv/Slug","title":"` + strings.Repeat("t", twitchClipsMaxString+1) + `"}`,
		`{"__typename":"Video","id":"4123456789","url":"https://clips.twitch.tv/Slug","title":"Wrong type"}`,
		`{"__typename":"Clip","id":"4123456789","url":"","title":"Missing url"}`,
	} {
		if _, ok := twitchChannelClipEntry(json.RawMessage(raw)); ok {
			t.Fatalf("accepted hostile node %s", raw[:min(len(raw), 120)])
		}
	}
	entry, ok := twitchChannelClipEntry(json.RawMessage(`{"__typename":"Clip","url":"https://clips.twitch.tv/CulturedFixtureSlug-abc_123","title":"Slug fallback"}`))
	if !ok || entry.ID != "CulturedFixtureSlug-abc_123" {
		t.Fatalf("slug fallback entry=%#v ok=%t", entry, ok)
	}
}

func TestTwitchChannelClipEntrySoftMetadataParity(t *testing.T) {
	t.Parallel()
	complete, ok := twitchChannelClipEntry(json.RawMessage(`{
		"__typename":"Clip",
		"id":"4123456789",
		"url":"https://clips.twitch.tv/CulturedFixtureSlug-abc_123",
		"title":"Complete",
		"thumbnailURL":"https://static-cdn.example.test/complete.jpg",
		"durationSeconds":32.5,
		"createdAt":"2024-01-15T12:00:00Z",
		"viewCount":101,
		"language":"en"
	}`))
	if !ok {
		t.Fatal("expected complete entry")
	}
	if complete.Thumbnail != "https://static-cdn.example.test/complete.jpg" || !complete.HasDuration || complete.Duration != 32.5 ||
		!complete.HasTimestamp || complete.Timestamp != 1705320000 || !complete.HasViewCount || complete.ViewCount != 101 || complete.Language != "en" {
		t.Fatalf("complete=%#v", complete)
	}

	zeros, ok := twitchChannelClipEntry(json.RawMessage(`{
		"__typename":"Clip",
		"id":"4123456789",
		"url":"https://clips.twitch.tv/CulturedFixtureSlug-abc_123",
		"title":"Zeros",
		"durationSeconds":0,
		"createdAt":"1970-01-01T00:00:00Z",
		"viewCount":0,
		"language":"en"
	}`))
	if !ok || !zeros.HasDuration || zeros.Duration != 0 || !zeros.HasTimestamp || zeros.Timestamp != 0 || !zeros.HasViewCount || zeros.ViewCount != 0 {
		t.Fatalf("zeros=%#v ok=%t", zeros, ok)
	}
	encoded, err := value.ObjectValue(zeros.Object()).MarshalJSON()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(encoded), `"duration":0`) || !strings.Contains(string(encoded), `"timestamp":0`) || !strings.Contains(string(encoded), `"view_count":0`) {
		t.Fatalf("zeros JSON omitted valid zeros: %s", encoded)
	}

	absent, ok := twitchChannelClipEntry(json.RawMessage(`{
		"__typename":"Clip",
		"id":"4123456789",
		"url":"https://clips.twitch.tv/CulturedFixtureSlug-abc_123",
		"title":"Absent"
	}`))
	if !ok || absent.Thumbnail != "" || absent.HasDuration || absent.HasTimestamp || absent.HasViewCount || absent.Language != "" {
		t.Fatalf("absent=%#v ok=%t", absent, ok)
	}
	absentJSON, err := value.ObjectValue(absent.Object()).MarshalJSON()
	if err != nil {
		t.Fatal(err)
	}
	wantAbsent := `{"_type":"url_transparent","url":"https://clips.twitch.tv/CulturedFixtureSlug-abc_123","ie_key":"twitch","id":"4123456789","title":"Absent"}`
	if string(absentJSON) != wantAbsent {
		t.Fatalf("absent JSON = %s", absentJSON)
	}

	numericStrings, ok := twitchChannelClipEntry(json.RawMessage(`{
		"__typename":"Clip",
		"id":"4123456789",
		"url":"https://clips.twitch.tv/CulturedFixtureSlug-abc_123",
		"title":"Numeric strings",
		"durationSeconds":"18.25",
		"viewCount":"42"
	}`))
	if !ok || !numericStrings.HasDuration || numericStrings.Duration != 18.25 || !numericStrings.HasViewCount || numericStrings.ViewCount != 42 {
		t.Fatalf("numeric strings=%#v ok=%t", numericStrings, ok)
	}

	for _, test := range []struct {
		name  string
		raw   string
		check func(Entry)
	}{
		{
			name: "malformed optional omitted",
			raw: `{
				"__typename":"Clip",
				"id":"4123456789",
				"url":"https://clips.twitch.tv/CulturedFixtureSlug-abc_123",
				"title":"Keep",
				"thumbnailURL":"https://static-cdn.example.test/ok.jpg",
				"durationSeconds":"not-a-number",
				"createdAt":"not-rfc3339",
				"viewCount":"nope",
				"language":` + strconv.Quote(strings.Repeat("l", twitchClipsMaxLanguage+1)) + `
			}`,
			check: func(entry Entry) {
				if entry.Thumbnail != "https://static-cdn.example.test/ok.jpg" || entry.HasDuration || entry.HasTimestamp || entry.HasViewCount || entry.Language != "" {
					t.Fatalf("malformed optional = %#v", entry)
				}
			},
		},
		{
			name: "unsafe thumbnail omitted",
			raw: `{
				"__typename":"Clip",
				"id":"4123456789",
				"url":"https://clips.twitch.tv/CulturedFixtureSlug-abc_123",
				"title":"Keep",
				"thumbnailURL":"https://evil.example/thumb.jpg",
				"durationSeconds":10,
				"viewCount":3,
				"language":"en"
			}`,
			check: func(entry Entry) {
				if entry.Thumbnail != "" || !entry.HasDuration || entry.Duration != 10 || !entry.HasViewCount || entry.ViewCount != 3 || entry.Language != "en" {
					t.Fatalf("unsafe thumbnail = %#v", entry)
				}
			},
		},
		{
			name: "negative duration omitted",
			raw: `{
				"__typename":"Clip",
				"id":"4123456789",
				"url":"https://clips.twitch.tv/CulturedFixtureSlug-abc_123",
				"title":"Keep",
				"durationSeconds":-1,
				"viewCount":5
			}`,
			check: func(entry Entry) {
				if entry.HasDuration || !entry.HasViewCount || entry.ViewCount != 5 {
					t.Fatalf("negative duration = %#v", entry)
				}
			},
		},
		{
			name: "negative views omitted",
			raw: `{
				"__typename":"Clip",
				"id":"4123456789",
				"url":"https://clips.twitch.tv/CulturedFixtureSlug-abc_123",
				"title":"Keep",
				"durationSeconds":1,
				"viewCount":-1
			}`,
			check: func(entry Entry) {
				if !entry.HasDuration || entry.Duration != 1 || entry.HasViewCount {
					t.Fatalf("negative views = %#v", entry)
				}
			},
		},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			entry, ok := twitchChannelClipEntry(json.RawMessage(test.raw))
			if !ok {
				t.Fatal("valid clip URL was dropped")
			}
			test.check(entry)
		})
	}
}

func TestTwitchOptionalIntBoundaries(t *testing.T) {
	t.Parallel()
	max := int64(math.MaxInt64)
	mustPresent := func(raw string, want int64) {
		t.Helper()
		got, ok := twitchOptionalInt(json.RawMessage(raw), max)
		if !ok || got != want {
			t.Fatalf("twitchOptionalInt(%s) = %d, %t; want %d, true", raw, got, ok, want)
		}
	}
	mustAbsent := func(raw string) {
		t.Helper()
		got, ok := twitchOptionalInt(json.RawMessage(raw), max)
		if ok {
			t.Fatalf("twitchOptionalInt(%s) = %d, true; want absent", raw, got)
		}
	}

	mustPresent("0", 0)
	mustPresent(`"0"`, 0)
	mustPresent(strconv.FormatInt(max, 10), max)
	mustPresent(strconv.Quote(strconv.FormatInt(max, 10)), max)
	mustPresent("+42", 42)
	mustPresent(`"42"`, 42)
	mustPresent("10.0", 10)
	mustPresent("1e2", 100)
	mustPresent(`"1e2"`, 100)

	mustAbsent(strconv.FormatUint(uint64(max)+1, 10))
	mustAbsent(strconv.Quote(strconv.FormatUint(uint64(max)+1, 10)))
	mustAbsent("1e1000")
	mustAbsent("1e308")
	mustAbsent("1.5")
	mustAbsent(`"1.5"`)
	mustAbsent("-1")
	mustAbsent(`"-1"`)
	mustAbsent("-0")
	mustAbsent("NaN")
	mustAbsent("Inf")
	mustAbsent("+Inf")
	mustAbsent("-Inf")
	mustAbsent("null")
	mustAbsent("")
	mustAbsent(`"not-a-number"`)
	mustAbsent(strconv.Quote(strings.Repeat("9", twitchClipsMaxNumericText+1)))
}

func TestTwitchChannelClipsRequestMarshalingValidatesInputs(t *testing.T) {
	t.Parallel()
	if _, err := marshalTwitchChannelClipsRequest("bad/channel", twitchClipsQuery{filter: "LAST_WEEK"}, ""); !errors.Is(err, ErrInvalidMetadata) {
		t.Fatalf("hostile channel err=%v", err)
	}
	if _, err := marshalTwitchChannelClipsRequest("fixture_channel", twitchClipsQuery{}, ""); !errors.Is(err, ErrInvalidMetadata) {
		t.Fatalf("empty filter err=%v", err)
	}
	body, err := marshalTwitchChannelClipsRequest("fixture_channel", twitchClipsQuery{filter: "LAST_WEEK", label: "Top 7D"}, "cursor-1")
	if err != nil || len(body) == 0 {
		t.Fatalf("valid request err=%v body=%d", err, len(body))
	}
}

func FuzzTwitchChannelClipsPageResponse(f *testing.F) {
	f.Add(twitchFixture(f, "clips_page1.json"))
	f.Add(twitchFixture(f, "clips_page2.json"))
	f.Add(twitchFixture(f, "clips_empty.json"))
	f.Add(twitchFixture(f, "clips_not_found.json"))
	f.Add(twitchFixture(f, "clips_malformed.json"))
	f.Add([]byte(`[{"data":{"user":null}}]`))
	f.Add([]byte(`[]`))
	f.Fuzz(func(t *testing.T, body []byte) {
		if len(body) > 1<<20 {
			t.Skip()
		}
		var response []twitchChannelClipsPageResponse
		if err := json.Unmarshal(body, &response); err != nil {
			return
		}
		entries, cursor, err := parseTwitchChannelClipsPage(response)
		if err != nil {
			if strings.Contains(err.Error(), string(body)) {
				t.Fatalf("error exposed body: %v", err)
			}
			return
		}
		if len(cursor) > twitchClipsMaxCursor {
			t.Fatalf("cursor exceeds bound: %d", len(cursor))
		}
		if len(entries) > twitchClipsMaxEdges {
			t.Fatalf("entries exceed bound: %d", len(entries))
		}
		seenID := make(map[string]struct{}, len(entries))
		seenURL := make(map[string]struct{}, len(entries))
		for _, entry := range entries {
			parsed, err := url.Parse(entry.URL)
			if err != nil {
				t.Fatalf("hostile entry URL %q", entry.URL)
			}
			target, ok := classifyTwitchURL(parsed)
			if !ok || target.kind != twitchKindClip || !twitchClipPattern.MatchString(entry.ID) || entry.ExtractorKey != "twitch" || !entry.Transparent {
				t.Fatalf("unsafe entry %#v", entry)
			}
			if len(entry.Title) > twitchClipsMaxString {
				t.Fatalf("title exceeds bound")
			}
			if entry.Thumbnail != "" && !validTwitchAssetURL(entry.Thumbnail) {
				t.Fatalf("unsafe thumbnail %q", entry.Thumbnail)
			}
			if entry.HasDuration && (entry.Duration < 0 || entry.Duration > float64(30*24*60*60)) {
				t.Fatalf("duration out of bounds %#v", entry)
			}
			if entry.HasViewCount && entry.ViewCount < 0 {
				t.Fatalf("view_count out of bounds %#v", entry)
			}
			if len(entry.Language) > twitchClipsMaxLanguage {
				t.Fatalf("language exceeds bound")
			}
			object := entry.Object()
			if entry.HasDuration {
				if _, ok := object.Lookup("duration").Float(); !ok {
					t.Fatal("HasDuration but duration missing from Object")
				}
			} else if !object.Lookup("duration").IsMissing() {
				t.Fatal("absent duration serialized")
			}
			if entry.HasTimestamp {
				if _, ok := object.Lookup("timestamp").Int(); !ok {
					t.Fatal("HasTimestamp but timestamp missing from Object")
				}
			} else if !object.Lookup("timestamp").IsMissing() {
				t.Fatal("absent timestamp serialized")
			}
			if entry.HasViewCount {
				if _, ok := object.Lookup("view_count").Int(); !ok {
					t.Fatal("HasViewCount but view_count missing from Object")
				}
			} else if !object.Lookup("view_count").IsMissing() {
				t.Fatal("absent view_count serialized")
			}
			if _, exists := seenID[entry.ID]; exists {
				t.Fatalf("duplicate id %q", entry.ID)
			}
			if _, exists := seenURL[entry.URL]; exists {
				t.Fatalf("duplicate url %q", entry.URL)
			}
			seenID[entry.ID] = struct{}{}
			seenURL[entry.URL] = struct{}{}
		}
	})
}

func FuzzTwitchClipsRangeQuery(f *testing.F) {
	for _, seed := range []string{
		"",
		"range=24hr",
		"range=7d",
		"range=30d",
		"range=all",
		"range=",
		"range=unknown",
		"filter=clips&range=all",
		"range=24hr&range=7d",
		"range=%zz",
		"range=%00",
		"range=%FF",
		"range=" + strings.Repeat("a", 64),
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, rawQuery string) {
		if len(rawQuery) > 1<<16 {
			t.Skip()
		}
		query, err := url.ParseQuery(rawQuery)
		if err != nil || !twitchQueryValuesSafe(query) {
			return
		}
		if !twitchChannelClipsPathQueryOK(query) {
			return
		}
		clips, ok := twitchClipsQueryFromValues(query)
		if !ok {
			t.Fatalf("safe query rejected: %q", rawQuery)
		}
		switch clips.filter {
		case "LAST_DAY", "LAST_WEEK", "LAST_MONTH", "ALL_TIME":
		default:
			t.Fatalf("unexpected filter %q from %q", clips.filter, rawQuery)
		}
		switch clips.label {
		case "Top 24H", "Top 7D", "Top 30D", "Top All":
		default:
			t.Fatalf("unexpected label %q from %q", clips.label, rawQuery)
		}
	})
}

func assertTwitchChannelClipsGraphQLRequest(t *testing.T, request twitchRecordedRequest, channel, filter, cursor string) {
	t.Helper()
	if got := request.header.Get("Client-ID"); got != twitchClientID {
		t.Fatalf("Client-ID = %q", got)
	}
	if got := request.header.Get("Content-Type"); got != "text/plain;charset=UTF-8" {
		t.Fatalf("Content-Type = %q", got)
	}
	var operations []struct {
		OperationName string `json:"operationName"`
		Variables     struct {
			Login    string `json:"login"`
			Limit    int    `json:"limit"`
			Cursor   string `json:"cursor"`
			Criteria struct {
				Filter string `json:"filter"`
			} `json:"criteria"`
		} `json:"variables"`
		Extensions struct {
			PersistedQuery struct {
				Version    int    `json:"version"`
				SHA256Hash string `json:"sha256Hash"`
			} `json:"persistedQuery"`
		} `json:"extensions"`
	}
	if err := json.Unmarshal(request.body, &operations); err != nil {
		t.Fatal(err)
	}
	if len(operations) != 1 {
		t.Fatalf("operations = %#v", operations)
	}
	operation := operations[0]
	if operation.OperationName != twitchClipsOperation ||
		operation.Extensions.PersistedQuery.Version != 1 ||
		operation.Extensions.PersistedQuery.SHA256Hash != twitchOperationHashes[twitchClipsOperation] {
		t.Fatalf("operation = %#v", operation)
	}
	if operation.Variables.Login != channel || operation.Variables.Criteria.Filter != filter || operation.Variables.Limit != twitchClipsPageLimit {
		t.Fatalf("variables = %#v", operation.Variables)
	}
	var raw []map[string]any
	if err := json.Unmarshal(request.body, &raw); err != nil {
		t.Fatal(err)
	}
	variables := raw[0]["variables"].(map[string]any)
	if cursor == "" {
		if _, present := variables["cursor"]; present {
			t.Fatalf("unexpected cursor in %#v", variables)
		}
	} else if got, _ := variables["cursor"].(string); got != cursor {
		t.Fatalf("cursor = %#v", variables["cursor"])
	}
}
