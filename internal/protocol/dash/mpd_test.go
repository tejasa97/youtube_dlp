package dash

import (
	"testing"
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
