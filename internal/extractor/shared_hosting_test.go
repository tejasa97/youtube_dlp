package extractor

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/ytdlp-go/ytdlp/internal/value"
)

const sharedFixtureRoot = "../../conformance/extractors/shared"

func sharedFixture(t testing.TB, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(sharedFixtureRoot, name))
	if err != nil {
		t.Fatal(err)
	}
	return data
}

type sharedFixtureTransport struct {
	mu        sync.Mutex
	pages     map[string][]byte
	responses map[string]fixtureHTTP
	requests  []string
}
type fixtureHTTP struct {
	status int
	body   []byte
}

func (transport *sharedFixtureTransport) ReadPage(ctx context.Context, rawURL string) ([]byte, http.Header, error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	transport.mu.Lock()
	defer transport.mu.Unlock()
	transport.requests = append(transport.requests, "PAGE "+rawURL)
	if page, ok := transport.pages[rawURL]; ok {
		return append([]byte(nil), page...), make(http.Header), nil
	}
	return nil, nil, errors.New("unexpected fixture page")
}
func (transport *sharedFixtureTransport) Do(ctx context.Context, request *http.Request) (*http.Response, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	transport.mu.Lock()
	defer transport.mu.Unlock()
	transport.requests = append(transport.requests, request.Method+" "+request.URL.String())
	response, ok := transport.responses[request.URL.String()]
	if !ok {
		return nil, errors.New("unexpected fixture request")
	}
	status := response.status
	if status == 0 {
		status = http.StatusOK
	}
	return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(bytes.NewReader(response.body)), Request: request}, nil
}

func TestSharedHostingRoutes(t *testing.T) {
	t.Parallel()
	tests := []struct {
		extractor Extractor
		rawURL    string
		want      bool
	}{
		{NewBrightcove(), "https://players.brightcove.net/12345/default_default/index.html?videoId=123", true},
		{NewBrightcove(), "https://players.brightcove.net/12345/default_default/index.html?playlistId=123", true},
		{NewBrightcove(), "https://players.brightcove.net/12345/default_default/index.html?videoId=1&playlistId=2", false},
		{NewKaltura(), "kaltura:123:1_abcd1234", true},
		{NewKaltura(), "https://www.kaltura.com/index.php/kwidget/wid/_123/uiconf_id/1/entry_id/1_abcd1234", true},
		{NewJWPlatform(), "https://cdn.jwplayer.com/players/AbCd1234-ABCDEFGHI.js", true},
		{NewJWPlatform(), "jwplatform:AbCd1234", true},
		{NewWistia(), "https://fast.wistia.net/embed/iframe/a1b2c3d4e5", true},
		{NewWistia(), "wistia:a1b2c3d4e5", true},
		{NewSproutVideo(), "https://videos.sproutvideo.com/embed/4abcdef1234567890a/0abcdef1234567890", true},
		{NewSproutVideo(), "https://videos.sproutvideo.com/embed/not-hex/token", false},
	}
	for _, test := range tests {
		parsed, err := url.Parse(test.rawURL)
		if err != nil {
			t.Fatal(err)
		}
		if got := test.extractor.Suitable(parsed); got != test.want {
			t.Errorf("%s Suitable(%q)=%t, want %t", test.extractor.Name(), test.rawURL, got, test.want)
		}
	}
}

func TestSharedHostingSuccessFixtures(t *testing.T) {
	brightURL := "https://players.brightcove.net/12345/default_default/index.html?videoId=123"
	brightConfig := "https://players.brightcove.net/12345/default_default/config.json"
	brightAPI := "https://edge.api.brightcove.com/playback/v1/accounts/12345/videos/123"
	transport := &sharedFixtureTransport{responses: map[string]fixtureHTTP{
		brightConfig: {body: sharedFixture(t, "brightcove.json")},
		brightAPI:    {body: []byte(`{"id":"123","name":"Brightcove Fixture","duration":12000,"sources":[{"src":"https://media.example/bc/master.m3u8","type":"application/x-mpegURL"},{"src":"https://media.example/bc/video.mp4","height":720,"avg_bitrate":1500000}]}`)},
		"https://cdn.jwplayer.com/v2/media/AbCd1234":             {body: sharedFixture(t, "jwplatform.json")},
		"https://fast.wistia.net/embed/medias/a1b2c3d4e5.json":   {body: sharedFixture(t, "wistia.json")},
		"https://cdnapi.kaltura.com/api_v3/service/multirequest": {body: sharedFixture(t, "kaltura.json")},
	}, pages: map[string][]byte{"https://videos.sproutvideo.com/embed/4abcdef1234567890a/0abcdef1234567890": sharedFixture(t, "sproutvideo.html")}}
	tests := []struct {
		name           string
		extractor      Extractor
		rawURL, wantID string
	}{
		{"brightcove", NewBrightcove(), brightURL, "123"},
		{"kaltura", NewKaltura(), "kaltura:123:1_abcd1234", "1_abcd1234"},
		{"jwplatform", NewJWPlatform(), "https://cdn.jwplayer.com/players/AbCd1234-ABCDEFGHI.js", "AbCd1234"},
		{"wistia", NewWistia(), "wistia:a1b2c3d4e5", "a1b2c3d4e5"},
		{"sproutvideo", NewSproutVideo(), "https://videos.sproutvideo.com/embed/4abcdef1234567890a/0abcdef1234567890", "4abcdef1234567890a"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result, err := test.extractor.Extract(context.Background(), Request{URL: test.rawURL, Transport: transport})
			if err != nil {
				t.Fatal(err)
			}
			if id, ok := result.Info.ID(); !ok || id != test.wantID {
				t.Fatalf("id=%q %t, want %q", id, ok, test.wantID)
			}
			formats, ok := result.Info.Formats()
			if !ok || len(formats) == 0 {
				t.Fatal("missing usable formats")
			}
			if !sharedHasProtocol(formats, "m3u8_native") {
				t.Fatal("fixture did not expose a usable HLS format")
			}
		})
	}
}

func TestSharedHostingErrorsAreCategorizedAndSecretSafe(t *testing.T) {
	brightURL := "https://players.brightcove.net/12345/default_default/index.html?videoId=123"
	transport := &sharedFixtureTransport{responses: map[string]fixtureHTTP{
		"https://players.brightcove.net/12345/default_default/config.json":      {body: sharedFixture(t, "brightcove.json")},
		"https://edge.api.brightcove.com/playback/v1/accounts/12345/videos/123": {body: []byte(`{"errors":[{"error_subcode":"CLIENT_GEO"}]}`)},
	}}
	if _, err := NewBrightcove().Extract(context.Background(), Request{URL: brightURL, Transport: transport}); !errors.Is(err, ErrRegionRestricted) {
		t.Fatalf("geo error=%v", err)
	}
	statusTransport := &sharedFixtureTransport{responses: map[string]fixtureHTTP{"https://fixture.invalid/": {status: http.StatusUnauthorized, body: []byte("token=must-not-leak")}}}
	var target map[string]any
	err := hostedRequestJSON(context.Background(), statusTransport, http.MethodGet, "https://fixture.invalid/", nil, make(http.Header), &target)
	if !errors.Is(err, ErrAuthentication) || strings.Contains(err.Error(), "must-not-leak") {
		t.Fatalf("auth error leaks or wrong category: %v", err)
	}
	geoTransport := &sharedFixtureTransport{responses: map[string]fixtureHTTP{"https://geo.invalid/": {status: http.StatusForbidden, body: []byte(`{"error_subcode":"CLIENT_GEO","token":"must-not-leak"}`)}}}
	if err := hostedRequestJSON(context.Background(), geoTransport, http.MethodGet, "https://geo.invalid/", nil, make(http.Header), &target); !errors.Is(err, ErrRegionRestricted) || strings.Contains(err.Error(), "must-not-leak") {
		t.Fatalf("403 geo error leaks or wrong category: %v", err)
	}
	if _, err := parseSproutVideoPage([]byte("<html>password required</html>"), "4abcdef1234567890a", "https://example.invalid"); !errors.Is(err, ErrAuthentication) {
		t.Fatalf("sprout auth=%v", err)
	}
	if _, err := normalizeJWPlatform(jwPlatformResponse{}, "AbCd1234", "https://example.invalid"); !errors.Is(err, ErrInvalidMetadata) {
		t.Fatalf("jw malformed=%v", err)
	}
	if _, err := normalizeWistiaMedia(wistiaEmbedResponse{}, "a1b2c3d4e5", "wistia:a1b2c3d4e5"); !errors.Is(err, ErrInvalidMetadata) {
		t.Fatalf("wistia malformed=%v", err)
	}
	if _, err := normalizeBrightcoveMedia(brightcoveMedia{ID: "123", Name: "no sources"}, "123", brightURL, make(http.Header)); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("brightcove unavailable=%v", err)
	}
	if _, err := normalizeKalturaMedia(kalturaEntry{}, nil, nil, kalturaTarget{entryID: "1_abcd1234"}); !errors.Is(err, ErrInvalidMetadata) {
		t.Fatalf("kaltura malformed=%v", err)
	}
}

func TestSharedHostingCancellationAndLazyPlaylists(t *testing.T) {
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	transport := &sharedFixtureTransport{}
	for _, test := range []struct {
		extractor Extractor
		rawURL    string
	}{
		{NewBrightcove(), "https://players.brightcove.net/12345/default_default/index.html?videoId=123"},
		{NewKaltura(), "kaltura:123:1_abcd1234"},
		{NewJWPlatform(), "jwplatform:AbCd1234"},
		{NewWistia(), "wistia:a1b2c3d4e5"},
		{NewSproutVideo(), "https://videos.sproutvideo.com/embed/4abcdef1234567890a/0abcdef1234567890"},
	} {
		if _, err := test.extractor.Extract(canceled, Request{URL: test.rawURL, Transport: transport}); !errors.Is(err, context.Canceled) {
			t.Fatalf("%s cancellation=%v", test.extractor.Name(), err)
		}
	}
	playlist, err := normalizeWistiaChannel(wistiaChannelResponse{Series: []struct {
		Title       string `json:"title"`
		Description string `json:"description"`
		Sections    []struct {
			Videos []struct {
				HashedID string `json:"hashedId"`
				Name     string `json:"name"`
			} `json:"videos"`
			Episodes []struct {
				HashedID string `json:"hashedId"`
				Name     string `json:"name"`
			} `json:"episodes"`
		} `json:"sections"`
	}{{Title: "Fixture", Sections: []struct {
		Videos []struct {
			HashedID string `json:"hashedId"`
			Name     string `json:"name"`
		} `json:"videos"`
		Episodes []struct {
			HashedID string `json:"hashedId"`
			Name     string `json:"name"`
		} `json:"episodes"`
	}{{Videos: []struct {
		HashedID string `json:"hashedId"`
		Name     string `json:"name"`
	}{{HashedID: "a1b2c3d4e5", Name: "one"}}}}}}}, wistiaTarget{id: "z1x2c3v4b5", canonical: "wistiachannel:z1x2c3v4b5"})
	if err != nil || !playlist.IsPlaylist() {
		t.Fatalf("playlist=%#v err=%v", playlist, err)
	}
	iterator := playlist.Entries.Iterator()
	entry, ok, err := iterator.Next(context.Background())
	if err != nil || !ok || entry.URL != "wistia:a1b2c3d4e5" {
		t.Fatalf("lazy entry=%#v ok=%t err=%v", entry, ok, err)
	}
}

func sharedHasProtocol(formats []value.Value, protocol string) bool {
	for _, format := range formats {
		object, ok := format.Object()
		if !ok {
			continue
		}
		if got, ok := object.Lookup("protocol").StringValue(); ok && got == protocol {
			return true
		}
	}
	return false
}

func FuzzParseHostedEmbedPayloads(f *testing.F) {
	f.Add([]byte(`{"media":{"hashedId":"a1b2c3d4e5","name":"x"}}`))
	f.Add(sharedFixture(f, "sproutvideo.html"))
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 1<<20 {
			t.Skip()
		}
		var response wistiaEmbedResponse
		if json.Unmarshal(data, &response) == nil {
			_, _ = normalizeWistiaMedia(response, "a1b2c3d4e5", "wistia:a1b2c3d4e5")
		}
		_, _ = parseSproutVideoPage(data, "4abcdef1234567890a", "https://example.invalid")
		_, _ = extractJSONObjectAfter(data, kalturaPackageData)
	})
}
