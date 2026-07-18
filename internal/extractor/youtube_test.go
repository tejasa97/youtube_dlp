package extractor

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"reflect"
	"testing"

	"github.com/ytdlp-go/ytdlp/internal/javascript/ejs"
	"github.com/ytdlp-go/ytdlp/internal/javascript/engine"
)

const (
	youtubeFixtureURL = "https://www.youtube.com/watch?v=fixture0001"
	youtubePlayerURL  = "https://www.youtube.com/s/player/fixture/base.js"
)

type memoryTransport struct {
	pages map[string][]byte
	reads []string
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

func readYouTubeFixture(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile("../../conformance/extractors/youtube/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return data
}
