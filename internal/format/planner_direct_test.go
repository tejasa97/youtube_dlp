package format

import (
	"errors"
	"fmt"
	"testing"

	"github.com/tejasa97/ytdlp-go/internal/value"
)

func plannerFixtureDirect(values []plannerFormatFixture) []value.Value {
	return plannerFixtureToValues(values)
}

func TestPlannerCanonicalOrderingIsWorstToBest(t *testing.T) {
	formats := []plannerFormatFixture{
		{FormatID: "v-low", URL: "https://e/0", Ext: "mp4", VCodec: ptr("avc1"), ACodec: ptr("none"), Height: ptr(int64(360))},
		{FormatID: "v-high", URL: "https://e/1", Ext: "mp4", VCodec: ptr("avc1"), ACodec: ptr("none"), Height: ptr(int64(720))},
		{FormatID: "a-low", URL: "https://e/2", Ext: "m4a", VCodec: ptr("none"), ACodec: ptr("aac"), TBR: ptr(64.0)},
		{FormatID: "a-high", URL: "https://e/3", Ext: "m4a", VCodec: ptr("none"), ACodec: ptr("aac"), TBR: ptr(128.0)},
	}
	plans, err := plannerRunSelector(t, formats, "bestvideo+bestaudio", plannerSelectorOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(plans) != 1 {
		t.Fatalf("plan count = %d, want 1", len(plans))
	}
	ids := []string{plans[0].Tracks[0].ID, plans[0].Tracks[1].ID}
	if ids[0] != "v-high" || ids[1] != "a-high" {
		t.Fatalf("best/worst atoms produced %v, want [v-high a-high]", ids)
	}
}

func TestPlannerPreparedInfoImmutable(t *testing.T) {
	formats := plannerFixtureDirect([]plannerFormatFixture{
		{FormatID: "v", URL: "https://e/0", Ext: "mp4", VCodec: ptr("avc1"), ACodec: ptr("aac")},
	})
	info := value.NewInfo(value.NewObject(value.Field{Key: "formats", Value: value.List(formats...)}))
	prepared, err := Prepare(info, Options{})
	if err != nil {
		t.Fatal(err)
	}
	canonicalBefore := prepared.formats[0].ID
	parsed, err := ParseSelector("best")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := prepared.Plan(parsed); err != nil {
		t.Fatal(err)
	}
	if prepared.formats[0].ID != canonicalBefore {
		t.Fatalf("canonical format id changed after planning")
	}
}

func TestPlannerMetadataIndependence(t *testing.T) {
	formats := plannerFixtureDirect([]plannerFormatFixture{
		{FormatID: "v", URL: "https://e/0", Ext: "mp4", VCodec: ptr("avc1"), ACodec: ptr("none"), Height: ptr(int64(720))},
		{FormatID: "a", URL: "https://e/1", Ext: "m4a", VCodec: ptr("none"), ACodec: ptr("aac"), TBR: ptr(128.0)},
	})
	info := value.NewInfo(value.NewObject(value.Field{Key: "formats", Value: value.List(formats...)}))
	prepared, err := Prepare(info, Options{})
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := ParseSelector("bv+ba")
	if err != nil {
		t.Fatal(err)
	}
	plans, err := prepared.Plan(parsed)
	if err != nil {
		t.Fatal(err)
	}
	if len(plans) != 1 {
		t.Fatalf("plan count = %d, want 1", len(plans))
	}
	if plans[0].Metadata.Fields().Len() == 0 {
		t.Fatalf("merged metadata is empty")
	}
	plans[0].Metadata.Fields().Set("requested_formats", value.Missing())
	again, err := prepared.Plan(parsed)
	if err != nil {
		t.Fatal(err)
	}
	requestedValues, _ := again[0].Metadata.Lookup("requested_formats").ListValue()
	if len(requestedValues) != 2 {
		t.Fatalf("requested_formats lost between planning calls")
	}
}
func TestPlannerAvailabilityCacheScope(t *testing.T) {
	formats := plannerFixtureDirect([]plannerFormatFixture{
		{FormatID: "v1", URL: "https://e/0", Ext: "mp4", VCodec: ptr("avc1"), ACodec: ptr("none"), Height: ptr(int64(360))},
		{FormatID: "v2", URL: "https://e/1", Ext: "mp4", VCodec: ptr("avc1"), ACodec: ptr("none"), Height: ptr(int64(720))},
		{FormatID: "a1", URL: "https://e/2", Ext: "m4a", VCodec: ptr("none"), ACodec: ptr("aac"), TBR: ptr(128.0)},
	})
	info := value.NewInfo(value.NewObject(value.Field{Key: "formats", Value: value.List(formats...)}))
	calls := make(map[*value.Object]int)
	availability := FormatAvailabilityFunc(func(o *value.Object) (bool, error) {
		calls[o]++
		return true, nil
	})
	parsed, err := ParseSelector("bv+ba")
	if err != nil {
		t.Fatal(err)
	}
	opts := EvaluationOptions{Availability: availability}
	plans, err := PlanSelectWithEvaluationOptions(info, parsed, Options{}, opts)
	if err != nil {
		t.Fatal(err)
	}
	if len(plans) != 1 {
		t.Fatalf("plan count = %d, want 1", len(plans))
	}
	for _, count := range calls {
		if count != 1 {
			t.Errorf("availability called %d times for one canonical object", count)
		}
	}
}

func TestPlannerAvailabilityCacheFreshPerCall(t *testing.T) {
	formats := plannerFixtureDirect([]plannerFormatFixture{
		{FormatID: "v", URL: "https://e/0", Ext: "mp4", VCodec: ptr("avc1"), ACodec: ptr("none"), Height: ptr(int64(720))},
	})
	info := value.NewInfo(value.NewObject(value.Field{Key: "formats", Value: value.List(formats...)}))
	calls := 0
	availability := FormatAvailabilityFunc(func(o *value.Object) (bool, error) {
		calls++
		return true, nil
	})
	parsed, _ := ParseSelector("best")
	opts := EvaluationOptions{Availability: availability}
	for i := 0; i < 3; i++ {
		if _, err := PlanSelectWithEvaluationOptions(info, parsed, Options{}, opts); err != nil {
			t.Fatal(err)
		}
	}
	if calls != 3 {
		t.Errorf("availability called %d times across 3 plan calls, want 3", calls)
	}
}

func TestPlannerAvailabilityErrorPropagation(t *testing.T) {
	formats := plannerFixtureDirect([]plannerFormatFixture{
		{FormatID: "v", URL: "https://e/0", Ext: "mp4", VCodec: ptr("avc1"), ACodec: ptr("none"), Height: ptr(int64(720))},
	})
	info := value.NewInfo(value.NewObject(value.Field{Key: "formats", Value: value.List(formats...)}))
	want := errors.New("simulated availability failure")
	availability := FormatAvailabilityFunc(func(o *value.Object) (bool, error) {
		return false, want
	})
	parsed, _ := ParseSelector("best")
	_, err := PlanSelectWithEvaluationOptions(info, parsed, Options{}, EvaluationOptions{Availability: availability})
	if !errors.Is(err, want) {
		t.Errorf("availability error did not propagate: %v", err)
	}
}

func TestPlannerSelectorLimits(t *testing.T) {
	formats := make([]plannerFormatFixture, 65)
	for i := range formats {
		formats[i].FormatID = fmt.Sprintf("f%02d", i)
		formats[i].URL = fmt.Sprintf("https://e/%d", i)
		formats[i].Ext = "mp4"
		vcodec := "avc1"
		acodec := "aac"
		formats[i].VCodec = &vcodec
		formats[i].ACodec = &acodec
	}
	info := value.NewInfo(value.NewObject(value.Field{Key: "formats", Value: value.List(plannerFixtureDirect(formats)...)}))
	parsed, err := ParseSelector("all")
	if err != nil {
		t.Fatal(err)
	}
	_, err = PlanSelectWithOptions(info, parsed, Options{})
	if !errors.Is(err, ErrSelectorLimit) {
		t.Fatalf("expected ErrSelectorLimit for all outputs > 64, got %v", err)
	}
}

func TestPlannerMultistreamDefaultSuppresses(t *testing.T) {
	formats := plannerFixtureDirect([]plannerFormatFixture{
		{FormatID: "v1", URL: "https://e/0", Ext: "mp4", VCodec: ptr("avc1"), ACodec: ptr("none"), Height: ptr(int64(720))},
		{FormatID: "v2", URL: "https://e/1", Ext: "mp4", VCodec: ptr("avc1"), ACodec: ptr("none"), Height: ptr(int64(1080))},
		{FormatID: "a1", URL: "https://e/2", Ext: "m4a", VCodec: ptr("none"), ACodec: ptr("aac"), TBR: ptr(64.0)},
		{FormatID: "a2", URL: "https://e/3", Ext: "m4a", VCodec: ptr("none"), ACodec: ptr("aac"), TBR: ptr(128.0)},
	})
	info := value.NewInfo(value.NewObject(value.Field{Key: "formats", Value: value.List(formats...)}))
	parsed, _ := ParseSelector("bv+ba+bv+ba")
	plans, err := PlanSelectWithOptions(info, parsed, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if len(plans) != 1 {
		t.Fatalf("plan count = %d, want 1", len(plans))
	}
	// multistream suppression retains the first video (canonical order,
	// best-of-pair is v2 here because bestvideo picks the best by canonical
	// ordering) and first audio (a2). The pair is computed best+best.
	if len(plans[0].Tracks) != 2 {
		t.Fatalf("track count = %d, want 2", len(plans[0].Tracks))
	}
	if plans[0].Tracks[0].ID != "v2" || plans[0].Tracks[1].ID != "a2" {
		t.Fatalf("suppression retained %v, want [v2 a2]",
			[]string{plans[0].Tracks[0].ID, plans[0].Tracks[1].ID})
	}
}

func TestPlannerDefaultSelectorSpecMatchesPinnedPolicy(t *testing.T) {
	tests := []struct {
		name         string
		capabilities PlannerCapabilities
		context      DefaultSelectorContext
		options      Options
		want         string
	}{
		{
			name:         "vod-merger",
			capabilities: PlannerCapabilities{CanMergeFormats: true, OutputToStdout: false},
			context:      DefaultSelectorContext{IsLive: false, LiveFromStart: false},
			want:         "bestvideo*+bestaudio/best",
		},
		{
			name:         "no-merger",
			capabilities: PlannerCapabilities{CanMergeFormats: false, OutputToStdout: false},
			context:      DefaultSelectorContext{IsLive: false, LiveFromStart: false},
			want:         "best/bestvideo+bestaudio",
		},
		{
			name:         "stdout",
			capabilities: PlannerCapabilities{CanMergeFormats: true, OutputToStdout: true},
			context:      DefaultSelectorContext{IsLive: false, LiveFromStart: false},
			want:         "best/bestvideo+bestaudio",
		},
		{
			name:         "live-no-fromstart",
			capabilities: PlannerCapabilities{CanMergeFormats: true, OutputToStdout: false},
			context:      DefaultSelectorContext{IsLive: true, LiveFromStart: false},
			want:         "best/bestvideo+bestaudio",
		},
		{
			name:         "live-fromstart",
			capabilities: PlannerCapabilities{CanMergeFormats: true, OutputToStdout: false},
			context:      DefaultSelectorContext{IsLive: true, LiveFromStart: true},
			want:         "bestvideo*+bestaudio/best",
		},
		{
			name:         "multiple-audio-streams",
			capabilities: PlannerCapabilities{CanMergeFormats: true},
			context:      DefaultSelectorContext{},
			options:      Options{AllowMultipleAudioStreams: true},
			want:         "bestvideo+bestaudio/best",
		},
		{
			name:         "legacy-format-spec",
			capabilities: PlannerCapabilities{CanMergeFormats: true},
			context:      DefaultSelectorContext{LegacyFormatSpec: true},
			want:         "bestvideo+bestaudio/best",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := DefaultSelectorSpec(test.capabilities, test.context, test.options)
			if got != test.want {
				t.Fatalf("DefaultSelectorSpec = %q, want %q", got, test.want)
			}
		})
	}
}

func TestPlannerDefaultPolicyResolve(t *testing.T) {
	formats := plannerFixtureDirect([]plannerFormatFixture{
		{FormatID: "v", URL: "https://e/0", Ext: "mp4", VCodec: ptr("avc1"), ACodec: ptr("none"), Height: ptr(int64(720))},
		{FormatID: "a", URL: "https://e/1", Ext: "m4a", VCodec: ptr("none"), ACodec: ptr("aac"), TBR: ptr(128.0)},
	})
	info := value.NewInfo(value.NewObject(value.Field{Key: "formats", Value: value.List(formats...)}))
	prepared, err := Prepare(info, Options{})
	if err != nil {
		t.Fatal(err)
	}
	plans, err := prepared.DefaultWithContext(
		PlannerCapabilities{CanMergeFormats: true, OutputToStdout: false},
		DefaultSelectorContext{IsLive: false},
		EvaluationOptions{},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(plans) != 1 || len(plans[0].Tracks) != 2 {
		t.Fatalf("default plans = %+v", plans)
	}
}

func TestPlannerCompatibleExtensionPinnedCases(t *testing.T) {
	cases := []struct {
		name  string
		v     []string
		a     []string
		ve    []string
		ae    []string
		prefs []string
		want  string
	}{
		{"mp4", []string{"avc1.640028"}, []string{"mp4a.40.2"}, []string{"mp4"}, []string{"m4a"}, nil, "mp4"},
		{"webm", []string{"vp9"}, []string{"opus"}, []string{"webm"}, []string{"weba"}, nil, "webm"},
		{"incompatible", []string{"avc1"}, []string{"opus"}, []string{"mp4"}, []string{"webm"}, nil, "mkv"},
		{"multi-video", []string{"avc1", "avc1"}, []string{"mp4a"}, []string{"mp4", "mp4"}, []string{"m4a"}, nil, "mkv"},
		{"multi-audio", []string{"avc1"}, []string{"mp4a", "opus"}, []string{"mp4"}, []string{"m4a", "webm"}, nil, "mkv"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := compatibleExtension(c.v, c.a, c.ve, c.ae, c.prefs)
			if got != c.want {
				t.Errorf("compatibleExtension = %q, want %q", got, c.want)
			}
		})
	}
}

func ptr[T any](v T) *T { return &v }
