package engine

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	mediaformat "github.com/tejasa97/ytdlp-go/internal/format"
	"github.com/tejasa97/ytdlp-go/internal/network"
	"github.com/tejasa97/ytdlp-go/internal/value"
)

func formatSelectorInfo() value.Info {
	format := func(id, ext, vcodec, acodec string, height int64) value.Value {
		return value.ObjectValue(value.NewObject(
			value.Field{Key: "format_id", Value: value.String(id)},
			value.Field{Key: "url", Value: value.String("https://example.invalid/" + id)},
			value.Field{Key: "ext", Value: value.String(ext)},
			value.Field{Key: "vcodec", Value: value.String(vcodec)},
			value.Field{Key: "acodec", Value: value.String(acodec)},
			value.Field{Key: "height", Value: value.Int(height)},
		))
	}
	return value.NewInfo(value.NewObject(value.Field{Key: "formats", Value: value.List(
		format("v1080", "mp4", "avc1", "none", 1080),
		format("v720", "mp4", "avc1", "none", 720),
		format("a128", "m4a", "none", "aac", 0),
		format("a64", "m4a", "none", "aac", 0),
		format("mux", "mp4", "avc1", "aac", 360),
	)}))
}

func TestFormatSelectorPrecedenceTraps(t *testing.T) {
	info := formatSelectorInfo()
	tests := []struct {
		name       string
		expression string
		wantIDs    []string
	}{
		// Pinned FormatSorter produces canonical worst-to-best order; quality
		// atoms traverse it in their pinned direction. With no numeric
		// tiebreaker the canonical id field makes "a64" the best audio.
		{"merge before slash", "bestvideo+bestaudio/best", []string{"v1080", "a64"}},
		{"grouped merge before slash", "(bestvideo+bestaudio)/best", []string{"v1080", "a64"}},
		{"comma independent", "bestvideo,(bestaudio/worst)", []string{"v1080"}},
		{"group filter", "(bv*+ba)[height<=1080]", []string{"v1080", "a64"}},
		{"nth best video", "bestvideo.2", []string{"v720"}},
		{"nth worst video", "worstvideo.2", []string{"v1080"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if test.name == "comma independent" {
				selector, err := mediaformat.ParseSelector(test.expression)
				if err != nil {
					t.Fatal(err)
				}
				plans, err := mediaformat.PlanSelect(info, selector)
				if err != nil || len(plans) != 2 {
					t.Fatalf("plans = %#v, %v", plans, err)
				}
				if plans[0].Tracks[0].ID != "v1080" || plans[1].Tracks[0].ID != "a64" {
					t.Fatalf("plans = %#v", plans)
				}
				return
			}
			selector, err := mediaformat.ParseSelector(test.expression)
			if err != nil {
				t.Fatal(err)
			}
			selected, err := mediaformat.Select(info, selector)
			if err != nil {
				t.Fatal(err)
			}
			if len(selected) != len(test.wantIDs) {
				t.Fatalf("selected = %#v", selected)
			}
			for index, want := range test.wantIDs {
				if selected[index].ID != want {
					t.Fatalf("selected[%d] = %q, want %q", index, selected[index].ID, want)
				}
			}
		})
	}
}

func TestPlanPreparedFormatsUsesInjectedDefaultCapabilities(t *testing.T) {
	info := formatSelectorInfo()
	tests := []struct {
		name          string
		capabilities  mediaformat.PlannerCapabilities
		live          bool
		liveFromStart bool
		want          []string
	}{
		{name: "merger", capabilities: mediaformat.PlannerCapabilities{CanMergeFormats: true}, want: []string{"v1080", "a64"}},
		{name: "no merger", capabilities: mediaformat.PlannerCapabilities{}, want: []string{"mux"}},
		{name: "stdout", capabilities: mediaformat.PlannerCapabilities{CanMergeFormats: true, OutputToStdout: true}, want: []string{"mux"}},
		{name: "live", capabilities: mediaformat.PlannerCapabilities{CanMergeFormats: true}, live: true, want: []string{"mux"}},
		{name: "live from start", capabilities: mediaformat.PlannerCapabilities{CanMergeFormats: true}, live: true, liveFromStart: true, want: []string{"v1080", "a64"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			currentInfo := value.NewInfo(info.Fields().Clone())
			if test.live {
				currentInfo.Set("is_live", value.Bool(true))
			}
			current, err := mediaformat.Prepare(currentInfo, mediaformat.Options{})
			if err != nil {
				t.Fatal(err)
			}
			operation := &operation{
				request:             Request{LiveFromStart: test.liveFromStart},
				plannerCapabilities: &test.capabilities,
			}
			plans, err := operation.planPreparedFormats(current)
			if err != nil {
				t.Fatal(err)
			}
			if len(plans) != 1 || len(plans[0].Tracks) != len(test.want) {
				t.Fatalf("plans = %#v, want %v", plans, test.want)
			}
			for index, want := range test.want {
				if plans[0].Tracks[index].ID != want {
					t.Fatalf("track[%d] = %q, want %q", index, plans[0].Tracks[index].ID, want)
				}
			}
		})
	}
}

func TestPrepareCompatibilityWiresMultistreamOptions(t *testing.T) {
	plan, err := prepareCompatibility(Request{
		AllowMultipleVideoStreams: true,
		AllowMultipleAudioStreams: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !plan.formatOptions.AllowMultipleVideoStreams || !plan.formatOptions.AllowMultipleAudioStreams {
		t.Fatalf("format options = %+v", plan.formatOptions)
	}
}

func TestFormatSelectorCommaNeverMergesInDownload(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = writer.Write([]byte("media-bytes"))
	}))
	defer server.Close()

	operation := &operation{
		client: newBroadTestClient(),
		request: Request{
			OutputDir: t.TempDir(),
			Overwrite: true,
			Format:    "video,audio",
		},
		compatibility: compatibilityPlan{
			formatOptions: mediaformat.Options{},
		},
	}
	transport, err := network.New(network.Config{})
	if err != nil {
		t.Fatal(err)
	}
	operation.transport = transport
	selector, err := mediaformat.ParseSelector(operation.request.Format)
	if err != nil {
		t.Fatal(err)
	}
	operation.compatibility.selector = &selector

	info := value.NewInfo(value.NewObject(
		value.Field{Key: "title", Value: value.String("comma")},
		value.Field{Key: "formats", Value: value.List(
			value.ObjectValue(value.NewObject(
				value.Field{Key: "format_id", Value: value.String("video")},
				value.Field{Key: "url", Value: value.String(server.URL + "/video")},
				value.Field{Key: "ext", Value: value.String("mp4")},
				value.Field{Key: "vcodec", Value: value.String("avc1")},
				value.Field{Key: "acodec", Value: value.String("none")},
			)),
			value.ObjectValue(value.NewObject(
				value.Field{Key: "format_id", Value: value.String("audio")},
				value.Field{Key: "url", Value: value.String(server.URL + "/audio")},
				value.Field{Key: "ext", Value: value.String("m4a")},
				value.Field{Key: "vcodec", Value: value.String("none")},
				value.Field{Key: "acodec", Value: value.String("aac")},
			)),
		)},
	))
	plans, err := operation.planFormats(info)
	if err != nil || len(plans) != 2 {
		t.Fatalf("plans = %#v, %v", plans, err)
	}
	base := filepath.Join(operation.request.OutputDir, "comma.mp4")
	var artifacts []Artifact
	for index, plan := range plans {
		path, _, downloadErr := operation.downloadSelections(context.Background(), plan.Tracks, operation.request.OutputDir, mustOutputPlanDestination(t, base, index, plan, true), nil)
		if downloadErr != nil {
			t.Fatal(downloadErr)
		}
		artifacts = append(artifacts, Artifact{Path: path, Kind: "media"})
		if index == 0 && !strings.Contains(path, ".f1_video.mp4") {
			t.Fatalf("path = %q", path)
		}
		if index == 1 && !strings.Contains(path, ".f2_audio.m4a") {
			t.Fatalf("path = %q", path)
		}
	}
	if len(artifacts) != 2 {
		t.Fatalf("artifacts = %#v", artifacts)
	}
}

func TestFormatSelectorMultiOutputLegacyAPIFailsClosed(t *testing.T) {
	selector, err := mediaformat.ParseSelector("bestvideo,bestaudio")
	if err != nil {
		t.Fatal(err)
	}
	_, err = mediaformat.Select(formatSelectorInfo(), selector)
	if !errors.Is(err, mediaformat.ErrMultiOutput) {
		t.Fatalf("Select() = %v", err)
	}
}

func TestFormatSelectorMalformedInputs(t *testing.T) {
	for _, input := range []string{"", "bestvideo,,best", "+bestaudio", "bestvideo+", "/", "(best", "best)", ",best", "(,best)", "(best,,)"} {
		if _, err := mediaformat.ParseSelector(input); !errors.Is(err, mediaformat.ErrInvalidSelector) {
			t.Fatalf("ParseSelector(%q) = %v", input, err)
		}
	}
}

func TestFormatSelectorInvalidSyntaxFailsBeforeExtraction(t *testing.T) {
	hits := 0
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		hits++
	}))
	t.Cleanup(server.Close)

	_, err := newBroadTestClient().Run(context.Background(), Request{
		URL: server.URL, OutputDir: t.TempDir(), Format: `  bestvideo+"unknown`,
	})
	if !errors.Is(err, mediaformat.ErrInvalidSelector) {
		t.Fatalf("Run() = %v", err)
	}
	if hits != 0 {
		t.Fatalf("network requests = %d, want 0", hits)
	}
}

func TestFormatSelectorDeterministicAcrossGoroutines(t *testing.T) {
	info := formatSelectorInfo()
	selector, err := mediaformat.ParseSelector("bestvideo+bestaudio/best")
	if err != nil {
		t.Fatal(err)
	}
	const workers = 8
	results := make(chan string, workers)
	for worker := 0; worker < workers; worker++ {
		go func() {
			selected, selectErr := mediaformat.Select(info, selector)
			if selectErr != nil {
				results <- selectErr.Error()
				return
			}
			results <- selected[0].ID + "+" + selected[1].ID
		}()
	}
	want := "v1080+a64"
	for worker := 0; worker < workers; worker++ {
		if got := <-results; got != want {
			t.Fatalf("worker result = %q, want %q", got, want)
		}
	}
}

func TestFormatSelectorExtensionAmbiguity(t *testing.T) {
	info := value.NewInfo(value.NewObject(value.Field{Key: "formats", Value: value.List(
		value.ObjectValue(value.NewObject(
			value.Field{Key: "format_id", Value: value.String("mp4")},
			value.Field{Key: "url", Value: value.String("https://example.invalid/mp4-id")},
			value.Field{Key: "ext", Value: value.String("mp4")},
			value.Field{Key: "vcodec", Value: value.String("avc1")},
			value.Field{Key: "acodec", Value: value.String("aac")},
		)),
		value.ObjectValue(value.NewObject(
			value.Field{Key: "format_id", Value: value.String("other")},
			value.Field{Key: "url", Value: value.String("https://example.invalid/other")},
			value.Field{Key: "ext", Value: value.String("mp4")},
			value.Field{Key: "vcodec", Value: value.String("avc1")},
			value.Field{Key: "acodec", Value: value.String("none")},
		)),
	)}))
	selector, err := mediaformat.ParseSelector("mp4")
	if err != nil {
		t.Fatal(err)
	}
	selected, err := mediaformat.Select(info, selector)
	if err != nil || len(selected) != 1 || selected[0].ID != "mp4" {
		t.Fatalf("direct ID = %#v, %v", selected, err)
	}
	selector, err = mediaformat.ParseSelector("mp4/best")
	if err != nil {
		t.Fatal(err)
	}
	selected, err = mediaformat.Select(info, selector)
	if err != nil || selected[0].ID != "mp4" {
		t.Fatalf("fallback = %#v, %v", selected, err)
	}
}

func TestFormatSelectorProductRollbackOnFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = writer.Write([]byte("ok-bytes"))
	}))
	defer server.Close()

	root := t.TempDir()
	preexisting := filepath.Join(root, "rollback_existing.mp4")
	if err := os.WriteFile(preexisting, []byte("keep-me"), 0o644); err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(root, "rollback.mp4")
	transport, err := network.New(network.Config{})
	if err != nil {
		t.Fatal(err)
	}
	operation := &operation{
		transport: transport,
		request:   Request{OutputDir: root, Overwrite: true},
	}
	plans := []mediaformat.OutputPlan{
		{Tracks: []mediaformat.Selection{{ID: "ok", URL: server.URL + "/ok", Ext: "mp4", VCodec: "avc1", ACodec: "aac"}}},
		{Tracks: []mediaformat.Selection{{ID: "bad", URL: "://missing-scheme", Ext: "mp4", VCodec: "avc1", ACodec: "aac"}}},
	}
	tracker := newMediaTransaction()
	if err := tracker.acquireDestinationBackups([]string{
		mustOutputPlanDestination(t, destination, 0, plans[0], true),
		mustOutputPlanDestination(t, destination, 1, plans[1], true),
	}, true); err != nil {
		t.Fatal(err)
	}
	for index, plan := range plans {
		path, _, downloadErr := operation.downloadSelections(context.Background(), plan.Tracks, root, mustOutputPlanDestination(t, destination, index, plan, true), nil)
		if downloadErr != nil {
			if rollbackErr := tracker.rollback(); rollbackErr != nil {
				t.Fatalf("rollback = %v", rollbackErr)
			}
			if index == 0 {
				t.Fatalf("first download should succeed: %v", downloadErr)
			}
			if _, statErr := os.Stat(filepath.Join(root, "rollback.f1_ok.mp4")); statErr == nil {
				t.Fatal("partial first output was not rolled back")
			}
			if body, readErr := os.ReadFile(preexisting); readErr != nil || string(body) != "keep-me" {
				t.Fatalf("preexisting file changed: %q %v", body, readErr)
			}
			return
		}
		tracker.markPublished(path)
	}
	t.Fatal("expected second download to fail")
}

func multiOutputSelectorFixture(t *testing.T, onMedia func()) (pageURL string) {
	t.Helper()
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/page":
			writer.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprintf(writer, `{
				"id":"multi","title":"Multi","ext":"mp4",
				"formats":[
					{"format_id":"video","url":%q,"ext":"mp4","vcodec":"avc1","acodec":"none"},
					{"format_id":"audio","url":%q,"ext":"m4a","vcodec":"none","acodec":"aac"}
				]
			}`, server.URL+"/video", server.URL+"/audio")
		case "/video", "/audio":
			if request.Method != http.MethodHead {
				if onMedia != nil {
					onMedia()
				}
				_, _ = writer.Write([]byte("media-bytes"))
			}
		default:
			http.NotFound(writer, request)
		}
	}))
	t.Cleanup(server.Close)
	return server.URL + "/page"
}

func TestMultiOutputProductRejectsMoveBeforeDownload(t *testing.T) {
	mediaHits := 0
	pageURL := multiOutputSelectorFixture(t, func() { mediaHits++ })
	root := t.TempDir()
	sharedDest := filepath.Join(root, "shared.mp4")
	if err := os.WriteFile(sharedDest, []byte("keep-me"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := newBroadTestClient().Run(context.Background(), Request{
		URL: pageURL, OutputDir: root, Format: "video,audio", Overwrite: true,
		Postprocessors: []Postprocessor{{Move: &MovePostprocessor{Destination: sharedDest}}},
	})
	if !errors.Is(err, mediaformat.ErrMultiOutput) {
		t.Fatalf("Run() = %v", err)
	}
	if mediaHits != 0 {
		t.Fatalf("media downloads = %d, want 0", mediaHits)
	}
	if body, readErr := os.ReadFile(sharedDest); readErr != nil || string(body) != "keep-me" {
		t.Fatalf("shared destination changed: %q %v", body, readErr)
	}
}

func TestMultiOutputProductAcceptsDefaultDestinationPostprocessors(t *testing.T) {
	tests := []struct {
		name string
		post Postprocessor
	}{
		{"remux", Postprocessor{Remux: &RemuxPostprocessor{Format: "mkv"}}},
		{"extract-audio", Postprocessor{ExtractAudio: &ExtractAudioPostprocessor{Codec: "mp3"}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := validateMultiOutputProduct(Request{Postprocessors: []Postprocessor{test.post}}, 2); err != nil {
				t.Fatalf("validate = %v", err)
			}
		})
	}
}

func TestOutputPlanDestinationUniqueWithDuplicateIDs(t *testing.T) {
	firstPlan := mediaformat.OutputPlan{Tracks: []mediaformat.Selection{{ID: "same", Ext: "mp4"}}}
	secondPlan := mediaformat.OutputPlan{Tracks: []mediaformat.Selection{{ID: "same", Ext: "m4a"}}}
	first, err := outputPlanDestination("/tmp/out/video.mp4", 0, firstPlan, true, nil)
	if err != nil {
		t.Fatal(err)
	}
	second, err := outputPlanDestination("/tmp/out/video.mp4", 1, secondPlan, true, nil)
	if err != nil {
		t.Fatal(err)
	}
	if first == second {
		t.Fatalf("paths collide: %q", first)
	}
	if filepath.Base(first) == filepath.Base(second) {
		t.Fatalf("basename collision: %q %q", first, second)
	}
}

func TestOutputPlanDestinationWindowsSafe(t *testing.T) {
	plan := mediaformat.OutputPlan{Tracks: []mediaformat.Selection{{ID: `weird:id|name`, Ext: "mp4"}}}
	path, err := outputPlanDestination(`C:\downloads\clip.mp4`, 2, plan, true, nil)
	if err != nil {
		t.Fatal(err)
	}
	base := filepath.Base(strings.ReplaceAll(path, `\`, `/`))
	if strings.ContainsAny(base, `<>:"|?*`) {
		t.Fatalf("unsafe windows basename: %q", base)
	}
	if !strings.Contains(base, ".f3_") {
		t.Fatalf("ordinal missing: %q", base)
	}
}

func TestMultiOutputMediaByteAccounting(t *testing.T) {
	serverA := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = writer.Write([]byte("aaaa"))
	}))
	defer serverA.Close()
	serverB := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = writer.Write([]byte("bbbbbbbb"))
	}))
	defer serverB.Close()

	root := t.TempDir()
	transport, err := network.New(network.Config{})
	if err != nil {
		t.Fatal(err)
	}
	operation := &operation{
		transport: transport,
		request:   Request{OutputDir: root, Overwrite: true},
	}
	plans := []mediaformat.OutputPlan{
		{Tracks: []mediaformat.Selection{{ID: "a", URL: serverA.URL, Ext: "mp4", VCodec: "avc1", ACodec: "aac"}}},
		{Tracks: []mediaformat.Selection{{ID: "b", URL: serverB.URL, Ext: "mp4", VCodec: "avc1", ACodec: "aac"}}},
	}
	base := filepath.Join(root, "sizes.mp4")
	var artifacts []Artifact
	for index, plan := range plans {
		path, _, err := operation.downloadSelections(context.Background(), plan.Tracks, root, mustOutputPlanDestination(t, base, index, plan, true), nil)
		if err != nil {
			t.Fatal(err)
		}
		artifacts = append(artifacts, Artifact{Path: path, Kind: "media"})
	}
	bytes, err := mediaArtifactBytes(artifacts)
	if err != nil {
		t.Fatal(err)
	}
	if bytes != 12 {
		t.Fatalf("media bytes = %d, want 12", bytes)
	}
}

func TestMultiOutputProductValidatesPostprocessorDestinations(t *testing.T) {
	tests := []struct {
		name string
		post Postprocessor
	}{
		{"move", Postprocessor{Move: &MovePostprocessor{Destination: "out.mp4"}}},
		{"remux", Postprocessor{Remux: &RemuxPostprocessor{Format: "mkv"}}},
		{"extract-audio", Postprocessor{ExtractAudio: &ExtractAudioPostprocessor{Codec: "mp3"}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := Request{Postprocessors: []Postprocessor{test.post}}
			if destination := postprocessorExplicitDestination(test.post); destination == "" {
				if err := validateMultiOutputProduct(request, 2); err != nil {
					t.Fatalf("default destination validate = %v", err)
				}
			} else if err := validateMultiOutputProduct(request, 2); !errors.Is(err, mediaformat.ErrMultiOutput) {
				t.Fatalf("fixed destination validate = %v", err)
			}
		})
	}
}

func TestMultiOutputProductAllowsSponsorBlockRemove(t *testing.T) {
	operation := &operation{
		request: Request{
			Format: "bestvideo,bestaudio",
			SponsorBlock: SponsorBlockOptions{
				Enabled: true, Remove: true, Categories: []string{"sponsor"},
			},
		},
	}
	if err := validateMultiOutputProduct(operation.request, 2); err != nil {
		t.Fatalf("validate = %v", err)
	}
}

func TestMultiOutputProductAllowsSubtitleEmbed(t *testing.T) {
	operation := &operation{
		request: Request{
			Format:    "bestvideo,bestaudio",
			Subtitles: SubtitleOptions{Embed: true},
		},
	}
	if err := validateMultiOutputProduct(operation.request, 2); err != nil {
		t.Fatalf("validate = %v", err)
	}
}

func TestErrSelectorLimitCategorizedInvalidInput(t *testing.T) {
	err := categorized("select format", fmt.Errorf("%w: too many independent outputs", mediaformat.ErrSelectorLimit))
	if !IsCategory(err, ErrorInvalidInput) {
		t.Fatalf("category = %v, want %v", err, ErrorInvalidInput)
	}
	if !errors.Is(err, mediaformat.ErrSelectorLimit) {
		t.Fatalf("errors.Is = %v", err)
	}
}

func mustOutputPlanDestination(t *testing.T, base string, planIndex int, plan mediaformat.OutputPlan, multi bool) string {
	t.Helper()
	path, err := outputPlanDestination(base, planIndex, plan, multi, nil)
	if err != nil {
		t.Fatal(err)
	}
	return path
}

func TestPlanDestinationExtensionFallbacks(t *testing.T) {
	tests := []struct {
		name   string
		tracks []mediaformat.Selection
		want   string
	}{
		{"empty", []mediaformat.Selection{{Ext: ""}}, "bin"},
		{"unsafe", []mediaformat.Selection{{Ext: "../../../etc"}}, "bin"},
		{"single", []mediaformat.Selection{{Ext: "m4a"}}, "m4a"},
		{"merge-mp4", []mediaformat.Selection{
			{Ext: "mp4", VCodec: "avc1", ACodec: "none"},
			{Ext: "m4a", VCodec: "none", ACodec: "aac"},
		}, "mp4"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := planDestinationExtension(mediaformat.OutputPlan{Tracks: test.tracks}, nil)
			if err != nil {
				t.Fatal(err)
			}
			if got != test.want {
				t.Fatalf("extension = %q, want %q", got, test.want)
			}
		})
	}
	got, err := planDestinationExtension(mediaformat.OutputPlan{Tracks: []mediaformat.Selection{
		{Ext: "mp4", VCodec: "avc1", ACodec: "none"},
		{Ext: "mp4", VCodec: "avc1", ACodec: "none"},
		{Ext: "m4a", VCodec: "none", ACodec: "aac"},
	}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got != "mkv" {
		t.Fatalf("3-track extension = %q, want mkv", got)
	}
}

func TestPlanDestinationExtensionUsesPlannerMetadata(t *testing.T) {
	plan := mediaformat.OutputPlan{
		Tracks: []mediaformat.Selection{
			{Ext: "mp4", VCodec: "avc1", ACodec: "none"},
			{Ext: "m4a", VCodec: "none", ACodec: "aac"},
		},
		Metadata: value.NewInfo(value.NewObject(value.Field{Key: "ext", Value: value.String("mp4")})),
	}
	got, err := planDestinationExtension(plan, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got != "mp4" {
		t.Fatalf("extension = %q, want planner metadata mp4", got)
	}
}

func TestMultiOutputPreservesPerPlanExtensions(t *testing.T) {
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/page":
			writer.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprintf(writer, `{
				"id":"ext","title":"Ext","ext":"mp4",
				"formats":[
					{"format_id":"video","url":%q,"ext":"mp4","vcodec":"avc1","acodec":"none","height":1080},
					{"format_id":"audio","url":%q,"ext":"m4a","vcodec":"none","acodec":"aac"}
				]
			}`, server.URL+"/video", server.URL+"/audio")
		case "/video":
			_, _ = writer.Write([]byte("VID!"))
		case "/audio":
			_, _ = writer.Write([]byte("AUDIODATA"))
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()

	root := t.TempDir()
	result, err := newBroadTestClient().Run(context.Background(), Request{
		URL: server.URL + "/page", OutputDir: root, Format: "video,audio",
		OutputTemplate: "%(title)s.%(ext)s", Overwrite: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(result.Filename, ".mp4") {
		t.Fatalf("Filename = %q", result.Filename)
	}
	if result.Filename != result.Artifacts[0].Path {
		t.Fatalf("Filename = %q, first artifact = %q", result.Filename, result.Artifacts[0].Path)
	}
	if len(result.Artifacts) != 2 {
		t.Fatalf("artifacts = %#v", result.Artifacts)
	}
	if !strings.HasSuffix(result.Artifacts[0].Path, ".mp4") {
		t.Fatalf("video artifact = %q", result.Artifacts[0].Path)
	}
	if !strings.HasSuffix(result.Artifacts[1].Path, ".m4a") {
		t.Fatalf("audio artifact = %q", result.Artifacts[1].Path)
	}
	videoBody, err := os.ReadFile(result.Artifacts[0].Path)
	if err != nil || string(videoBody) != "VID!" {
		t.Fatalf("video bytes = %q, %v", videoBody, err)
	}
	audioBody, err := os.ReadFile(result.Artifacts[1].Path)
	if err != nil || string(audioBody) != "AUDIODATA" {
		t.Fatalf("audio bytes = %q, %v", audioBody, err)
	}
	mediaBytes, err := mediaArtifactBytes(result.Artifacts)
	if err != nil {
		t.Fatal(err)
	}
	if mediaBytes != result.Bytes {
		t.Fatalf("media bytes = %d, result bytes = %d", mediaBytes, result.Bytes)
	}
	if mediaBytes != int64(len(videoBody)+len(audioBody)) {
		t.Fatalf("media bytes = %d, want %d", mediaBytes, len(videoBody)+len(audioBody))
	}
}
