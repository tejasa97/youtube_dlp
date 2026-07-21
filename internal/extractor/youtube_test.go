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
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/ytdlp-go/ytdlp/internal/javascript/ejs"
	"github.com/ytdlp-go/ytdlp/internal/javascript/engine"
	"github.com/ytdlp-go/ytdlp/internal/value"
)

const (
	youtubeFixtureURL = "https://www.youtube.com/watch?v=fixture0001"
	youtubePlayerURL  = "https://www.youtube.com/s/player/fixture/base.js"
)

type memoryTransport struct {
	pages map[string][]byte
	reads []string
}

type youtubeFallbackTransport struct {
	*memoryTransport
	responses map[string][]byte
	requests  []*http.Request
	bodies    [][]byte
}

func (transport *youtubeFallbackTransport) Do(ctx context.Context, request *http.Request) (*http.Response, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	body, err := io.ReadAll(request.Body)
	if err != nil {
		return nil, err
	}
	transport.requests = append(transport.requests, request)
	transport.bodies = append(transport.bodies, body)
	response, ok := transport.responses[request.Header.Get("X-Youtube-Client-Name")]
	if !ok {
		return nil, fmt.Errorf("unexpected YouTube client %q", request.Header.Get("X-Youtube-Client-Name"))
	}
	return &http.Response{
		StatusCode: http.StatusOK, Body: io.NopCloser(bytes.NewReader(response)),
		Header: make(http.Header), Request: request,
	}, nil
}

func (transport *memoryTransport) Do(context.Context, *http.Request) (*http.Response, error) {
	return nil, errors.New("unexpected Do call")
}

func (transport *memoryTransport) ReadPage(_ context.Context, rawURL string) ([]byte, http.Header, error) {
	transport.reads = append(transport.reads, rawURL)
	page, ok := transport.pages[rawURL]
	if !ok {
		return nil, nil, fmt.Errorf("unexpected URL %q", rawURL)
	}
	return append([]byte(nil), page...), make(http.Header), nil
}

func TestYouTubeSuitableAndVideoID(t *testing.T) {
	extractor := NewYouTube()
	for _, rawURL := range []string{
		"https://www.youtube.com/watch?v=fixture0001",
		"https://youtu.be/fixture0001",
		"https://m.youtube.com/shorts/fixture0001",
		"https://youtube.com/embed/fixture0001",
		"https://youtube.com/live/fixture0001",
	} {
		parsed, err := url.Parse(rawURL)
		if err != nil || !extractor.Suitable(parsed) {
			t.Fatalf("Suitable(%q) = false, %v", rawURL, err)
		}
		if id, err := youtubeVideoID(rawURL); err != nil || id != "fixture0001" {
			t.Fatalf("youtubeVideoID(%q) = %q, %v", rawURL, id, err)
		}
	}
	if id, ok := youtubePlaylistID("https://www.youtube.com/playlist?list=PL_fixture"); !ok || id != "PL_fixture" {
		t.Fatalf("youtubePlaylistID() = %q, %v", id, ok)
	}
	parsed, _ := url.Parse("https://example.com/watch?v=fixture0001")
	if extractor.Suitable(parsed) {
		t.Fatal("non-YouTube host is suitable")
	}
	for _, rawURL := range []string{
		"https://www.youtube.com/watch?v=short",
		"https://www.youtube.com/playlist?list=fixture0001",
		"https://youtu.be/fixture0001/extra",
	} {
		if _, err := youtubeVideoID(rawURL); !errors.Is(err, ErrUnsupported) {
			t.Fatalf("youtubeVideoID(%q) error = %v", rawURL, err)
		}
	}
}

func TestYouTubeExtractsPinnedVideoAndSolvesChallenges(t *testing.T) {
	watch := readYouTubeFixture(t, "watch.html")
	player := readYouTubeFixture(t, "../../javascript/ejs-0.8.0/synthetic-player.js")
	expected := readYouTubeFixture(t, "expected.json")
	solver, err := ejs.New(engine.New(4))
	if err != nil {
		t.Fatal(err)
	}
	transport := &memoryTransport{pages: map[string][]byte{
		youtubeFixtureURL: watch,
		youtubePlayerURL:  player,
	}}
	result, err := NewYouTube().Extract(context.Background(), Request{
		URL: youtubeFixtureURL, Transport: transport, ChallengeSolver: solver,
	})
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
	if len(transport.reads) != 2 || transport.reads[0] != youtubeFixtureURL || transport.reads[1] != youtubePlayerURL {
		t.Fatalf("reads = %#v", transport.reads)
	}
}

func TestYouTubeDiscoversPlayerJavaScriptFromPageConfig(t *testing.T) {
	watch := bytes.Replace(
		readYouTubeFixture(t, "watch.html"),
		[]byte(`"assets": {"js": "/s/player/fixture/base.js"}`),
		[]byte(`"assets": {}, "webPlayerContextConfig": {"jsUrl": "\/s\/player\/fixture\/base.js"}`),
		1,
	)
	solver, err := ejs.New(engine.New(4))
	if err != nil {
		t.Fatal(err)
	}
	transport := &memoryTransport{pages: map[string][]byte{
		youtubeFixtureURL: watch,
		youtubePlayerURL:  readYouTubeFixture(t, "../../javascript/ejs-0.8.0/synthetic-player.js"),
	}}
	result, err := NewYouTube().Extract(context.Background(), Request{
		URL: youtubeFixtureURL, Transport: transport, ChallengeSolver: solver,
	})
	if err != nil {
		t.Fatal(err)
	}
	formats, _ := result.Info.Formats()
	if len(formats) != 4 || len(transport.reads) != 2 || transport.reads[1] != youtubePlayerURL {
		t.Fatalf("formats=%d reads=%v", len(formats), transport.reads)
	}
}

func TestYouTubeRecoversURLBearingFormatsFromNativeClient(t *testing.T) {
	transport := &youtubeFallbackTransport{
		memoryTransport: &memoryTransport{pages: map[string][]byte{
			youtubeFixtureURL: readYouTubeFixture(t, "sabr-watch.html"),
		}},
		responses: map[string][]byte{"3": readYouTubeFixture(t, "android-player.json")},
	}
	result, err := NewYouTube().Extract(context.Background(), Request{URL: youtubeFixtureURL, Transport: transport})
	if err != nil {
		t.Fatal(err)
	}
	if title, _ := result.Info.Lookup("title").StringValue(); title != "Synthetic SABR YouTube Video" {
		t.Fatalf("title = %q", title)
	}
	formats, _ := result.Info.Formats()
	if len(formats) != 1 {
		t.Fatalf("formats = %#v", formats)
	}
	format, _ := formats[0].Object()
	if rawURL, _ := format.Lookup("url").StringValue(); rawURL != "https://media.example/android-video.mp4" {
		t.Fatalf("format = %#v", format)
	}
	if len(transport.requests) != 1 {
		t.Fatalf("requests = %d", len(transport.requests))
	}
	request := transport.requests[0]
	if request.Method != http.MethodPost || request.URL.String() != youtubePlayerAPIURL ||
		request.Header.Get("X-Youtube-Client-Name") != "3" ||
		request.Header.Get("X-Youtube-Client-Version") != "21.26.364" ||
		request.Header.Get("User-Agent") == "" {
		t.Fatalf("request = %s %s headers=%v", request.Method, request.URL, request.Header)
	}
	var body struct {
		VideoID      string `json:"videoId"`
		ContentCheck bool   `json:"contentCheckOk"`
		RacyCheck    bool   `json:"racyCheckOk"`
		Context      struct {
			Client struct {
				Name    string `json:"clientName"`
				Version string `json:"clientVersion"`
			} `json:"client"`
		} `json:"context"`
	}
	if err := json.Unmarshal(transport.bodies[0], &body); err != nil || body.VideoID != "fixture0001" ||
		!body.ContentCheck || !body.RacyCheck || body.Context.Client.Name != "ANDROID" ||
		body.Context.Client.Version != "21.26.364" {
		t.Fatalf("body = %#v, error=%v", body, err)
	}
}

func TestYouTubeSABRFallbackFailureIsCategorizedAndCancelable(t *testing.T) {
	unavailable := []byte(`{"playabilityStatus":{"status":"LOGIN_REQUIRED","reason":"fixture"}}`)
	transport := &youtubeFallbackTransport{
		memoryTransport: &memoryTransport{pages: map[string][]byte{
			youtubeFixtureURL: readYouTubeFixture(t, "sabr-watch.html"),
		}},
		responses: map[string][]byte{"3": unavailable, "28": unavailable},
	}
	_, err := NewYouTube().Extract(context.Background(), Request{URL: youtubeFixtureURL, Transport: transport})
	if !errors.Is(err, ErrUnavailable) || len(transport.requests) != 2 {
		t.Fatalf("error=%v requests=%d", err, len(transport.requests))
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	transport.requests = nil
	_, err = NewYouTube().Extract(ctx, Request{URL: youtubeFixtureURL, Transport: transport})
	if !errors.Is(err, context.Canceled) || len(transport.requests) != 0 {
		t.Fatalf("cancellation error=%v requests=%d", err, len(transport.requests))
	}
}

func TestYouTubeRejectsMalformedNativeClientResponses(t *testing.T) {
	for name, response := range map[string][]byte{
		"invalid JSON": []byte(`{"playabilityStatus":`),
		"wrong video":  []byte(`{"playabilityStatus":{"status":"OK"},"videoDetails":{"videoId":"different01"},"streamingData":{"formats":[{"itag":18,"url":"https://media.example/video.mp4"}]}}`),
	} {
		t.Run(name, func(t *testing.T) {
			transport := &youtubeFallbackTransport{
				memoryTransport: &memoryTransport{pages: map[string][]byte{
					youtubeFixtureURL: readYouTubeFixture(t, "sabr-watch.html"),
				}},
				responses: map[string][]byte{
					"3":  response,
					"28": []byte(`{"playabilityStatus":{"status":"LOGIN_REQUIRED"}}`),
				},
			}
			_, err := NewYouTube().Extract(context.Background(), Request{URL: youtubeFixtureURL, Transport: transport})
			if !errors.Is(err, ErrInvalidMetadata) {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestYouTubeChallengeAndAvailabilityFailuresAreCategorized(t *testing.T) {
	watch := readYouTubeFixture(t, "watch.html")
	transport := &memoryTransport{pages: map[string][]byte{youtubeFixtureURL: watch}}
	_, err := NewYouTube().Extract(context.Background(), Request{URL: youtubeFixtureURL, Transport: transport})
	if !errors.Is(err, ErrChallengeSolver) {
		t.Fatalf("missing challenge solver error = %v", err)
	}

	for _, test := range []struct {
		status string
		want   error
	}{
		{"LOGIN_REQUIRED", ErrAuthentication},
		{"ERROR", ErrUnavailable},
	} {
		page := []byte(`ytInitialPlayerResponse = {"playabilityStatus":{"status":"` + test.status + `","reason":"fixture reason"}};`)
		transport := &memoryTransport{pages: map[string][]byte{youtubeFixtureURL: page}}
		_, err := NewYouTube().Extract(context.Background(), Request{URL: youtubeFixtureURL, Transport: transport})
		if !errors.Is(err, test.want) {
			t.Fatalf("status %s error = %v", test.status, err)
		}
	}
}

func TestYouTubeCanonicalizesShortURLsBeforeFetching(t *testing.T) {
	page := []byte(`ytInitialPlayerResponse = {"playabilityStatus":{"status":"ERROR","reason":"fixture reason"}};`)
	transport := &memoryTransport{pages: map[string][]byte{youtubeFixtureURL: page}}
	_, err := NewYouTube().Extract(context.Background(), Request{
		URL: "https://youtu.be/fixture0001", Transport: transport,
	})
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("error = %v", err)
	}
	if len(transport.reads) != 1 || transport.reads[0] != youtubeFixtureURL {
		t.Fatalf("reads = %#v", transport.reads)
	}
}

func TestYouTubeRejectsMalformedPlayerResponse(t *testing.T) {
	for _, page := range [][]byte{
		[]byte("no player marker"),
		[]byte("ytInitialPlayerResponse = {\"open\": true"),
		[]byte("ytInitialPlayerResponse = {not-json};"),
	} {
		transport := &memoryTransport{pages: map[string][]byte{youtubeFixtureURL: page}}
		_, err := NewYouTube().Extract(context.Background(), Request{URL: youtubeFixtureURL, Transport: transport})
		if !errors.Is(err, ErrInvalidMetadata) {
			t.Fatalf("page %q error = %v", page, err)
		}
	}
}

type youtubePlaylistTransport struct {
	page         []byte
	continuation []byte
	status       int
	reads        []string
	requests     int
}

func (transport *youtubePlaylistTransport) ReadPage(_ context.Context, rawURL string) ([]byte, http.Header, error) {
	transport.reads = append(transport.reads, rawURL)
	if rawURL != "https://www.youtube.com/playlist?list=PL_fixture" {
		return nil, nil, fmt.Errorf("unexpected URL %q", rawURL)
	}
	return append([]byte(nil), transport.page...), make(http.Header), nil
}

func (transport *youtubePlaylistTransport) Do(_ context.Context, request *http.Request) (*http.Response, error) {
	transport.requests++
	if request.Method != http.MethodPost || request.URL.Path != "/youtubei/v1/browse" ||
		request.URL.Query().Get("key") != "fixture-key" || request.URL.Query().Get("prettyPrint") != "false" ||
		request.Header.Get("X-Youtube-Client-Version") != youtubeDefaultClientVersion {
		return nil, fmt.Errorf("unexpected continuation request: %s %s headers=%v", request.Method, request.URL, request.Header)
	}
	body, err := io.ReadAll(request.Body)
	if err != nil || !strings.Contains(string(body), `"continuation":"fixture-token-2"`) || !strings.Contains(string(body), `"visitorData":"fixture-visitor"`) {
		return nil, fmt.Errorf("unexpected continuation body: %s: %v", body, err)
	}
	status := transport.status
	if status == 0 {
		status = http.StatusOK
	}
	return &http.Response{
		StatusCode: status, Body: io.NopCloser(bytes.NewReader(transport.continuation)),
		Header: make(http.Header), Request: request,
	}, nil
}

func TestYouTubePlaylistIsLazyPagedAndMatchesPinnedShape(t *testing.T) {
	transport := &youtubePlaylistTransport{
		page: readYouTubeFixture(t, "playlist.html"), continuation: readYouTubeFixture(t, "playlist-continuation.json"),
	}
	result, err := NewYouTube().Extract(context.Background(), Request{
		URL: "https://www.youtube.com/playlist?feature=share&list=PL_fixture", Transport: transport,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.IsPlaylist() || transport.requests != 0 || len(transport.reads) != 1 {
		t.Fatalf("result=%#v reads=%v requests=%d", result, transport.reads, transport.requests)
	}
	entries, err := CollectEntries(context.Background(), result.Entries, 10)
	if err != nil || transport.requests != 1 {
		t.Fatalf("entries=%#v error=%v requests=%d", entries, err, transport.requests)
	}
	info := value.NewInfo(result.Info.Fields().Clone())
	entryValues := make([]value.Value, len(entries))
	for index, entry := range entries {
		entryValues[index] = value.ObjectValue(entry.Object())
	}
	info.Set("entries", value.List(entryValues...))
	actual, err := json.Marshal(info.Fields())
	if err != nil {
		t.Fatal(err)
	}
	var actualDocument, expectedDocument any
	if err := json.Unmarshal(actual, &actualDocument); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(readYouTubeFixture(t, "playlist-expected.json"), &expectedDocument); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(actualDocument, expectedDocument) {
		t.Fatalf("playlist mismatch\nactual: %s\nexpected: %#v", actual, expectedDocument)
	}
}

func TestYouTubePlaylistFailuresAreCategorized(t *testing.T) {
	for _, test := range []struct {
		name  string
		alert string
		want  error
	}{
		{"private", "This playlist is private. Sign in to continue.", ErrAuthentication},
		{"unavailable", "The playlist does not exist.", ErrUnavailable},
	} {
		t.Run(test.name, func(t *testing.T) {
			page := []byte(`ytInitialData={"metadata":{"playlistMetadataRenderer":{"title":"Fixture"}},"alerts":[{"alertRenderer":{"text":{"simpleText":` + strconv.Quote(test.alert) + `}}}]};`)
			transport := &youtubePlaylistTransport{page: page}
			_, err := NewYouTube().Extract(context.Background(), Request{URL: "https://www.youtube.com/playlist?list=PL_fixture", Transport: transport})
			if !errors.Is(err, test.want) {
				t.Fatalf("error = %v", err)
			}
		})
	}
	transport := &youtubePlaylistTransport{page: []byte(`ytInitialData={"contents":{}};`)}
	if _, err := NewYouTube().Extract(context.Background(), Request{URL: "https://www.youtube.com/playlist?list=PL_fixture", Transport: transport}); !errors.Is(err, ErrInvalidMetadata) {
		t.Fatalf("malformed error = %v", err)
	}
	transport = &youtubePlaylistTransport{
		page: readYouTubeFixture(t, "playlist.html"), continuation: []byte(`{}`), status: http.StatusForbidden,
	}
	result, err := NewYouTube().Extract(context.Background(), Request{URL: "https://www.youtube.com/playlist?list=PL_fixture", Transport: transport})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := CollectEntries(context.Background(), result.Entries, 10); !errors.Is(err, ErrAuthentication) {
		t.Fatalf("continuation auth error = %v", err)
	}
}

func TestYouTubePlaylistTraversalDepthIsBounded(t *testing.T) {
	data := strings.Repeat(`{"x":`, youtubeMaxJSONDepth+2) + `{}` + strings.Repeat(`}`, youtubeMaxJSONDepth+2)
	if _, err := parseYouTubePlaylistData([]byte(data)); !errors.Is(err, ErrInvalidMetadata) {
		t.Fatalf("depth error = %v", err)
	}
}

func TestYouTubeExtractsLiveHLSAndClassifiesLiveStates(t *testing.T) {
	liveURL := "https://www.youtube.com/watch?v=livefix0001"
	transport := &memoryTransport{pages: map[string][]byte{liveURL: readYouTubeFixture(t, "live-watch.html")}}
	result, err := NewYouTube().Extract(context.Background(), Request{URL: liveURL, Transport: transport})
	if err != nil {
		t.Fatal(err)
	}
	if status, _ := result.Info.Lookup("live_status").StringValue(); status != "is_live" {
		t.Fatalf("live_status = %q", status)
	}
	formats, _ := result.Info.Formats()
	format, _ := formats[0].Object()
	if protocol, _ := format.Lookup("protocol").StringValue(); protocol != "m3u8_native" {
		t.Fatalf("live format = %#v", format)
	}
	trueValue, falseValue := true, false
	for _, test := range []struct {
		details youtubeVideoDetails
		want    string
	}{
		{youtubeVideoDetails{IsPostLiveDVR: true}, "post_live"},
		{youtubeVideoDetails{IsUpcoming: true}, "is_upcoming"},
		{youtubeVideoDetails{IsLiveContent: &trueValue}, "was_live"},
		{youtubeVideoDetails{IsLive: &falseValue}, "not_live"},
		{youtubeVideoDetails{}, ""},
	} {
		if got := youtubeLiveStatus(test.details); got != test.want {
			t.Fatalf("youtubeLiveStatus(%#v) = %q, want %q", test.details, got, test.want)
		}
	}
}

func FuzzParseYouTubePlaylistData(f *testing.F) {
	page := readYouTubeFixture(f, "playlist.html")
	if initial, err := extractJSONObject(page, youtubeInitialDataMarker); err == nil {
		f.Add(initial)
	}
	f.Add(readYouTubeFixture(f, "playlist-continuation.json"))
	f.Add([]byte(`{"metadata":{"playlistMetadataRenderer":{"title":"x"}}}`))
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 1<<20 {
			t.Skip()
		}
		_, _ = parseYouTubePlaylistData(data)
	})
}

func FuzzDiscoverYouTubePlayerPath(f *testing.F) {
	f.Add([]byte(`{"PLAYER_JS_URL":"\/s\/player\/fixture\/base.js"}`))
	f.Add([]byte(`{"jsUrl":"https://www.youtube.com/s/player/fixture/base.js"}`))
	f.Add([]byte(`{"PLAYER_JS_URL":"unterminated}`))
	f.Fuzz(func(t *testing.T, page []byte) {
		if len(page) > 1<<20 {
			t.Skip()
		}
		_ = discoverYouTubePlayerPath(page, "")
	})
}

type youtubeTestHelper interface {
	Helper()
	Fatal(...any)
}

func readYouTubeFixture(t youtubeTestHelper, name string) []byte {
	t.Helper()
	data, err := os.ReadFile("../../conformance/extractors/youtube/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return data
}
