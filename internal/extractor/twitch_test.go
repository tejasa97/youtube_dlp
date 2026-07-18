package extractor

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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
)

const twitchFixtureRoot = "../../conformance/extractors/twitch"

type twitchRecordedRequest struct {
	header http.Header
	body   []byte
}

type twitchFixtureTransport struct {
	mu              sync.Mutex
	metadata        []byte
	token           []byte
	metadataStatus  int
	tokenStatus     int
	graphQLRequests []twitchRecordedRequest
	graphQLFixtures []twitchGraphQLFixture
	mediaPolls      int
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
		index := len(transport.graphQLRequests)
		transport.graphQLRequests = append(transport.graphQLRequests, twitchRecordedRequest{header: request.Header.Clone(), body: body})
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
		{"https://www.twitch.tv/directory", false},
		{"https://www.twitch.tv/channel/clips", false},
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
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, rawURL string) {
		if len(rawURL) > 1<<20 {
			t.Skip()
		}
		parsed, err := url.Parse(rawURL)
		if err == nil {
			_, _ = classifyTwitchURL(parsed)
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
