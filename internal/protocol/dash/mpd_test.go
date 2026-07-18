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

func TestParseSegmentBaseSingleFileAndRejectsSIDX(t *testing.T) {
	mpd, err := Parse("https://example.test/manifest.mpd", []byte(`<MPD><Period><AdaptationSet mimeType="video/mp4"><Representation id="v"><BaseURL>video.mp4</BaseURL><SegmentBase><Initialization range="0-99"/></SegmentBase></Representation></AdaptationSet></Period></MPD>`))
	if err != nil {
		t.Fatal(err)
	}
	if got := mpd.Representations[0].Segments; len(got) != 1 || got[0].URL != "https://example.test/video.mp4" || got[0].RangeLength != 0 {
		t.Fatalf("segments = %#v", got)
	}
	_, err = Parse("https://example.test/manifest.mpd", []byte(`<MPD><Period><AdaptationSet mimeType="video/mp4"><Representation id="v"><BaseURL>video.mp4</BaseURL><SegmentBase indexRange="100-200"/></Representation></AdaptationSet></Period></MPD>`))
	if !errors.Is(err, ErrUnsupportedAddressing) {
		t.Fatalf("Parse() error = %v", err)
	}
}

func TestParseRejectsUnboundedNegativeRepeat(t *testing.T) {
	_, err := Parse("https://example.test/live.mpd", []byte(`<MPD type="dynamic"><Period><AdaptationSet contentType="video"><Representation id="v"><SegmentTemplate media="$Time$.m4s"><SegmentTimeline><S t="0" d="2" r="-1"/></SegmentTimeline></SegmentTemplate></Representation></AdaptationSet></Period></MPD>`))
	if !errors.Is(err, ErrUnsupportedTimeline) {
		t.Fatalf("Parse() error = %v", err)
	}
}

func FuzzParse(f *testing.F) {
	f.Add("https://example.test/manifest.mpd", []byte(`<MPD mediaPresentationDuration="PT2S"><Period><AdaptationSet mimeType="video/mp4"><Representation id="v"><SegmentTemplate duration="1" media="$Number$.m4s"/></Representation></AdaptationSet></Period></MPD>`))
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
