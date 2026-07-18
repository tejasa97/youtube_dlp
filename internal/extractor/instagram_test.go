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
)

const riskFixtureRoot = "../../conformance/extractors/risk"

type riskFixtureResponse struct {
	status int
	body   []byte
}

type riskFixtureTransport struct {
	mu        sync.Mutex
	responses map[string]riskFixtureResponse
	pages     map[string][]byte
	handler   func(context.Context, *http.Request) (*http.Response, error)
	profile   string
	requests  []string
	wait      bool
}

func (transport *riskFixtureTransport) Do(ctx context.Context, request *http.Request) (*http.Response, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	transport.mu.Lock()
	transport.requests = append(transport.requests, request.Method+" "+request.URL.String())
	handler := transport.handler
	response, ok := transport.responses[request.Method+" "+request.URL.String()]
	transport.mu.Unlock()
	if transport.wait {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	if handler != nil {
		return handler(ctx, request)
	}
	if !ok {
		return riskHTTPResponse(http.StatusNotFound, nil), nil
	}
	status := response.status
	if status == 0 {
		status = http.StatusOK
	}
	return riskHTTPResponse(status, response.body), nil
}

func (transport *riskFixtureTransport) ReadPage(ctx context.Context, rawURL string) ([]byte, http.Header, error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	transport.mu.Lock()
	transport.requests = append(transport.requests, "GET "+rawURL)
	body, ok := transport.pages[rawURL]
	transport.mu.Unlock()
	if transport.wait {
		<-ctx.Done()
		return nil, nil, ctx.Err()
	}
	if !ok {
		return nil, nil, &HTTPStatusError{Code: http.StatusNotFound}
	}
	return append([]byte(nil), body...), make(http.Header), nil
}

func (transport *riskFixtureTransport) DoProfile(ctx context.Context, request *http.Request, profile string) (*http.Response, error) {
	transport.mu.Lock()
	transport.profile = profile
	transport.mu.Unlock()
	return transport.Do(ctx, request)
}

func (transport *riskFixtureTransport) ReadPageProfile(ctx context.Context, rawURL, profile string) ([]byte, http.Header, error) {
	transport.mu.Lock()
	transport.profile = profile
	transport.mu.Unlock()
	return transport.ReadPage(ctx, rawURL)
}

func riskHTTPResponse(status int, body []byte) *http.Response {
	return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(bytes.NewReader(body))}
}

func readRiskFixture(t interface {
	Helper()
	Fatal(...any)
}, site, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(riskFixtureRoot, site, name))
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestInstagramRoutingAndPostExtraction(t *testing.T) {
	for _, rawURL := range []string{
		"https://www.instagram.com/p/aye83DjauH/",
		"https://instagram.com/creator/reel/aye83DjauH/",
		"https://instagram.com/stories/creator/123/",
		"https://instagram.com/fixture.creator/",
	} {
		parsed, _ := url.Parse(rawURL)
		if !NewInstagram().Suitable(parsed) {
			t.Fatalf("Suitable(%q) = false", rawURL)
		}
	}
	for _, rawURL := range []string{"https://example.com/p/id/", "https://instagram.com/explore/", "ftp://instagram.com/p/id/", "https://instagram.com:443/p/id/"} {
		parsed, _ := url.Parse(rawURL)
		if NewInstagram().Suitable(parsed) {
			t.Fatalf("Suitable(%q) = true", rawURL)
		}
	}
	canonical := "https://www.instagram.com/p/aye83DjauH/"
	transport := &riskFixtureTransport{pages: map[string][]byte{canonical: readRiskFixture(t, "instagram", "post.html")}}
	result, err := NewInstagram().Extract(context.Background(), Request{URL: canonical + "?secret=not-forwarded", Transport: transport})
	if err != nil {
		t.Fatal(err)
	}
	if transport.profile != instagramImpersonationProfile {
		t.Fatalf("profile = %q", transport.profile)
	}
	assertRiskString(t, result, "id", "aye83DjauH")
	assertRiskString(t, result, "title", "Video by fixture_creator")
	assertRiskString(t, result, "channel", "fixture_creator")
	formats, ok := result.Info.Formats()
	if !ok || len(formats) != 1 {
		t.Fatalf("formats = %#v", formats)
	}
	format, _ := formats[0].Object()
	if got, _ := format.Lookup("url").StringValue(); got != "https://media.example.test/instagram/fixture.mp4" {
		t.Fatalf("format URL = %q", got)
	}
}

func TestInstagramProfilePlaylistIsLazyAndBoundedByCursor(t *testing.T) {
	profileURL := "https://www.instagram.com/api/v1/users/web_profile_info/?username=fixture_creator"
	transport := &riskFixtureTransport{responses: map[string]riskFixtureResponse{
		"GET " + profileURL: {body: readRiskFixture(t, "instagram", "profile.json")},
	}}
	transport.handler = func(ctx context.Context, request *http.Request) (*http.Response, error) {
		if request.URL.String() == profileURL {
			return riskHTTPResponse(http.StatusOK, readRiskFixture(t, "instagram", "profile.json")), nil
		}
		if request.URL.Host == "www.instagram.com" && request.URL.Path == "/graphql/query/" {
			if !strings.Contains(request.URL.Query().Get("variables"), "cursor-safe-page-2") {
				t.Fatalf("cursor variables = %q", request.URL.Query().Get("variables"))
			}
			return riskHTTPResponse(http.StatusOK, readRiskFixture(t, "instagram", "profile-page2.json")), nil
		}
		return riskHTTPResponse(http.StatusNotFound, nil), nil
	}
	result, err := NewInstagram().Extract(context.Background(), Request{URL: "https://instagram.com/fixture_creator/", Transport: transport})
	if err != nil {
		t.Fatal(err)
	}
	if !result.IsPlaylist() {
		t.Fatal("profile result is not a playlist")
	}
	if got := len(transport.requests); got != 1 {
		t.Fatalf("requests before iteration = %d, want 1", got)
	}
	entries, err := CollectEntries(context.Background(), result.Entries, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 || entries[0].ID != "FirstVideo" || entries[1].ID != "SecondVideo" {
		t.Fatalf("entries = %#v", entries)
	}
	if got := len(transport.requests); got != 2 {
		t.Fatalf("requests after iteration = %d, want 2", got)
	}
}

func TestInstagramCarouselAuthChallengeMalformedAndCancellation(t *testing.T) {
	carousel := []byte(`<script type="application/json">{"shortcode":"Carousel1","__typename":"GraphSidecar","user":{"username":"creator"},"carousel_media":[{"code":"Child1","video_url":"https://media.example.test/one.mp4"},{"code":"Child2","video_versions":[{"url":"https://media.example.test/two.mp4"}]}]}</script>`)
	result, err := parseInstagramPage(carousel, "Carousel1", "https://www.instagram.com/p/Carousel1/")
	if err != nil || !result.IsPlaylist() {
		t.Fatalf("carousel result playlist=%v error=%v", result.IsPlaylist(), err)
	}
	entries, err := CollectEntries(context.Background(), result.Entries, 10)
	if err != nil || len(entries) != 2 || entries[0].ExtractorKey != "generic" {
		t.Fatalf("carousel entries=%#v error=%v", entries, err)
	}
	for _, test := range []struct {
		name string
		page []byte
		want error
	}{
		{"auth", []byte(`<a href="/accounts/login/">private account</a>`), ErrAuthentication},
		{"challenge", []byte(`{"message":"challenge_required","secret":"must-not-leak"}`), ErrChallengeSolver},
		{"unavailable", []byte(`Page isn't available`), ErrUnavailable},
		{"malformed", []byte(`<script type="application/json">{"video_url":</script>`), ErrInvalidMetadata},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := parseInstagramPage(test.page, "Fixture1", "https://www.instagram.com/p/Fixture1/")
			if !errors.Is(err, test.want) {
				t.Fatalf("error = %v, want %v", err, test.want)
			}
			if err != nil && strings.Contains(err.Error(), "must-not-leak") {
				t.Fatalf("secret leaked: %v", err)
			}
		})
	}
	plainTransport := &memoryTransport{pages: map[string][]byte{}}
	if _, err := NewInstagram().Extract(context.Background(), Request{URL: "https://instagram.com/p/Fixture1/", Transport: plainTransport}); !errors.Is(err, ErrTransportProfile) {
		t.Fatalf("missing profile error = %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := NewInstagram().Extract(ctx, Request{URL: "https://instagram.com/p/Fixture1/", Transport: &riskFixtureTransport{wait: true}}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation error = %v", err)
	}
}

func TestInstagramJSONDiagnosticsAreSecretSafe(t *testing.T) {
	transport := &riskFixtureTransport{responses: map[string]riskFixtureResponse{}, handler: func(context.Context, *http.Request) (*http.Response, error) {
		return riskHTTPResponse(http.StatusOK, []byte(`{"secret":"private-cookie-value"} trailing`)), nil
	}}
	_, err := NewInstagram().Extract(context.Background(), Request{URL: "https://instagram.com/fixture_creator/", Transport: transport})
	if !errors.Is(err, ErrInvalidMetadata) || strings.Contains(err.Error(), "private-cookie-value") {
		t.Fatalf("error = %v", err)
	}
}

func FuzzParseInstagramPage(f *testing.F) {
	f.Add(readRiskFixture(f, "instagram", "post.html"))
	f.Add([]byte(`<script type="application/json">{}</script>`))
	f.Add([]byte(`challenge_required`))
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 1<<20 {
			t.Skip()
		}
		_, _ = parseInstagramPage(data, "Fixture1", "https://www.instagram.com/p/Fixture1/")
	})
}

func assertRiskString(t *testing.T, result Extraction, key, want string) {
	t.Helper()
	if got, ok := result.Info.Lookup(key).StringValue(); !ok || got != want {
		t.Fatalf("%s = %q, %t; want %q", key, got, ok, want)
	}
}

func TestRiskFixtureJSONIsValid(t *testing.T) {
	for _, fixture := range [][2]string{{"instagram", "profile.json"}, {"kick", "live.json"}, {"bbciplayer", "selector.json"}, {"ard", "item.json"}, {"nrk", "manifest.json"}, {"nrk", "metadata.json"}} {
		var decoded any
		if err := json.Unmarshal(readRiskFixture(t, fixture[0], fixture[1]), &decoded); err != nil {
			t.Fatalf("%s/%s: %v", fixture[0], fixture[1], err)
		}
	}
}
