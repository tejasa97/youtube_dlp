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
	mediaPolls      int
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
		{"https://www.twitch.tv/videos/123", false},
		{"https://www.twitch.tv/directory", false},
		{"https://www.twitch.tv/channel/clips", false},
		{"https://www.twitch.tv/channel//", false},
		{"https://player.twitch.tv/?video=v123", false},
		{"ftp://www.twitch.tv/channel", false},
		{"https://example.com/channel", false},
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
	_, err := NewTwitch().Extract(ctx, Request{URL: "https://twitch.tv/fixture_channel", Transport: &twitchFixtureTransport{}})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Extract() error = %v, want context.Canceled", err)
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

func FuzzTwitchMetadataResponse(f *testing.F) {
	f.Add(twitchFixture(f, "metadata.json"))
	f.Add([]byte(`[{"data":{"user":null}},{"data":{}},{"data":{}}]`))
	f.Add([]byte(`{"malformed":`))
	f.Fuzz(func(t *testing.T, body []byte) {
		transport := &twitchFixtureTransport{metadata: body}
		_, _ = requestTwitchMetadata(context.Background(), transport, "fixture_channel")
	})
}
