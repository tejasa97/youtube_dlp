package ytdlp

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/ytdlp-go/ytdlp/internal/extractor"
	"github.com/ytdlp-go/ytdlp/internal/network"
	"github.com/ytdlp-go/ytdlp/internal/sponsorblock"
	"github.com/ytdlp-go/ytdlp/internal/value"
)

func TestSponsorBlockOptionsValidation(t *testing.T) {
	for _, tc := range []struct {
		name    string
		options SponsorBlockOptions
		want    bool
	}{
		{"disabled", SponsorBlockOptions{Enabled: false}, true},
		{"mark while disabled", SponsorBlockOptions{Mark: true}, false},
		{"remove while disabled", SponsorBlockOptions{Remove: true}, false},
		{"force keyframes while disabled", SponsorBlockOptions{ForceKeyframes: true}, false},
		{"empty enabled", SponsorBlockOptions{Enabled: true}, false},
		{"known category", SponsorBlockOptions{Enabled: true, Categories: []string{"sponsor"}}, true},
		{"mark known category", SponsorBlockOptions{Enabled: true, Mark: true, Categories: []string{"sponsor"}}, true},
		{"remove known category", SponsorBlockOptions{Enabled: true, Remove: true, Categories: []string{"sponsor"}}, true},
		{"remove with force keyframes", SponsorBlockOptions{Enabled: true, Remove: true, ForceKeyframes: true, Categories: []string{"sponsor"}}, true},
		{"force keyframes without remove", SponsorBlockOptions{Enabled: true, ForceKeyframes: true, Categories: []string{"sponsor"}}, false},
		{"remove categories without remove", SponsorBlockOptions{Enabled: true, Categories: []string{"sponsor"}, RemoveCategories: []string{"intro"}}, false},
		{"remove categories override", SponsorBlockOptions{Enabled: true, Remove: true, Categories: []string{"sponsor"}, RemoveCategories: []string{"intro"}}, true},
		{"non-removable remove category", SponsorBlockOptions{Enabled: true, Remove: true, Categories: []string{"sponsor"}, RemoveCategories: []string{"poi_highlight"}}, false},
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

func TestSponsorBlockMarkingRewritesChaptersAndPreservesFields(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(writer, `[{"videoID":"abc","segments":[
			{"segment":[10,30],"category":"sponsor","actionType":"skip","videoDuration":60},
			{"segment":[20,25],"category":"selfpromo","actionType":"skip","videoDuration":60}
		]}]`)
	}))
	defer server.Close()
	transport, err := network.New(network.Config{})
	if err != nil {
		t.Fatal(err)
	}
	defer transport.CloseIdleConnections()
	request := Request{SkipDownload: true, SponsorBlock: SponsorBlockOptions{
		Enabled: true, Mark: true, Categories: []string{"sponsor", "selfpromo"}, APIBase: server.URL,
	}}
	compatibility, err := prepareCompatibility(request)
	if err != nil {
		t.Fatal(err)
	}
	info := value.NewInfo(value.NewObject(
		value.Field{Key: "id", Value: value.String("abc")},
		value.Field{Key: "title", Value: value.String("Video")},
		value.Field{Key: "duration", Value: value.Int(60)},
		value.Field{Key: "chapters", Value: value.List(
			value.ObjectValue(value.NewObject(
				value.Field{Key: "start_time", Value: value.Int(0)},
				value.Field{Key: "end_time", Value: value.Int(40)},
				value.Field{Key: "title", Value: value.String("First")},
				value.Field{Key: "custom", Value: value.String("preserved")},
			)),
			value.ObjectValue(value.NewObject(
				value.Field{Key: "start_time", Value: value.Int(40)},
				value.Field{Key: "end_time", Value: value.Int(60)},
				value.Field{Key: "title", Value: value.String("Second")},
			)),
		)},
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
	rawChapters := encoded["chapters"].([]any)
	if len(rawChapters) != 6 {
		t.Fatalf("chapters = %#v", rawChapters)
	}
	first := rawChapters[0].(map[string]any)
	overlap := rawChapters[2].(map[string]any)
	if first["custom"] != "preserved" || first["title"] != "First" {
		t.Fatalf("ordinary chapter = %#v", first)
	}
	if overlap["title"] != "[SponsorBlock]: Sponsor, Unpaid/Self Promotion" ||
		overlap["category"] != "selfpromo" {
		t.Fatalf("overlap = %#v", overlap)
	}
	if len(encoded["sponsorblock_chapters"].([]any)) != 2 {
		t.Fatalf("sponsorblock_chapters = %#v", encoded["sponsorblock_chapters"])
	}
}

func TestSponsorBlockMarkingSynthesizesBackgroundChapter(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(writer, `[{"videoID":"abc","segments":[{"segment":[5,10],"category":"sponsor","actionType":"skip","videoDuration":20}]}]`)
	}))
	defer server.Close()
	transport, _ := network.New(network.Config{})
	defer transport.CloseIdleConnections()
	info := value.NewInfo(value.NewObject(
		value.Field{Key: "id", Value: value.String("abc")},
		value.Field{Key: "title", Value: value.String("Video")},
		value.Field{Key: "duration", Value: value.Int(20)},
	))
	operation := &operation{
		request: Request{SponsorBlock: SponsorBlockOptions{
			Enabled: true, Mark: true, Categories: []string{"sponsor"}, APIBase: server.URL,
		}},
		transport: transport,
	}
	if err := operation.enrichWithSponsorBlock(context.Background(), "youtube", &info); err != nil {
		t.Fatal(err)
	}
	chapters, _ := info.Lookup("chapters").ListValue()
	if len(chapters) != 3 {
		t.Fatalf("chapters = %#v", chapters)
	}
}

func TestSponsorBlockMarkingFailureDoesNotPartiallyMutateInfo(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		writer.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(writer, `[{"videoID":"abc","segments":[{"segment":[5,10],"category":"sponsor","actionType":"skip","videoDuration":20}]}]`)
	}))
	defer server.Close()
	transport, _ := network.New(network.Config{})
	defer transport.CloseIdleConnections()
	original := value.List(value.String("malformed"))
	info := value.NewInfo(value.NewObject(
		value.Field{Key: "id", Value: value.String("abc")},
		value.Field{Key: "title", Value: value.String("Video")},
		value.Field{Key: "duration", Value: value.Int(20)},
		value.Field{Key: "chapters", Value: original},
	))
	operation := &operation{
		request: Request{SponsorBlock: SponsorBlockOptions{
			Enabled: true, Mark: true, Categories: []string{"sponsor"}, APIBase: server.URL,
		}},
		transport: transport,
	}
	err := operation.enrichWithSponsorBlock(context.Background(), "youtube", &info)
	if !IsCategory(err, ErrorInvalidInput) {
		t.Fatalf("error = %v", err)
	}
	if !info.Lookup("sponsorblock_chapters").IsMissing() ||
		!reflect.DeepEqual(info.Lookup("chapters"), original) {
		t.Fatalf("info was partially mutated: %#v", info)
	}
	if calls.Load() != 0 {
		t.Fatalf("invalid local metadata made %d SponsorBlock requests", calls.Load())
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

func TestSponsorBlockDurationMismatchWarnsOnceAndLeavesMetadataUncommittedOnEmitFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(writer, `[{"videoID":"abc","segments":[
			{"segment":[10,20],"category":"sponsor","actionType":"skip","videoDuration":120},
			{"segment":[30,40],"category":"sponsor","actionType":"skip","videoDuration":200},
			{"segment":[0,0],"category":"sponsor","actionType":"skip","videoDuration":200},
			{"segment":[50,60],"category":"not-a-real-category","actionType":"skip","videoDuration":200}
		]}]`)
	}))
	defer server.Close()
	transport, err := network.New(network.Config{})
	if err != nil {
		t.Fatal(err)
	}
	defer transport.CloseIdleConnections()

	var warnings []string
	client := NewClient(WithEventHandler(func(_ context.Context, event Event) error {
		if event.Kind == EventMetadataWarning {
			warnings = append(warnings, event.Message)
		}
		return nil
	}))
	info := value.NewInfo(value.NewObject(
		value.Field{Key: "id", Value: value.String("abc")},
		value.Field{Key: "title", Value: value.String("Video")},
		value.Field{Key: "duration", Value: value.Int(120)},
	))
	op := &operation{
		client: client, transport: transport,
		request: Request{SponsorBlock: SponsorBlockOptions{
			Enabled: true, Categories: []string{"sponsor"}, APIBase: server.URL,
		}},
	}
	if err := op.enrichWithSponsorBlock(context.Background(), "youtube", &info); err != nil {
		t.Fatal(err)
	}
	if len(warnings) != 1 || warnings[0] != sponsorBlockDurationMismatchWarning {
		t.Fatalf("warnings = %#v", warnings)
	}
	chapters, _ := info.Lookup("sponsorblock_chapters").ListValue()
	if len(chapters) != 1 {
		t.Fatalf("chapters = %#v", chapters)
	}

	failing := NewClient(WithEventHandler(func(context.Context, Event) error {
		return errors.New("observer failed")
	}))
	rollbackInfo := value.NewInfo(value.NewObject(
		value.Field{Key: "id", Value: value.String("abc")},
		value.Field{Key: "title", Value: value.String("Video")},
		value.Field{Key: "duration", Value: value.Int(120)},
	))
	failOp := &operation{
		client: failing, transport: transport,
		request: Request{SponsorBlock: SponsorBlockOptions{
			Enabled: true, Categories: []string{"sponsor"}, APIBase: server.URL,
		}},
	}
	err = failOp.enrichWithSponsorBlock(context.Background(), "youtube", &rollbackInfo)
	if !IsCategory(err, ErrorInternal) {
		t.Fatalf("error = %v", err)
	}
	if !rollbackInfo.Lookup("sponsorblock_chapters").IsMissing() {
		t.Fatalf("metadata committed despite warning failure: %#v", rollbackInfo)
	}
}

func TestSponsorBlockNoDurationMismatchWarningForWholeVideoOrInvalid(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(writer, `[{"videoID":"abc","segments":[
			{"segment":[0,0],"category":"sponsor","actionType":"skip","videoDuration":999},
			{"segment":[10,20],"category":"not-a-real-category","actionType":"skip","videoDuration":999},
			{"segment":[10,20],"category":"sponsor","actionType":"skip","videoDuration":0}
		]}]`)
	}))
	defer server.Close()
	transport, err := network.New(network.Config{})
	if err != nil {
		t.Fatal(err)
	}
	defer transport.CloseIdleConnections()
	var warnings int
	client := NewClient(WithEventHandler(func(_ context.Context, event Event) error {
		if event.Kind == EventMetadataWarning {
			warnings++
		}
		return nil
	}))
	info := value.NewInfo(value.NewObject(
		value.Field{Key: "id", Value: value.String("abc")},
		value.Field{Key: "duration", Value: value.Int(60)},
	))
	operation := &operation{
		client: client, transport: transport,
		request: Request{SponsorBlock: SponsorBlockOptions{
			Enabled: true, Categories: []string{"sponsor"}, APIBase: server.URL,
		}},
	}
	if err := operation.enrichWithSponsorBlock(context.Background(), "youtube", &info); err != nil {
		t.Fatal(err)
	}
	if warnings != 0 {
		t.Fatalf("warnings = %d", warnings)
	}
}

func TestSponsorBlockMarkPlusRemoveDefersMarkDuringEnrich(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(writer, `[{"videoID":"abc","segments":[
			{"segment":[10,20],"category":"sponsor","actionType":"skip","videoDuration":100},
			{"segment":[55,65],"category":"selfpromo","actionType":"skip","videoDuration":100}
		]}]`)
	}))
	defer server.Close()
	transport, err := network.New(network.Config{})
	if err != nil {
		t.Fatal(err)
	}
	defer transport.CloseIdleConnections()
	original := value.List(value.ObjectValue(value.NewObject(
		value.Field{Key: "start_time", Value: value.Float(0)},
		value.Field{Key: "end_time", Value: value.Float(100)},
		value.Field{Key: "title", Value: value.String("Video")},
	)))
	info := value.NewInfo(value.NewObject(
		value.Field{Key: "id", Value: value.String("abc")},
		value.Field{Key: "title", Value: value.String("Video")},
		value.Field{Key: "duration", Value: value.Int(100)},
		value.Field{Key: "chapters", Value: original},
	))
	operation := &operation{
		client: NewClient(), transport: transport,
		request: Request{SponsorBlock: SponsorBlockOptions{
			Enabled: true, Mark: true, Remove: true,
			Categories: []string{"sponsor", "selfpromo"}, RemoveCategories: []string{"sponsor"},
			APIBase: server.URL,
		}},
	}
	if err := operation.enrichWithSponsorBlock(context.Background(), "youtube", &info); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(info.Lookup("chapters"), original) {
		t.Fatalf("mark+remove mutated chapters during enrich: %#v", info.Lookup("chapters"))
	}
	sponsors, _ := info.Lookup("sponsorblock_chapters").ListValue()
	if len(sponsors) != 2 {
		t.Fatalf("sponsorblock_chapters = %#v", sponsors)
	}
}

func TestSponsorBlockMarkPlusOrdinaryRemovalDefersMarkDuringEnrich(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(writer, `[{"videoID":"abc","segments":[
			{"segment":[10,20],"category":"sponsor","actionType":"skip","videoDuration":100}
		]}]`)
	}))
	defer server.Close()
	transport, err := network.New(network.Config{})
	if err != nil {
		t.Fatal(err)
	}
	defer transport.CloseIdleConnections()
	original := value.List(value.ObjectValue(value.NewObject(
		value.Field{Key: "start_time", Value: value.Float(0)},
		value.Field{Key: "end_time", Value: value.Float(100)},
		value.Field{Key: "title", Value: value.String("Intro")},
	)))
	info := value.NewInfo(value.NewObject(
		value.Field{Key: "id", Value: value.String("abc")},
		value.Field{Key: "title", Value: value.String("Video")},
		value.Field{Key: "duration", Value: value.Int(100)},
		value.Field{Key: "chapters", Value: original},
	))
	operation := &operation{
		client: NewClient(), transport: transport,
		request: Request{
			RemoveChapters: []string{"^Intro$"},
			SponsorBlock: SponsorBlockOptions{
				Enabled: true, Mark: true, Categories: []string{"sponsor"}, APIBase: server.URL,
			},
		},
	}
	if err := operation.enrichWithSponsorBlock(context.Background(), "youtube", &info); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(info.Lookup("chapters"), original) {
		t.Fatalf("mark+ordinary removal mutated chapters during enrich: %#v", info.Lookup("chapters"))
	}
	sponsors, _ := info.Lookup("sponsorblock_chapters").ListValue()
	if len(sponsors) != 1 {
		t.Fatalf("sponsorblock_chapters = %#v", sponsors)
	}
}

func TestSponsorBlockMarkPlusRemoveSimulateEnrichAppliesMarks(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(writer, `[{"videoID":"abc","segments":[
			{"segment":[10,20],"category":"sponsor","actionType":"skip","videoDuration":100}
		]}]`)
	}))
	defer server.Close()
	transport, err := network.New(network.Config{})
	if err != nil {
		t.Fatal(err)
	}
	defer transport.CloseIdleConnections()
	info := value.NewInfo(value.NewObject(
		value.Field{Key: "id", Value: value.String("abc")},
		value.Field{Key: "title", Value: value.String("Video")},
		value.Field{Key: "duration", Value: value.Int(100)},
		value.Field{Key: "chapters", Value: value.List(value.ObjectValue(value.NewObject(
			value.Field{Key: "start_time", Value: value.Float(0)},
			value.Field{Key: "end_time", Value: value.Float(100)},
			value.Field{Key: "title", Value: value.String("Video")},
			value.Field{Key: "custom", Value: value.String("preserved")},
		)))},
	))
	op := &operation{
		client: NewClient(), transport: transport,
		request: Request{
			Simulate: true,
			SponsorBlock: SponsorBlockOptions{
				Enabled: true, Mark: true, Remove: true, Categories: []string{"sponsor"}, APIBase: server.URL,
			},
		},
	}
	if err := op.enrichWithSponsorBlock(context.Background(), "youtube", &info); err != nil {
		t.Fatal(err)
	}
	chapters, _ := info.Lookup("chapters").ListValue()
	sponsors := 0
	for _, item := range chapters {
		object, _ := item.Object()
		if category, ok := object.Lookup("category").StringValue(); ok && category != "" {
			sponsors++
		}
	}
	if sponsors != 1 {
		t.Fatalf("simulate enrich chapters = %#v", chapters)
	}
	first, _ := chapters[0].Object()
	if custom, _ := first.Lookup("custom").StringValue(); custom != "preserved" {
		t.Fatalf("ordinary fields not preserved: %#v", first)
	}
}

func TestSponsorBlockMarkPlusRemoveSkipDownloadEnrichAppliesMarks(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(writer, `[{"videoID":"abc","segments":[
			{"segment":[10,20],"category":"sponsor","actionType":"skip","videoDuration":100}
		]}]`)
	}))
	defer server.Close()
	transport, err := network.New(network.Config{})
	if err != nil {
		t.Fatal(err)
	}
	defer transport.CloseIdleConnections()
	info := value.NewInfo(value.NewObject(
		value.Field{Key: "id", Value: value.String("abc")},
		value.Field{Key: "title", Value: value.String("Video")},
		value.Field{Key: "duration", Value: value.Int(100)},
	))
	op := &operation{
		client: NewClient(), transport: transport,
		request: Request{
			SkipDownload: true,
			SponsorBlock: SponsorBlockOptions{
				Enabled: true, Mark: true, Remove: true, Categories: []string{"sponsor"}, APIBase: server.URL,
			},
		},
	}
	if err := op.enrichWithSponsorBlock(context.Background(), "youtube", &info); err != nil {
		t.Fatal(err)
	}
	chapters, _ := info.Lookup("chapters").ListValue()
	sponsors := 0
	for _, item := range chapters {
		object, _ := item.Object()
		if category, ok := object.Lookup("category").StringValue(); ok && category != "" {
			sponsors++
		}
	}
	if sponsors != 1 {
		t.Fatalf("skip-download enrich chapters = %#v", chapters)
	}
}

func TestSponsorBlockArrangeRemoveOnlyAndMarkRemove(t *testing.T) {
	normal := []sponsorblock.NormalChapter{
		{StartTime: 0, EndTime: 40, Title: "Intro", Source: 0},
		{StartTime: 40, EndTime: 100, Title: "Main", Source: 1},
	}
	sponsors := []sponsorblock.Chapter{
		{StartTime: 10, EndTime: 20, Category: "sponsor", Title: "Sponsor", Type: "skip"},
		{StartTime: 55, EndTime: 65, Category: "selfpromo", Title: "Unpaid/Self Promotion", Type: "skip"},
	}
	removeOnly, err := arrangeSponsorBlockRemove(normal, sponsors, []string{"sponsor"}, 100, "Video", false)
	if err != nil {
		t.Fatal(err)
	}
	if len(removeOnly.Cuts) != 1 || removeOnly.Cuts[0] != (sponsorblock.Range{Start: 10, End: 20}) {
		t.Fatalf("remove-only cuts = %#v", removeOnly.Cuts)
	}
	for _, chapter := range removeOnly.Chapters {
		if chapter.Sponsor {
			t.Fatalf("remove-only should not mark non-remove sponsors: %#v", removeOnly.Chapters)
		}
	}
	markRemove, err := arrangeSponsorBlockRemove(normal, sponsors, []string{"sponsor"}, 100, "Video", true)
	if err != nil {
		t.Fatal(err)
	}
	if len(markRemove.Cuts) != 1 || markRemove.Cuts[0] != (sponsorblock.Range{Start: 10, End: 20}) {
		t.Fatalf("mark+remove cuts = %#v", markRemove.Cuts)
	}
	sponsorsMarked := 0
	for _, chapter := range markRemove.Chapters {
		if chapter.Sponsor {
			sponsorsMarked++
			if chapter.Category != "selfpromo" {
				t.Fatalf("unexpected sponsor %#v", chapter)
			}
		}
	}
	if sponsorsMarked != 1 {
		t.Fatalf("mark+remove chapters = %#v", markRemove.Chapters)
	}
}
