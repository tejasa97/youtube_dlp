package dash

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestParseBaseInheritanceTemplatesAndSegmentList(t *testing.T) {
	mpd, err := Parse("https://example.invalid/root/manifest.mpd", []byte(`
<MPD type="static" mediaPresentationDuration="PT6S">
  <BaseURL>cdn/</BaseURL>
  <Period>
    <AdaptationSet contentType="video" mimeType="video/mp4">
      <SegmentTemplate timescale="1" initialization="init-$RepresentationID$.mp4" media="v-$Number%03d$.m4s" startNumber="5">
        <SegmentTimeline><S t="0" d="2" r="2"/></SegmentTimeline>
      </SegmentTemplate>
      <Representation id="v1" bandwidth="1000" width="1280" height="720"/>
    </AdaptationSet>
    <AdaptationSet contentType="audio" mimeType="audio/mp4">
      <Representation id="a1" bandwidth="128">
        <BaseURL>audio/</BaseURL>
        <SegmentList>
          <Initialization sourceURL="all.bin" range="0-3"/>
          <SegmentURL media="all.bin" mediaRange="4-7"/>
        </SegmentList>
      </Representation>
    </AdaptationSet>
  </Period>
</MPD>`))
	if err != nil {
		t.Fatal(err)
	}
	if len(mpd.Representations) != 2 {
		t.Fatalf("representations = %#v", mpd.Representations)
	}
	video := mpd.Representations[0]
	if got := video.Segments[0].URL; got != "https://example.invalid/root/cdn/init-v1.mp4" {
		t.Fatalf("video init = %q", got)
	}
	if got := video.Segments[3].URL; got != "https://example.invalid/root/cdn/v-007.m4s" {
		t.Fatalf("video segment = %q", got)
	}
	audio := mpd.Representations[1]
	if audio.Segments[0].RangeStart != 0 || audio.Segments[0].RangeLength != 4 || audio.Segments[1].RangeStart != 4 || audio.Segments[1].RangeLength != 4 {
		t.Fatalf("audio ranges = %#v", audio.Segments)
	}
}

func TestParseDurationTemplate(t *testing.T) {
	mpd, err := Parse("https://example.invalid/m.mpd", []byte(`<MPD mediaPresentationDuration="PT5.5S"><Period><AdaptationSet mimeType="video/mp4"><Representation id="v" bandwidth="1"><SegmentTemplate timescale="10" duration="20" media="$Number$.m4s"/></Representation></AdaptationSet></Period></MPD>`))
	if err != nil {
		t.Fatal(err)
	}
	if got := len(mpd.Representations[0].Segments); got != 3 {
		t.Fatalf("segment count = %d", got)
	}
}

func TestParseInheritedNegativeRepeatDynamicTimeline(t *testing.T) {
	fixtureRoot := filepath.Join("..", "..", "..", "conformance", "media", "dash")
	input, err := os.ReadFile(filepath.Join(fixtureRoot, "negative_repeat.mpd"))
	if err != nil {
		t.Fatal(err)
	}
	var expected struct {
		Dynamic               bool     `json:"dynamic"`
		MinimumUpdatePeriodMS int64    `json:"minimum_update_period_ms"`
		RepresentationID      string   `json:"representation_id"`
		URLs                  []string `json:"urls"`
	}
	expectedBytes, err := os.ReadFile(filepath.Join(fixtureRoot, "negative_repeat.expected.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(expectedBytes, &expected); err != nil {
		t.Fatal(err)
	}
	mpd, err := Parse("https://media.example.test/live/manifest.mpd", input)
	if err != nil {
		t.Fatal(err)
	}
	if mpd.Dynamic != expected.Dynamic || mpd.MinimumUpdatePeriod != time.Duration(expected.MinimumUpdatePeriodMS)*time.Millisecond {
		t.Fatalf("manifest timing = %#v", mpd)
	}
	if len(mpd.Representations) != 1 || mpd.Representations[0].ID != expected.RepresentationID {
		t.Fatalf("representations = %#v", mpd.Representations)
	}
	var urls []string
	for _, segment := range mpd.Representations[0].Segments {
		urls = append(urls, segment.URL)
	}
	if stringJSON(urls) != stringJSON(expected.URLs) {
		t.Fatalf("URLs = %v, want %v", urls, expected.URLs)
	}
}

func TestParseSegmentBaseSingleFileAndIndexRange(t *testing.T) {
	mpd, err := Parse("https://example.test/manifest.mpd", []byte(`<MPD><Period><AdaptationSet mimeType="video/mp4"><Representation id="v"><BaseURL>video.mp4</BaseURL><SegmentBase><Initialization range="0-99"/></SegmentBase></Representation></AdaptationSet></Period></MPD>`))
	if err != nil {
		t.Fatal(err)
	}
	if got := mpd.Representations[0].Segments; len(got) != 1 || got[0].URL != "https://example.test/video.mp4" || got[0].RangeLength != 0 {
		t.Fatalf("segments = %#v", got)
	}
	// indexRange now produces a marker segment for SIDX expansion.
	mpd, err = Parse("https://example.test/manifest.mpd", []byte(`<MPD><Period><AdaptationSet mimeType="video/mp4"><Representation id="v"><BaseURL>video.mp4</BaseURL><SegmentBase indexRange="100-200"/></Representation></AdaptationSet></Period></MPD>`))
	if err != nil {
		t.Fatal(err)
	}
	segments := mpd.Representations[0].Segments
	if len(segments) != 1 || segments[0].IndexRange != "100-200" || segments[0].URL != "https://example.test/video.mp4" {
		t.Fatalf("segments = %#v", segments)
	}
}

// TestParseSegmentBaseSingleFileAndRejectsSIDX is retained as a compatibility
// alias for the parity manifest reference. The behavior changed: indexRange is
// now accepted and produces a marker segment for SIDX expansion rather than
// being rejected.
func TestParseSegmentBaseSingleFileAndRejectsSIDX(t *testing.T) {
	TestParseSegmentBaseSingleFileAndIndexRange(t)
}

func TestParseRejectsUnboundedNegativeRepeat(t *testing.T) {
	_, err := Parse("https://example.test/live.mpd", []byte(`<MPD type="dynamic"><Period><AdaptationSet contentType="video"><Representation id="v"><SegmentTemplate media="$Time$.m4s"><SegmentTimeline><S t="0" d="2" r="-1"/></SegmentTimeline></SegmentTemplate></Representation></AdaptationSet></Period></MPD>`))
	if !errors.Is(err, ErrUnsupportedTimeline) {
		t.Fatalf("Parse() error = %v", err)
	}
}

func TestParseSegmentBaseIndexRangeRepresentationLevel(t *testing.T) {
	mpd, err := Parse("https://example.test/manifest.mpd", []byte(`<MPD><Period><AdaptationSet mimeType="video/mp4"><Representation id="v" bandwidth="1000"><BaseURL>video.mp4</BaseURL><SegmentBase indexRange="100-499"><Initialization range="0-99"/></SegmentBase></Representation></AdaptationSet></Period></MPD>`))
	if err != nil {
		t.Fatal(err)
	}
	segments := mpd.Representations[0].Segments
	if len(segments) != 1 {
		t.Fatalf("segments = %#v", segments)
	}
	if segments[0].IndexRange != "100-499" || segments[0].InitRange != "0-99" || segments[0].URL != "https://example.test/video.mp4" {
		t.Fatalf("segment = %#v", segments[0])
	}
}

func TestParseSegmentBaseIndexRangeInheritedPeriod(t *testing.T) {
	mpd, err := Parse("https://example.test/manifest.mpd", []byte(`<MPD><Period><SegmentBase indexRange="200-599"/><AdaptationSet mimeType="video/mp4"><Representation id="v" bandwidth="1000"><BaseURL>media.mp4</BaseURL></Representation></AdaptationSet></Period></MPD>`))
	if err != nil {
		t.Fatal(err)
	}
	segments := mpd.Representations[0].Segments
	if len(segments) != 1 || segments[0].IndexRange != "200-599" {
		t.Fatalf("segments = %#v", segments)
	}
}

func TestParseSegmentBaseIndexRangeInheritedAdaptationSet(t *testing.T) {
	mpd, err := Parse("https://example.test/manifest.mpd", []byte(`<MPD><Period><AdaptationSet mimeType="audio/mp4"><SegmentBase indexRange="50-199"><Initialization range="0-49"/></SegmentBase><Representation id="a" bandwidth="128"><BaseURL>audio.mp4</BaseURL></Representation></AdaptationSet></Period></MPD>`))
	if err != nil {
		t.Fatal(err)
	}
	segments := mpd.Representations[0].Segments
	if len(segments) != 1 || segments[0].IndexRange != "50-199" || segments[0].InitRange != "0-49" {
		t.Fatalf("segments = %#v", segments)
	}
}

func TestParseSegmentBaseFieldLevelInheritanceSplitFields(t *testing.T) {
	// indexRange at Period level, Initialization at Representation level.
	mpd, err := Parse("https://example.test/manifest.mpd", []byte(`<MPD><Period><SegmentBase indexRange="100-499"/><AdaptationSet mimeType="video/mp4"><Representation id="v" bandwidth="1000"><BaseURL>video.mp4</BaseURL><SegmentBase><Initialization range="0-99"/></SegmentBase></Representation></AdaptationSet></Period></MPD>`))
	if err != nil {
		t.Fatal(err)
	}
	segments := mpd.Representations[0].Segments
	if len(segments) != 1 {
		t.Fatalf("segments = %#v", segments)
	}
	if segments[0].IndexRange != "100-499" || segments[0].InitRange != "0-99" {
		t.Fatalf("segment = %#v, want IndexRange=100-499 InitRange=0-99", segments[0])
	}
}

func TestParseSegmentBaseFieldLevelInheritanceOverride(t *testing.T) {
	// Period sets indexRange="100-199", Representation overrides with "200-399".
	// AdaptationSet sets Initialization sourceURL, Representation overrides with
	// a different Initialization element (range only). Wholesale override means
	// the Representation's Initialization replaces the parent's entirely.
	mpd, err := Parse("https://example.test/manifest.mpd", []byte(`<MPD><Period><SegmentBase indexRange="100-199"/><AdaptationSet mimeType="video/mp4"><SegmentBase><Initialization sourceURL="init.mp4"/></SegmentBase><Representation id="v" bandwidth="1000"><BaseURL>video.mp4</BaseURL><SegmentBase indexRange="200-399"><Initialization range="0-49"/></SegmentBase></Representation></AdaptationSet></Period></MPD>`))
	if err != nil {
		t.Fatal(err)
	}
	segments := mpd.Representations[0].Segments
	// Representation indexRange overrides Period. Representation's Initialization
	// (range="0-49", no sourceURL) wholesale replaces AdaptationSet's
	// (sourceURL="init.mp4"). Result: same-resource init range on marker.
	if len(segments) != 1 {
		t.Fatalf("segments = %#v, want 1 marker segment", segments)
	}
	if segments[0].IndexRange != "200-399" || segments[0].InitRange != "0-49" {
		t.Fatalf("segment = %#v, want IndexRange=200-399 InitRange=0-49", segments[0])
	}
}

func TestParseSegmentBaseFieldLevelInheritanceInitSourceURLFromPeriod(t *testing.T) {
	// Period provides Initialization sourceURL, Representation provides indexRange.
	mpd, err := Parse("https://example.test/manifest.mpd", []byte(`<MPD><Period><SegmentBase><Initialization sourceURL="common_init.mp4" range="0-199"/></SegmentBase><AdaptationSet mimeType="video/mp4"><Representation id="v" bandwidth="1000"><BaseURL>video.mp4</BaseURL><SegmentBase indexRange="0-499"/></Representation></AdaptationSet></Period></MPD>`))
	if err != nil {
		t.Fatal(err)
	}
	segments := mpd.Representations[0].Segments
	if len(segments) != 2 {
		t.Fatalf("segments = %#v", segments)
	}
	if !segments[0].Initialize || segments[0].URL != "https://example.test/common_init.mp4" || segments[0].RangeLength != 200 {
		t.Fatalf("init segment = %#v", segments[0])
	}
	if segments[1].IndexRange != "0-499" || segments[1].URL != "https://example.test/video.mp4" {
		t.Fatalf("marker segment = %#v", segments[1])
	}
}

func TestParseSegmentBaseIndexRangeSeparateInitResource(t *testing.T) {
	mpd, err := Parse("https://example.test/manifest.mpd", []byte(`<MPD><Period><AdaptationSet mimeType="video/mp4"><Representation id="v" bandwidth="1000"><BaseURL>video.mp4</BaseURL><SegmentBase indexRange="0-999"><Initialization sourceURL="init.mp4" range="0-199"/></SegmentBase></Representation></AdaptationSet></Period></MPD>`))
	if err != nil {
		t.Fatal(err)
	}
	segments := mpd.Representations[0].Segments
	if len(segments) != 2 {
		t.Fatalf("segments = %#v", segments)
	}
	if !segments[0].Initialize || segments[0].URL != "https://example.test/init.mp4" || segments[0].RangeStart != 0 || segments[0].RangeLength != 200 {
		t.Fatalf("init segment = %#v", segments[0])
	}
	if segments[1].IndexRange != "0-999" || segments[1].URL != "https://example.test/video.mp4" {
		t.Fatalf("media segment = %#v", segments[1])
	}
}

func TestParseSegmentBaseIndexRangeSameResourceInit(t *testing.T) {
	mpd, err := Parse("https://example.test/manifest.mpd", []byte(`<MPD><Period><AdaptationSet mimeType="video/mp4"><Representation id="v" bandwidth="1000"><BaseURL>video.mp4</BaseURL><SegmentBase indexRange="200-999"><Initialization sourceURL="video.mp4" range="0-199"/></SegmentBase></Representation></AdaptationSet></Period></MPD>`))
	if err != nil {
		t.Fatal(err)
	}
	segments := mpd.Representations[0].Segments
	// Same-resource init is stored as InitRange on the marker segment.
	if len(segments) != 1 || segments[0].InitRange != "0-199" || segments[0].IndexRange != "200-999" {
		t.Fatalf("segments = %#v", segments)
	}
}

func TestParseSegmentBaseIndexRangeMalformedRanges(t *testing.T) {
	cases := []struct {
		name       string
		indexRange string
	}{
		{"reversed", "500-100"},
		{"negative start", "-1-200"},
		{"non-numeric", "abc-def"},
		{"missing dash", "100200"},
		{"overflow", "0-9223372036854775807"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Parse("https://example.test/m.mpd", []byte(`<MPD><Period><AdaptationSet mimeType="video/mp4"><Representation id="v"><BaseURL>v.mp4</BaseURL><SegmentBase indexRange="`+tc.indexRange+`"/></Representation></AdaptationSet></Period></MPD>`))
			if !errors.Is(err, ErrUnsupportedAddressing) {
				t.Fatalf("Parse() error = %v, want ErrUnsupportedAddressing", err)
			}
		})
	}
}

func TestParseSegmentBaseCoexistsWithTemplateAndList(t *testing.T) {
	mpd, err := Parse("https://example.test/manifest.mpd", []byte(`<MPD mediaPresentationDuration="PT2S"><Period>
<AdaptationSet contentType="video" mimeType="video/mp4"><SegmentTemplate duration="1" media="v-$Number$.m4s"/><Representation id="v" bandwidth="1000"/></AdaptationSet>
<AdaptationSet contentType="audio" mimeType="audio/mp4"><Representation id="a" bandwidth="128"><BaseURL>audio.mp4</BaseURL><SegmentBase indexRange="0-499"><Initialization range="0-99"/></SegmentBase></Representation></AdaptationSet>
</Period></MPD>`))
	if err != nil {
		t.Fatal(err)
	}
	if len(mpd.Representations) != 2 {
		t.Fatalf("representations = %d", len(mpd.Representations))
	}
	// Video uses template.
	if mpd.Representations[0].Segments[0].IndexRange != "" {
		t.Fatalf("video should use template, got %#v", mpd.Representations[0].Segments[0])
	}
	// Audio uses SegmentBase with indexRange.
	if mpd.Representations[1].Segments[0].IndexRange != "0-499" {
		t.Fatalf("audio segment = %#v", mpd.Representations[1].Segments[0])
	}
}

func TestParseMultiPeriodPreservesPeriodIdentity(t *testing.T) {
	// Parsing preserves each period's representations. The downloader performs
	// compatibility matching and ordered concatenation after format selection.
	mpd, err := Parse("https://example.test/manifest.mpd", []byte(`<MPD mediaPresentationDuration="PT4S"><Period start="PT0S" duration="PT2S"><AdaptationSet mimeType="video/mp4"><Representation id="p1v" bandwidth="1000"><BaseURL>p1.mp4</BaseURL><SegmentBase indexRange="0-99"/></Representation></AdaptationSet></Period><Period start="PT2S" duration="PT2S"><AdaptationSet mimeType="video/mp4"><Representation id="p2v" bandwidth="1000"><BaseURL>p2.mp4</BaseURL><SegmentBase indexRange="0-99"/></Representation></AdaptationSet></Period></MPD>`))
	if err != nil {
		t.Fatal(err)
	}
	if len(mpd.Representations) != 2 {
		t.Fatalf("representations = %d", len(mpd.Representations))
	}
	if mpd.Representations[0].ID != "p1v" || mpd.Representations[1].ID != "p2v" {
		t.Fatalf("IDs = %s, %s", mpd.Representations[0].ID, mpd.Representations[1].ID)
	}
	if mpd.PeriodCount != 2 || mpd.Representations[0].PeriodIndex != 0 || mpd.Representations[1].PeriodIndex != 1 {
		t.Fatalf("period identity = %#v", mpd.Representations)
	}
}

func FuzzParse(f *testing.F) {
	f.Add("https://example.test/manifest.mpd", []byte(`<MPD mediaPresentationDuration="PT2S"><Period><AdaptationSet mimeType="video/mp4"><Representation id="v"><SegmentTemplate duration="1" media="$Number$.m4s"/></Representation></AdaptationSet></Period></MPD>`))
	f.Add("https://example.test/manifest.mpd", []byte(`<MPD><Period id="one"><AdaptationSet contentType="video" mimeType="video/mp4"><Representation id="v1"><SegmentList><SegmentURL media="one.m4s"/></SegmentList></Representation></AdaptationSet></Period><Period id="two"><AdaptationSet contentType="video" mimeType="video/mp4"><Representation id="v2"><SegmentList><SegmentURL media="two.m4s"/></SegmentList></Representation></AdaptationSet></Period></MPD>`))
	f.Fuzz(func(t *testing.T, rawURL string, input []byte) {
		if len(rawURL) > 4096 || len(input) > 1<<20 {
			t.Skip()
		}
		_, _ = Parse(rawURL, input)
	})
}

func stringJSON(value any) string {
	encoded, _ := json.Marshal(value)
	return string(encoded)
}
