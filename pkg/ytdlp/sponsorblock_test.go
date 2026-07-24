package ytdlp

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/ytdlp-go/ytdlp/internal/extractor"
	"github.com/ytdlp-go/ytdlp/internal/network"
	"github.com/ytdlp-go/ytdlp/internal/value"
)

func TestSponsorBlockOptionsValidation(t *testing.T) {
	for _, tc := range []struct {
		name    string
		options SponsorBlockOptions
		want    bool
	}{
		{"disabled", SponsorBlockOptions{Enabled: false}, true},
		{"empty enabled", SponsorBlockOptions{Enabled: true}, false},
		{"known category", SponsorBlockOptions{Enabled: true, Categories: []string{"sponsor"}}, true},
		{"unknown category", SponsorBlockOptions{Enabled: true, Categories: []string{"unknown"}}, false},
		{"empty category entry", SponsorBlockOptions{Enabled: true, Categories: []string{""}}, false},
		{"bad api base", SponsorBlockOptions{Enabled: true, Categories: []string{"sponsor"}, APIBase: "javascript:alert(1)"}, false},
		{"https api base", SponsorBlockOptions{Enabled: true, Categories: []string{"sponsor"}, APIBase: "https://example.test"}, true},
		{"too many categories", SponsorBlockOptions{Enabled: true, Categories: manyCategories(65)}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := validateSponsorBlockOptions(tc.options)
			if tc.want && err != nil {
				t.Fatalf("validate = %v, want nil", err)
			}
			if !tc.want && err == nil {
				t.Fatal("validate = nil, want error")
			}
		})
	}
}

// manyCategories returns a deterministic slice of repeated
// "sponsor" entries long enough to trigger the category cap.
func manyCategories(n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = "sponsor"
	}
	return out
}

func TestSponsorBlockMapErrorCategorizes(t *testing.T) {
	if err := mapSponsorBlockError(nil); err != nil {
		t.Fatalf("nil err: %v", err)
	}
	mapped := mapSponsorBlockError(errors.New("synthetic internal error"))
	if !IsCategory(mapped, ErrorInternal) {
		t.Fatalf("err = %v, want ErrorInternal", mapped)
	}
}

func TestSponsorBlockSecretSafeErrorMessages(t *testing.T) {
	mapped := mapSponsorBlockError(errors.New("token=leaked-secret"))
	rendered := mapped.Error()
	if strings.Contains(rendered, "leaked-secret") {
		t.Fatalf("error leaked token: %q", rendered)
	}
}

func TestSponsorBlockProcessMediaInfoJSONAndCredentialIsolation(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		calls.Add(1)
		if request.Header.Get("Authorization") != "" || request.Header.Get("Cookie") != "" {
			t.Errorf("credential reached SponsorBlock: %v", request.Header)
		}
		if !strings.HasSuffix(request.URL.Path, "/ba78") {
			t.Errorf("path = %q, want SHA-256 prefix ba78", request.URL.Path)
		}
		if request.URL.Query().Get("service") != "YouTube" ||
			request.URL.Query().Get("categories") != `["sponsor"]` ||
			request.URL.Query().Get("actionTypes") != `["skip","poi","chapter"]` {
			t.Errorf("query = %q", request.URL.RawQuery)
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(writer, `[{"videoID":"abc","segments":[{"segment":[1,5],"category":"sponsor","actionType":"skip","videoDuration":60}]}]`)
	}))
	defer server.Close()
	transport, err := network.New(network.Config{DefaultHeaders: http.Header{
		"Authorization": {"Bearer must-not-leak"},
		"Cookie":        {"session=must-not-leak"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer transport.CloseIdleConnections()
	request := Request{
		SkipDownload: true,
		SponsorBlock: SponsorBlockOptions{
			Enabled: true, Categories: []string{"sponsor"}, APIBase: server.URL,
		},
	}
	compatibility, err := prepareCompatibility(request)
	if err != nil {
		t.Fatal(err)
	}
	info := value.NewInfo(value.NewObject(
		value.Field{Key: "id", Value: value.String("abc")},
		value.Field{Key: "title", Value: value.String("fixture")},
		value.Field{Key: "duration", Value: value.Int(60)},
		value.Field{Key: "ext", Value: value.String("mp4")},
	))
	operation := &operation{client: NewClient(), request: request, transport: transport, compatibility: compatibility}
	result, err := operation.processMedia(context.Background(), extractor.Media(info), "youtube")
	if err != nil {
		t.Fatal(err)
	}
	var encoded map[string]any
	if err := json.Unmarshal(result.InfoJSON, &encoded); err != nil {
		t.Fatal(err)
	}
	chapters, ok := encoded["sponsorblock_chapters"].([]any)
	if !ok || len(chapters) != 1 {
		t.Fatalf("InfoJSON chapters = %#v", encoded["sponsorblock_chapters"])
	}
	chapter := chapters[0].(map[string]any)
	if chapter["start_time"] != float64(0) || chapter["end_time"] != float64(5) ||
		chapter["category"] != "sponsor" || chapter["title"] != "Sponsor" || chapter["type"] != "skip" {
		t.Fatalf("chapter = %#v", chapter)
	}
	if calls.Load() != 1 {
		t.Fatalf("calls = %d", calls.Load())
	}
}

func TestSponsorBlockDisabledUnsupportedAndMissingID(t *testing.T) {
	info := value.NewInfo(value.NewObject(value.Field{Key: "title", Value: value.String("fixture")}))
	disabled := &operation{request: Request{}}
	if err := disabled.enrichWithSponsorBlock(context.Background(), "vimeo", &info); err != nil {
		t.Fatalf("disabled enrichment = %v", err)
	}
	enabled := &operation{request: Request{SponsorBlock: SponsorBlockOptions{Enabled: true, Categories: []string{"sponsor"}}}}
	if err := enabled.enrichWithSponsorBlock(context.Background(), "vimeo", &info); !IsCategory(err, ErrorUnsupported) {
		t.Fatalf("unsupported error = %v", err)
	}
	if err := enabled.enrichWithSponsorBlock(context.Background(), "youtube", &info); !IsCategory(err, ErrorInternal) {
		t.Fatalf("missing ID error = %v", err)
	}
}

func TestSponsorBlockCancellationCategoryPreservesCause(t *testing.T) {
	err := mapSponsorBlockError(context.Canceled)
	if !IsCategory(err, ErrorCancelled) || !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation = %v", err)
	}
}

type sponsorBlockPlaylistExtractor struct{}

func (sponsorBlockPlaylistExtractor) Name() string { return "sponsor-parent" }
func (sponsorBlockPlaylistExtractor) Suitable(parsed *url.URL) bool {
	return parsed != nil && parsed.Host == "playlist.invalid"
}
func (sponsorBlockPlaylistExtractor) Extract(context.Context, extractor.Request) (extractor.Extraction, error) {
	return extractor.Playlist(
		value.NewInfo(value.NewObject(
			value.Field{Key: "id", Value: value.String("playlist")},
			value.Field{Key: "title", Value: value.String("playlist")},
		)),
		extractor.StaticEntries(
			extractor.Entry{URL: "https://child.invalid/one", ExtractorKey: "youtube"},
			extractor.Entry{URL: "https://child.invalid/two", ExtractorKey: "youtube"},
		),
	)
}

type sponsorBlockYouTubeFixtureExtractor struct{}

func (sponsorBlockYouTubeFixtureExtractor) Name() string           { return "youtube" }
func (sponsorBlockYouTubeFixtureExtractor) Suitable(*url.URL) bool { return true }
func (sponsorBlockYouTubeFixtureExtractor) Extract(_ context.Context, request extractor.Request) (extractor.Extraction, error) {
	parsed, _ := url.Parse(request.URL)
	id := strings.TrimPrefix(parsed.Path, "/")
	return extractor.Media(value.NewInfo(value.NewObject(
		value.Field{Key: "id", Value: value.String(id)},
		value.Field{Key: "title", Value: value.String(id)},
		value.Field{Key: "duration", Value: value.Int(60)},
		value.Field{Key: "ext", Value: value.String("mp4")},
	))), nil
}

func TestSponsorBlockRecursesIntoPlaylistMedia(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		calls.Add(1)
		writer.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(writer, `[
			{"videoID":"one","segments":[{"segment":[5,10],"category":"sponsor","actionType":"skip","videoDuration":60}]},
			{"videoID":"two","segments":[{"segment":[15,20],"category":"sponsor","actionType":"skip","videoDuration":60}]}
		]`)
	}))
	defer server.Close()
	transport, err := network.New(network.Config{})
	if err != nil {
		t.Fatal(err)
	}
	defer transport.CloseIdleConnections()
	request := Request{
		URL: "https://playlist.invalid/root", SkipDownload: true,
		SponsorBlock: SponsorBlockOptions{Enabled: true, Categories: []string{"sponsor"}, APIBase: server.URL},
	}
	compatibility, err := prepareCompatibility(request)
	if err != nil {
		t.Fatal(err)
	}
	rootExtractor := ""
	operation := &operation{
		client: NewClient(), request: request, transport: transport,
		registry:      extractor.NewRegistry(sponsorBlockPlaylistExtractor{}, sponsorBlockYouTubeFixtureExtractor{}),
		compatibility: compatibility, rootExtractor: &rootExtractor,
	}
	result, err := operation.process(context.Background(), request.URL, "", nil, make(map[string]bool), 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Entries) != 2 || calls.Load() != 2 {
		t.Fatalf("entries=%d calls=%d", len(result.Entries), calls.Load())
	}
	for index, child := range result.Entries {
		var encoded map[string]any
		if err := json.Unmarshal(child.InfoJSON, &encoded); err != nil {
			t.Fatal(err)
		}
		chapters, ok := encoded["sponsorblock_chapters"].([]any)
		if !ok || len(chapters) != 1 {
			t.Fatalf("child %d chapters = %#v", index, encoded["sponsorblock_chapters"])
		}
	}
}
