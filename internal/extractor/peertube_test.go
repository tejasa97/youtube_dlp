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
	"strings"
	"sync"
	"testing"

	"github.com/ytdlp-go/ytdlp/internal/value"
)

const peerTubeFixtureID = "AbCdEfGhIjKlMnOpQrStUv"

type peerTubeFixtureResponse struct {
	status int
	body   []byte
	err    error
}

type peerTubeFixtureTransport struct {
	mu        sync.Mutex
	responses map[string]peerTubeFixtureResponse
	requests  []string
}

func (transport *peerTubeFixtureTransport) ReadPage(context.Context, string) ([]byte, http.Header, error) {
	return nil, nil, errors.New("unexpected PeerTube page request")
}

func (transport *peerTubeFixtureTransport) Do(ctx context.Context, request *http.Request) (*http.Response, error) {
	if err := contextError(ctx); err != nil {
		return nil, err
	}
	transport.mu.Lock()
	defer transport.mu.Unlock()
	transport.requests = append(transport.requests, request.Method+" "+request.URL.String())
	response, ok := transport.responses[request.URL.String()]
	if !ok {
		return nil, errors.New("fixture network endpoint missing")
	}
	if response.err != nil {
		return nil, response.err
	}
	if request.Method != http.MethodGet || request.Header.Get("Accept") != "application/json" {
		return nil, errors.New("invalid fixture request")
	}
	status := response.status
	if status == 0 {
		status = http.StatusOK
	}
	return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(bytes.NewReader(response.body)), Request: request}, nil
}

func TestPeerTubeExtractsFederatedMetadataFormatsCaptionsAndLiveState(t *testing.T) {
	base := "https://peertube.example.test/api/v1/videos/" + peerTubeFixtureID
	transport := &peerTubeFixtureTransport{responses: map[string]peerTubeFixtureResponse{
		base:                  {body: peerTubeFixture(t, "video.json")},
		base + "/description": {body: peerTubeFixture(t, "description.json")},
		base + "/captions":    {body: peerTubeFixture(t, "captions.json")},
	}}
	result, err := NewPeerTube().Extract(context.Background(), Request{
		URL:       "https://peertube.example.test/w/" + peerTubeFixtureID + "?start=10s",
		Transport: transport,
	})
	if err != nil {
		t.Fatal(err)
	}
	projection := peerTubeProjection(t, result)
	var expected map[string]any
	if err := json.Unmarshal(peerTubeFixture(t, "expected.json"), &expected); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(projection, expected) {
		actual, _ := json.Marshal(projection)
		t.Fatalf("PeerTube normalization mismatch\nactual: %s\nexpected: %s", actual, peerTubeFixture(t, "expected.json"))
	}
	formats, ok := result.Info.Formats()
	if !ok {
		t.Fatal("formats are absent")
	}
	formatByID := make(map[string]*value.Object)
	for _, rawFormat := range formats {
		format, _ := rawFormat.Object()
		formatID, _ := format.Lookup("format_id").StringValue()
		formatByID[formatID] = format
	}
	if protocol, _ := formatByID["hls-1"].Lookup("protocol").StringValue(); protocol != "m3u8_native" {
		t.Fatalf("HLS protocol = %q", protocol)
	}
	if codec, _ := formatByID["0p"].Lookup("vcodec").StringValue(); codec != "none" {
		t.Fatalf("audio-only vcodec = %q", codec)
	}
	if height, _ := formatByID["720p"].Lookup("height").Int(); height != 720 {
		t.Fatalf("720p height = %d", height)
	}
	subtitles, ok := result.Info.Lookup("subtitles").Object()
	if !ok {
		t.Fatal("subtitles object is absent")
	}
	english, _ := subtitles.Lookup("en").ListValue()
	caption, _ := english[0].Object()
	if captionURL, _ := caption.Lookup("url").StringValue(); captionURL != "https://peertube.example.test/lazy-static/video-captions/fixture-en.vtt" {
		t.Fatalf("resolved caption URL = %q", captionURL)
	}
	if got, want := transport.requests, []string{
		"GET " + base,
		"GET " + base + "/description",
		"GET " + base + "/captions",
	}; !reflect.DeepEqual(got, want) {
		t.Fatalf("requests = %#v, want %#v", got, want)
	}
}

func TestPeerTubeRoutingIsConservativeAndFederationExplicit(t *testing.T) {
	accepted := []string{
		"https://peertube.example.test/videos/watch/" + peerTubeFixtureID,
		"https://peertube2.example.test/videos/embed/9c9de5e8-0a1e-484a-b099-e80766180a6d",
		"https://framatube.org/w/" + peerTubeFixtureID,
		"https://peertube.example.test/api/v1/videos/" + peerTubeFixtureID,
		"peertube:videos.federated.example:" + peerTubeFixtureID,
	}
	for _, rawURL := range accepted {
		parsed, err := url.Parse(rawURL)
		if err != nil || !NewPeerTube().Suitable(parsed) {
			t.Errorf("Suitable(%q) = false (parse=%v)", rawURL, err)
		}
	}
	rejected := []string{
		"https://video.unrecognized.example/videos/watch/" + peerTubeFixtureID,
		"https://peertube.example.test/videos/watch/not-an-id",
		"https://user:password@peertube.example.test/videos/watch/" + peerTubeFixtureID,
		"https://peertube.example.test:8443/videos/watch/" + peerTubeFixtureID,
		"https://peertube.example.test/videos/watch/" + peerTubeFixtureID + "/extra",
		"https://peertube.example.test/videos/watch/" + peerTubeFixtureID + "#fragment",
		"peertube:localhost:" + peerTubeFixtureID,
		"peertube:127.0.0.1:" + peerTubeFixtureID,
		"peertube:metadata.google.internal:" + peerTubeFixtureID,
		"peertube:instance.local:" + peerTubeFixtureID,
	}
	for _, rawURL := range rejected {
		parsed, err := url.Parse(rawURL)
		if err == nil && NewPeerTube().Suitable(parsed) {
			t.Errorf("Suitable(%q) = true", rawURL)
		}
	}
}

func TestPeerTubeErrorsAreCategorizedAndSecretSafe(t *testing.T) {
	base := "https://peertube.example.test/api/v1/videos/" + peerTubeFixtureID
	tests := []struct {
		name   string
		status int
		body   string
		want   error
	}{
		{"auth", http.StatusUnauthorized, `{"error":"token=must-not-leak"}`, ErrAuthentication},
		{"forbidden", http.StatusForbidden, `{"error":"private token=must-not-leak"}`, ErrAuthentication},
		{"geo", http.StatusForbidden, `{"error":"geo blocked token=must-not-leak"}`, ErrRegionRestricted},
		{"legal", http.StatusUnavailableForLegalReasons, `token=must-not-leak`, ErrRegionRestricted},
		{"missing", http.StatusNotFound, `token=must-not-leak`, ErrUnavailable},
		{"server", http.StatusInternalServerError, `token=must-not-leak`, ErrPeerTubeNetwork},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			transport := &peerTubeFixtureTransport{responses: map[string]peerTubeFixtureResponse{base: {status: test.status, body: []byte(test.body)}}}
			_, err := NewPeerTube().Extract(context.Background(), Request{URL: "peertube:peertube.example.test:" + peerTubeFixtureID, Transport: transport})
			if !errors.Is(err, test.want) || strings.Contains(fmt.Sprint(err), "must-not-leak") {
				t.Fatalf("error = %v, want %v without response secret", err, test.want)
			}
		})
	}

	transport := &peerTubeFixtureTransport{responses: map[string]peerTubeFixtureResponse{base: {err: errors.New("dial secret=must-not-leak")}}}
	_, err := NewPeerTube().Extract(context.Background(), Request{URL: "peertube:peertube.example.test:" + peerTubeFixtureID, Transport: transport})
	if !errors.Is(err, ErrPeerTubeNetwork) || strings.Contains(fmt.Sprint(err), "must-not-leak") {
		t.Fatalf("network error = %v", err)
	}
	transport.responses[base] = peerTubeFixtureResponse{body: []byte(`{"name":`)}
	_, err = NewPeerTube().Extract(context.Background(), Request{URL: "peertube:peertube.example.test:" + peerTubeFixtureID, Transport: transport})
	if !errors.Is(err, ErrInvalidMetadata) {
		t.Fatalf("malformed error = %v", err)
	}
	transport.responses[base] = peerTubeFixtureResponse{body: []byte(`{"name":"No files"}`)}
	_, err = NewPeerTube().Extract(context.Background(), Request{URL: "peertube:peertube.example.test:" + peerTubeFixtureID, Transport: transport})
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("unavailable error = %v", err)
	}
	transport.responses[base] = peerTubeFixtureResponse{body: []byte(`{"files":[{"fileUrl":"https://media.example.test/video.mp4"}]}`)}
	_, err = NewPeerTube().Extract(context.Background(), Request{URL: "peertube:peertube.example.test:" + peerTubeFixtureID, Transport: transport})
	if !errors.Is(err, ErrInvalidMetadata) {
		t.Fatalf("missing title error = %v", err)
	}
}

func TestPeerTubeCancellationOptionalCaptionsAndAssetTrust(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := NewPeerTube().Extract(ctx, Request{URL: "peertube:peertube.example.test:" + peerTubeFixtureID, Transport: &peerTubeFixtureTransport{}})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation error = %v", err)
	}

	base := "https://peertube.example.test/api/v1/videos/" + peerTubeFixtureID
	minimal := []byte(`{"name":"Usable without captions","files":[{"fileUrl":"https://media.example.test/video.mp4","resolution":{"label":"720p"}}]}`)
	transport := &peerTubeFixtureTransport{responses: map[string]peerTubeFixtureResponse{
		base:               {body: minimal},
		base + "/captions": {status: http.StatusInternalServerError, body: []byte(`secret=not-returned`)},
	}}
	if _, err := NewPeerTube().Extract(context.Background(), Request{URL: "peertube:peertube.example.test:" + peerTubeFixtureID, Transport: transport}); err != nil {
		t.Fatalf("optional captions failed extraction: %v", err)
	}

	video := peerTubeVideo{Name: "Unsafe assets"}
	video.Files = []peerTubeFile{{FileURL: "http://127.0.0.1/private.mp4"}, {FileURL: "https://metadata.google.internal/private.mp4"}, {FileURL: "https://media.example.test:8443/private.mp4"}}
	if _, err := normalizePeerTube(peerTubeTarget{host: "peertube.example.test", id: peerTubeFixtureID}, video, peerTubeCaptions{}); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("unsafe assets error = %v", err)
	}
	video.Files = make([]peerTubeFile, peerTubeMaxFiles+1)
	if _, err := normalizePeerTube(peerTubeTarget{host: "peertube.example.test", id: peerTubeFixtureID}, video, peerTubeCaptions{}); !errors.Is(err, ErrInvalidMetadata) {
		t.Fatalf("file bound error = %v", err)
	}
}

func FuzzPeerTubeRouting(f *testing.F) {
	for _, seed := range []string{
		"https://peertube.example.test/w/" + peerTubeFixtureID,
		"https://framatube.org/videos/watch/9c9de5e8-0a1e-484a-b099-e80766180a6d",
		"peertube:videos.example.test:" + peerTubeFixtureID,
		"https://evil.example/w/" + peerTubeFixtureID,
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, rawURL string) {
		if len(rawURL) > 1<<20 {
			t.Skip()
		}
		parsed, err := url.Parse(rawURL)
		if err == nil {
			_ = NewPeerTube().Suitable(parsed)
		}
	})
}

func FuzzPeerTubeMetadata(f *testing.F) {
	f.Add(peerTubeFixture(f, "video.json"))
	f.Add([]byte(`{"name":"x","files":[{"fileUrl":"https://media.example.test/x.mp4"}]}`))
	f.Add([]byte(`{"name":"x","streamingPlaylists":[{"playlistUrl":"https://media.example.test/x.m3u8"}]}`))
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 1<<20 {
			t.Skip()
		}
		var video peerTubeVideo
		if json.Unmarshal(data, &video) == nil {
			_, _ = normalizePeerTube(peerTubeTarget{host: "peertube.example.test", id: peerTubeFixtureID}, video, peerTubeCaptions{})
		}
	})
}

func peerTubeProjection(t *testing.T, result Extraction) map[string]any {
	t.Helper()
	encoded, err := json.Marshal(result.Info.Fields())
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err := json.Unmarshal(encoded, &fields); err != nil {
		t.Fatal(err)
	}
	formats, ok := fields["formats"].([]any)
	if !ok {
		t.Fatal("formats are absent")
	}
	formatIDs := make([]string, 0, len(formats))
	for _, raw := range formats {
		format, ok := raw.(map[string]any)
		if !ok {
			t.Fatal("invalid format object")
		}
		formatIDs = append(formatIDs, format["format_id"].(string))
	}
	delete(fields, "formats")
	fields["format_ids"] = formatIDs
	subtitles, ok := fields["subtitles"].(map[string]any)
	if !ok {
		t.Fatal("subtitles are absent")
	}
	languages := make([]string, 0, len(subtitles))
	for language := range subtitles {
		languages = append(languages, language)
	}
	sort.Strings(languages)
	delete(fields, "subtitles")
	fields["subtitle_languages"] = languages
	// Normalize concrete slices back through JSON so the comparison has the
	// same JSON-domain types as the expected fixture.
	normalized, err := json.Marshal(fields)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(normalized, &fields); err != nil {
		t.Fatal(err)
	}
	return fields
}

type peerTubeTestHelper interface {
	Helper()
	Fatal(...any)
}

func peerTubeFixture(t peerTubeTestHelper, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "conformance", "extractors", "shared", "peertube", name))
	if err != nil {
		t.Fatal(err)
	}
	return data
}
