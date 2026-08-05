package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/tejasa97/youtube_dlp/internal/compat/chapterremove"
	"github.com/tejasa97/youtube_dlp/internal/media/ffmpeg"
	"github.com/tejasa97/youtube_dlp/internal/network"
	"github.com/tejasa97/youtube_dlp/internal/sponsorblock"
	"github.com/tejasa97/youtube_dlp/internal/value"
)

// TestRecodeVideoPostprocessorNoOpHonorsPinnedSemantics asserts that both
// "target matches source" and "no mapping rule applies" never invoke
// ffmpeg, never reserve a new destination, and never advance the current
// media path. The dispatch path is exercised against a placeholder file
// whose parent directory has no ffmpeg binary on PATH so any toolset
// discovery would either skip the operation (which is the intended
// behavior under test) or fail with an explicit error.
func TestRecodeVideoPostprocessorNoOpHonorsPinnedSemantics(t *testing.T) {
	root := t.TempDir()
	input := filepath.Join(root, "in.mkv")
	if err := os.WriteFile(input, []byte("placeholder"), 0o600); err != nil {
		t.Fatal(err)
	}
	operation := &operation{client: newBroadTestClient(), request: Request{}}
	cases := []struct {
		name    string
		mapping string
	}{
		{name: "target matches source", mapping: "mkv"},
		{name: "no mapping rule applies", mapping: "mov>mp4"},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			postprocessors := []Postprocessor{{RecodeVideo: &RecodeVideoPostprocessor{Format: tc.mapping}}}
			next, _, err := operation.applyPostprocessors(context.Background(), root, input, nil)
			if err != nil {
				t.Fatalf("applyPostprocessors: %v", err)
			}
			if next != input {
				t.Fatalf("current advanced to %q; want %q (no-op must keep the original path)", next, input)
			}
			if _, statErr := os.Stat(filepath.Join(root, "in.postprocessed.mkv")); statErr == nil {
				t.Fatalf("a destination was reserved for the no-op path")
			}
			if _, statErr := os.Stat(filepath.Join(root, "in.mp4")); statErr == nil {
				t.Fatalf("a destination was reserved for the no-op path")
			}
			_ = postprocessors
		})
	}
}

// TestRecodeVideoPostprocessorRejectsUnsupportedTarget ensures that the
// dispatch path fails at preflight (no ffmpeg toolset discovery, no file
// writes) when the target extension is outside the closed allowlist.
func TestRecodeVideoPostprocessorRejectsUnsupportedTarget(t *testing.T) {
	root := t.TempDir()
	input := filepath.Join(root, "in.mkv")
	if err := os.WriteFile(input, []byte("placeholder"), 0o600); err != nil {
		t.Fatal(err)
	}
	operation := &operation{client: newBroadTestClient(), request: Request{
		Postprocessors: []Postprocessor{{RecodeVideo: &RecodeVideoPostprocessor{Format: "jpg"}}},
	}}
	if _, _, err := operation.applyPostprocessors(context.Background(), root, input, nil); err == nil {
		t.Fatal("expected unsupported target error")
	}
}

func TestArrangeChapterRemovalCombinesOrdinaryManualAndSponsorCuts(t *testing.T) {
	program, err := chapterremove.Parse([]string{`(?i)^(intro|credits)$`, "*40-50"})
	if err != nil {
		t.Fatal(err)
	}
	normal := []sponsorblock.NormalChapter{
		{StartTime: 0, EndTime: 20, Title: "Intro", Source: 0},
		{StartTime: 20, EndTime: 80, Title: "Main", Source: 1},
		{StartTime: 80, EndTime: 100, Title: "Credits", Source: 2},
	}
	remove := map[int]struct{}{}
	for _, chapter := range normal {
		matched, matchErr := program.MatchTitle(context.Background(), chapter.Title)
		if matchErr != nil {
			t.Fatal(matchErr)
		}
		if matched {
			remove[chapter.Source] = struct{}{}
		}
	}
	ranges, err := program.ResolveRanges(100)
	if err != nil {
		t.Fatal(err)
	}
	arranged, err := arrangeChapterRemove(
		normal,
		[]sponsorblock.Chapter{{StartTime: 60, EndTime: 70, Category: "sponsor", Type: "skip"}},
		[]string{"sponsor"},
		remove,
		ranges,
		100,
		"Video",
		false,
	)
	if err != nil {
		t.Fatal(err)
	}
	wantCuts := []sponsorblock.Range{
		{Start: 0, End: 20},
		{Start: 40, End: 50},
		{Start: 60, End: 70},
		{Start: 80, End: 100},
	}
	if len(arranged.Cuts) != len(wantCuts) {
		t.Fatalf("cuts = %#v", arranged.Cuts)
	}
	for index := range wantCuts {
		if arranged.Cuts[index] != wantCuts[index] {
			t.Fatalf("cut %d = %#v, want %#v", index, arranged.Cuts[index], wantCuts[index])
		}
	}
	plan, err := cutPlanFromArrange(arranged.Cuts, 100)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Duration != 40 {
		t.Fatalf("post-cut duration = %v", plan.Duration)
	}
}

func TestChapterRemovalWarningsAndSimulationDoNotMutateMetadata(t *testing.T) {
	var warnings []string
	client := newBroadTestClient(WithEventHandler(func(_ context.Context, event Event) error {
		if event.Kind == EventMetadataWarning {
			warnings = append(warnings, event.Message)
		}
		return nil
	}))
	program, err := chapterremove.Parse([]string{"missing"})
	if err != nil {
		t.Fatal(err)
	}
	info := value.Info{}
	info.Set("duration", value.Float(10))
	info.Set("chapters", value.List(value.ObjectValue(value.NewObject(
		value.Field{Key: "start_time", Value: value.Float(0)},
		value.Field{Key: "end_time", Value: value.Float(10)},
		value.Field{Key: "title", Value: value.String("Main")},
	))))
	before, err := encodeInfo(info)
	if err != nil {
		t.Fatal(err)
	}
	operation := &operation{
		client: client,
		request: Request{
			Simulate:       true,
			RemoveChapters: []string{"missing"},
		},
		compatibility: compatibilityPlan{chapterRemoval: program},
	}
	_, _, cut, err := operation.applyChapterCuts(context.Background(), &info, "missing.mp4", nil, nil)
	if err != nil || cut {
		t.Fatalf("cut = %t, err = %v", cut, err)
	}
	after, err := encodeInfo(info)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Fatalf("simulate mutated metadata:\n%s\n%s", before, after)
	}
	// Simulation returns before postprocessor warnings, matching the absence of
	// postprocessing in upstream simulation mode.
	if len(warnings) != 0 {
		t.Fatalf("simulation warnings = %v", warnings)
	}
}

func TestChapterRemovalNoMatchWarningAndCancellation(t *testing.T) {
	var warnings []string
	client := newBroadTestClient(WithEventHandler(func(_ context.Context, event Event) error {
		if event.Kind == EventMetadataWarning {
			warnings = append(warnings, event.Message)
		}
		return nil
	}))
	program, err := chapterremove.Parse([]string{"missing"})
	if err != nil {
		t.Fatal(err)
	}
	info := value.Info{}
	info.Set("chapters", value.List(value.ObjectValue(value.NewObject(
		value.Field{Key: "start_time", Value: value.Float(0)},
		value.Field{Key: "end_time", Value: value.Float(10)},
		value.Field{Key: "title", Value: value.String("Main")},
	))))
	operation := &operation{
		client: client, request: Request{RemoveChapters: []string{"missing"}},
		compatibility: compatibilityPlan{chapterRemoval: program},
	}
	_, _, cut, err := operation.applyChapterCuts(context.Background(), &info, "", nil, nil)
	if err != nil || cut {
		t.Fatalf("cut = %t, err = %v", cut, err)
	}
	if len(warnings) != 1 || warnings[0] != "There are no chapters matching the regex" {
		t.Fatalf("warnings = %v", warnings)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, _, _, err = operation.applyChapterCuts(ctx, &info, "", nil, nil)
	if err == nil || !IsCategory(err, ErrorCancelled) {
		t.Fatalf("cancellation err = %v", err)
	}
}

func TestChapterRemovalInvalidRequestsFailBeforeNetwork(t *testing.T) {
	var hits int
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		hits++
	}))
	defer server.Close()
	for _, request := range []Request{
		{RemoveChapters: []string{"("}},
		{RemoveChapters: []string{"*1--2"}},
	} {
		request.URL = server.URL + "/media.mp4"
		if _, err := newBroadTestClient().Run(context.Background(), request); !IsCategory(err, ErrorInvalidInput) {
			t.Errorf("request %#v err = %v", request, err)
		}
	}
	if hits != 0 {
		t.Fatalf("invalid requests made %d network calls", hits)
	}
}

func TestChapterRemovalAllowsMultiOutputPlanning(t *testing.T) {
	info := value.NewInfo(value.NewObject(
		value.Field{Key: "id", Value: value.String("multi")},
		value.Field{Key: "title", Value: value.String("Multi")},
		value.Field{Key: "formats", Value: value.List(
			value.ObjectValue(value.NewObject(
				value.Field{Key: "format_id", Value: value.String("a")},
				value.Field{Key: "url", Value: value.String("https://media.example/a.mp4")},
				value.Field{Key: "ext", Value: value.String("mp4")},
			)),
			value.ObjectValue(value.NewObject(
				value.Field{Key: "format_id", Value: value.String("b")},
				value.Field{Key: "url", Value: value.String("https://media.example/b.mp4")},
				value.Field{Key: "ext", Value: value.String("mp4")},
			)),
		)},
	))
	compatibility, err := prepareCompatibility(Request{
		Format: "a,b", RemoveChapters: []string{"intro"},
	})
	if err != nil {
		t.Fatal(err)
	}
	operation := &operation{
		request:       Request{Format: "a,b", RemoveChapters: []string{"intro"}},
		compatibility: compatibility,
	}
	plans, err := operation.planFormats(info)
	if err != nil {
		t.Fatal(err)
	}
	if err := validateMultiOutputProduct(operation.request, len(plans)); err != nil {
		t.Fatalf("multi-output chapter removal validation: %v", err)
	}
}

func TestChapterRemovalProbesDurationAndFixesOpenFinalChapter(t *testing.T) {
	mediaPath := generateChapterRemovalMedia(t, 4)
	program, err := chapterremove.Parse([]string{"^Intro$"})
	if err != nil {
		t.Fatal(err)
	}
	info := value.NewInfo(value.NewObject(
		value.Field{Key: "title", Value: value.String("Open Final")},
		value.Field{Key: "duration", Value: value.Float(4)},
		value.Field{Key: "chapters", Value: value.List(
			value.ObjectValue(value.NewObject(
				value.Field{Key: "start_time", Value: value.Float(0)},
				value.Field{Key: "end_time", Value: value.Float(1)},
				value.Field{Key: "title", Value: value.String("Intro")},
			)),
			value.ObjectValue(value.NewObject(
				value.Field{Key: "start_time", Value: value.Float(1)},
				value.Field{Key: "end_time", Value: value.Float(0)},
				value.Field{Key: "title", Value: value.String("Main")},
			)),
		)},
	))
	operation := &operation{
		client: newBroadTestClient(), request: Request{RemoveChapters: []string{"^Intro$"}},
		compatibility: compatibilityPlan{chapterRemoval: program},
	}
	_, _, cut, err := operation.applyChapterCuts(context.Background(), &info, mediaPath, nil, nil)
	if err != nil || !cut {
		t.Fatalf("cut = %t, err = %v", cut, err)
	}
	duration, ok := sponsorblockDuration(info.Lookup("duration"))
	if !ok || duration < 2.7 || duration > 3.3 {
		t.Fatalf("post-cut duration = %v, %t", duration, ok)
	}
	chapters, _ := info.Lookup("chapters").ListValue()
	if len(chapters) != 1 {
		t.Fatalf("chapters = %#v", chapters)
	}
	main := mustObject(t, chapters[0])
	end, ok := sponsorblockNumber(main.Lookup("end_time"))
	if title, _ := main.Lookup("title").StringValue(); title != "Main" || !ok || end < 2.7 || end > 3.3 {
		t.Fatalf("final chapter = %#v", main)
	}
}

func TestChapterRemovalUsesRealDurationForMissingMetadataAndOpenRange(t *testing.T) {
	for _, test := range []struct {
		name             string
		metadataDuration float64
		spec             string
		wantDuration     float64
	}{
		{name: "missing metadata duration", spec: "*1-2", wantDuration: 4},
		{name: "tolerated real tail", metadataDuration: 4.2, spec: "*3.5-", wantDuration: 3.5},
		{name: "finite range inside tolerated real tail", metadataDuration: 4.2, spec: "*4.4-4.8", wantDuration: 4.6},
	} {
		t.Run(test.name, func(t *testing.T) {
			mediaPath := generateChapterRemovalMedia(t, 5)
			program, err := chapterremove.Parse([]string{test.spec})
			if err != nil {
				t.Fatal(err)
			}
			info := value.NewInfo(value.NewObject(
				value.Field{Key: "title", Value: value.String("Duration")},
			))
			if test.metadataDuration > 0 {
				info.Set("duration", value.Float(test.metadataDuration))
			}
			operation := &operation{
				client: newBroadTestClient(), request: Request{RemoveChapters: []string{test.spec}},
				compatibility: compatibilityPlan{chapterRemoval: program},
			}
			_, _, cut, err := operation.applyChapterCuts(context.Background(), &info, mediaPath, nil, nil)
			if err != nil || !cut {
				t.Fatalf("cut = %t, err = %v", cut, err)
			}
			duration, ok := sponsorblockDuration(info.Lookup("duration"))
			if !ok || duration < test.wantDuration-0.15 || duration > test.wantDuration+0.15 {
				t.Fatalf("post-cut duration = %v, %t; want %v", duration, ok, test.wantDuration)
			}
			tools, err := ffmpeg.Discover(ffmpeg.Config{})
			if err != nil {
				t.Fatal(err)
			}
			probe, err := tools.Probe(context.Background(), mediaPath)
			if err != nil {
				t.Fatal(err)
			}
			actual, err := strconv.ParseFloat(probe.Format.Duration, 64)
			if err != nil || actual < test.wantDuration-0.25 || actual > test.wantDuration+0.25 {
				t.Fatalf("media duration = %v, err = %v; want %v", actual, err, test.wantDuration)
			}
		})
	}
}

func TestChapterRemovalSafetyFailuresLeaveMediaAndMetadataUnchanged(t *testing.T) {
	tests := []struct {
		name     string
		duration float64
		chapters value.Value
		spec     string
	}{
		{
			name:     "metadata and real duration mismatch",
			duration: 20,
			chapters: value.List(
				value.ObjectValue(value.NewObject(
					value.Field{Key: "start_time", Value: value.Float(0)},
					value.Field{Key: "end_time", Value: value.Float(1)},
					value.Field{Key: "title", Value: value.String("Intro")},
				)),
				value.ObjectValue(value.NewObject(
					value.Field{Key: "start_time", Value: value.Float(1)},
					value.Field{Key: "end_time", Value: value.Float(20)},
					value.Field{Key: "title", Value: value.String("Main")},
				)),
			),
			spec: "^Intro$",
		},
		{
			name:     "open final chapter with duration mismatch",
			duration: 20,
			chapters: value.List(
				value.ObjectValue(value.NewObject(
					value.Field{Key: "start_time", Value: value.Float(0)},
					value.Field{Key: "end_time", Value: value.Float(1)},
					value.Field{Key: "title", Value: value.String("Intro")},
				)),
				value.ObjectValue(value.NewObject(
					value.Field{Key: "start_time", Value: value.Float(1)},
					value.Field{Key: "end_time", Value: value.Float(0)},
					value.Field{Key: "title", Value: value.String("Main")},
				)),
			),
			spec: "^Intro$",
		},
		{
			name:     "entire media removal",
			duration: 4,
			chapters: value.Null(),
			spec:     "*0-4",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			mediaPath := generateChapterRemovalMedia(t, 4)
			beforeMedia, err := os.ReadFile(mediaPath)
			if err != nil {
				t.Fatal(err)
			}
			program, err := chapterremove.Parse([]string{test.spec})
			if err != nil {
				t.Fatal(err)
			}
			info := value.NewInfo(value.NewObject(
				value.Field{Key: "title", Value: value.String("Safety")},
				value.Field{Key: "duration", Value: value.Float(test.duration)},
				value.Field{Key: "chapters", Value: test.chapters},
			))
			beforeInfo, err := encodeInfo(info)
			if err != nil {
				t.Fatal(err)
			}
			operation := &operation{
				client: newBroadTestClient(), request: Request{RemoveChapters: []string{test.spec}},
				compatibility: compatibilityPlan{chapterRemoval: program},
			}
			_, _, cut, err := operation.applyChapterCuts(context.Background(), &info, mediaPath, nil, nil)
			if err == nil || !IsCategory(err, ErrorInvalidInput) || cut {
				t.Fatalf("cut = %t, err = %v", cut, err)
			}
			afterMedia, readErr := os.ReadFile(mediaPath)
			if readErr != nil {
				t.Fatal(readErr)
			}
			afterInfo, encodeErr := encodeInfo(info)
			if encodeErr != nil {
				t.Fatal(encodeErr)
			}
			if string(afterMedia) != string(beforeMedia) || string(afterInfo) != string(beforeInfo) {
				t.Fatal("safety failure mutated media or metadata")
			}
		})
	}
}

func TestChapterRemovalCombinesSponsorBlockOrdinaryAndManualCuts(t *testing.T) {
	mediaPath := generateChapterRemovalMedia(t, 6)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`[{"videoID":"abc","segments":[
			{"segment":[4,5],"category":"sponsor","actionType":"skip","videoDuration":6}
		]}]`))
	}))
	defer server.Close()
	transport, err := network.New(network.Config{})
	if err != nil {
		t.Fatal(err)
	}
	defer transport.CloseIdleConnections()
	request := Request{
		RemoveChapters: []string{"^Intro$", "*2-3"},
		SponsorBlock: SponsorBlockOptions{
			Enabled: true, Mark: true, Remove: true,
			Categories: []string{"sponsor"}, APIBase: server.URL,
		},
	}
	compatibility, err := prepareCompatibility(request)
	if err != nil {
		t.Fatal(err)
	}
	info := value.NewInfo(value.NewObject(
		value.Field{Key: "id", Value: value.String("abc")},
		value.Field{Key: "title", Value: value.String("Combined")},
		value.Field{Key: "duration", Value: value.Float(6)},
		value.Field{Key: "chapters", Value: value.List(
			value.ObjectValue(value.NewObject(
				value.Field{Key: "start_time", Value: value.Float(0)},
				value.Field{Key: "end_time", Value: value.Float(1)},
				value.Field{Key: "title", Value: value.String("Intro")},
			)),
			value.ObjectValue(value.NewObject(
				value.Field{Key: "start_time", Value: value.Float(1)},
				value.Field{Key: "end_time", Value: value.Float(6)},
				value.Field{Key: "title", Value: value.String("Main")},
			)),
		)},
	))
	operation := &operation{
		client: newBroadTestClient(), request: request, compatibility: compatibility, transport: transport,
	}
	if err := operation.enrichWithSponsorBlock(context.Background(), "youtube", &info); err != nil {
		t.Fatal(err)
	}
	_, _, cut, err := operation.applyChapterCuts(context.Background(), &info, mediaPath, nil, nil)
	if err != nil || !cut {
		t.Fatalf("cut = %t, err = %v", cut, err)
	}
	duration, ok := sponsorblockDuration(info.Lookup("duration"))
	// The pinned arrangement merges the one-second trailing normal fragment
	// into the adjacent SponsorBlock cut.
	if !ok || duration < 1.7 || duration > 2.3 {
		t.Fatalf("combined duration = %v, %t", duration, ok)
	}
	tools, err := ffmpeg.Discover(ffmpeg.Config{})
	if err != nil {
		t.Fatal(err)
	}
	probe, err := tools.Probe(context.Background(), mediaPath)
	if err != nil {
		t.Fatal(err)
	}
	actual, err := strconv.ParseFloat(probe.Format.Duration, 64)
	if err != nil || actual < 1.7 || actual > 2.3 {
		t.Fatalf("combined media duration = %v, err = %v", actual, err)
	}
}

func TestProductRemovesChapterAndRetimesSubtitle(t *testing.T) {
	mediaPath := generateChapterRemovalMedia(t, 4)
	media, err := os.ReadFile(mediaPath)
	if err != nil {
		t.Fatal(err)
	}
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/page":
			writer.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprintf(writer, `{
				"id":"chapter-fixture","title":"Chapter Fixture","duration":4,"ext":"mp4",
				"formats":[{"format_id":"media","url":%q,"ext":"mp4","vcodec":"mpeg4","acodec":"aac"}],
				"chapters":[
					{"start_time":0,"end_time":1,"title":"Intro","custom":"preserved"},
					{"start_time":1,"end_time":4,"title":"Main"}
				],
				"subtitles":{"en":[{"url":%q,"ext":"vtt","name":"English"}]}
			}`, server.URL+"/media.mp4", server.URL+"/en.vtt")
		case "/media.mp4":
			writer.Header().Set("Content-Type", "video/mp4")
			writer.Header().Set("Content-Length", strconv.Itoa(len(media)))
			if request.Method != http.MethodHead {
				_, _ = writer.Write(media)
			}
		case "/en.vtt":
			_, _ = writer.Write([]byte("WEBVTT\n\n00:00.200 --> 00:00.800\nintro\n\n00:01.500 --> 00:02.500\nmain\n"))
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	result, err := newBroadTestClient().Run(ctx, Request{
		URL: server.URL + "/page", OutputDir: t.TempDir(),
		RemoveChapters:       []string{"^Intro$"},
		ForceKeyframesAtCuts: true,
		Subtitles: SubtitleOptions{
			WriteManual: true, Languages: []string{"en"}, Format: "vtt",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Downloaded || result.Filename == "" {
		t.Fatalf("result = %#v", result)
	}
	var metadata map[string]any
	if err := json.Unmarshal(result.InfoJSON, &metadata); err != nil {
		t.Fatal(err)
	}
	duration, _ := metadata["duration"].(float64)
	if duration != 3 {
		t.Fatalf("metadata duration = %#v", metadata["duration"])
	}
	chapters, _ := metadata["chapters"].([]any)
	if len(chapters) != 1 {
		t.Fatalf("chapters = %#v", metadata["chapters"])
	}
	main, _ := chapters[0].(map[string]any)
	if main["title"] != "Main" || main["start_time"] != float64(0) || main["end_time"] != float64(3) {
		t.Fatalf("main chapter = %#v", main)
	}
	var subtitlePath string
	for _, artifact := range result.Artifacts {
		if artifact.Kind == "subtitle" {
			subtitlePath = artifact.Path
		}
	}
	subtitle, err := os.ReadFile(subtitlePath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(subtitle), "intro") || !strings.Contains(string(subtitle), "00:00.500 --> 00:01.500") {
		t.Fatalf("retimed subtitle = %q", subtitle)
	}
	tools, err := ffmpeg.Discover(ffmpeg.Config{})
	if err != nil {
		t.Fatal(err)
	}
	probe, err := tools.Probe(ctx, result.Filename)
	if err != nil {
		t.Fatal(err)
	}
	actual, err := strconv.ParseFloat(probe.Format.Duration, 64)
	if err != nil || actual < 2.7 || actual > 3.3 {
		t.Fatalf("media duration = %v, err = %v", actual, err)
	}
}

func generateChapterRemovalMedia(t *testing.T, duration int) string {
	t.Helper()
	ffmpegPath, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg unavailable")
	}
	if _, err := exec.LookPath("ffprobe"); err != nil {
		t.Skip("ffprobe unavailable")
	}
	mediaPath := filepath.Join(t.TempDir(), "media.mp4")
	output, err := exec.Command(
		ffmpegPath,
		"-nostdin", "-y",
		"-f", "lavfi", "-i", fmt.Sprintf("color=c=black:s=32x32:d=%d", duration),
		"-f", "lavfi", "-i", fmt.Sprintf("sine=frequency=700:duration=%d", duration),
		"-shortest", "-c:v", "mpeg4", "-c:a", "aac", mediaPath,
	).CombinedOutput()
	if err != nil {
		t.Fatalf("generate media: %v: %s", err, output)
	}
	return mediaPath
}
