package hls

import (
	"errors"
	"os"
	"strings"
	"testing"
	"time"
)

func TestParseMasterAndMedia(t *testing.T) {
	master, err := Parse("https://example.invalid/path/master.m3u8", []byte(`#EXTM3U
#EXT-X-STREAM-INF:BANDWIDTH=1000,CODECS="avc1,mp4a",RESOLUTION=640x360
low/media.m3u8
#EXT-X-STREAM-INF:BANDWIDTH=5000,RESOLUTION=1920x1080
/high.m3u8
`))
	if err != nil {
		t.Fatal(err)
	}
	if len(master.Variants) != 2 || master.Variants[0].URL != "https://example.invalid/path/low/media.m3u8" || master.Variants[0].Codecs != "avc1,mp4a" {
		t.Fatalf("variants = %#v", master.Variants)
	}

	media, err := Parse("https://example.invalid/live/media.m3u8", []byte(`#EXTM3U
#EXT-X-MEDIA-SEQUENCE:42
#EXT-X-TARGETDURATION:6
#EXT-X-MAP:URI="init.mp4",BYTERANGE="4@2"
#EXT-X-KEY:METHOD=AES-128,URI="key.bin",IV=0x1
#EXT-X-DISCONTINUITY
#EXTINF:5.5,
#EXT-X-BYTERANGE:6@5
blob.bin
#EXT-X-ENDLIST
`))
	if err != nil {
		t.Fatal(err)
	}
	segment := media.Media.Segments[0]
	if segment.Sequence != 42 || segment.Duration != 5500*time.Millisecond || segment.RangeStart != 5 || segment.RangeLength != 6 || !segment.Discontinuity {
		t.Fatalf("segment = %#v", segment)
	}
	if segment.Map.URL != "https://example.invalid/live/init.mp4" || segment.Key.URL != "https://example.invalid/live/key.bin" || len(segment.Key.IV) != 16 || segment.Key.IV[15] != 1 {
		t.Fatalf("map/key = %#v / %#v", segment.Map, segment.Key)
	}
}

func TestParseBoundsInputAndEntryCount(t *testing.T) {
	if _, err := Parse("https://example.invalid/media.m3u8", make([]byte, maxPlaylistBytes+1)); !errors.Is(err, ErrInvalidPlaylist) {
		t.Fatalf("oversized playlist error = %v", err)
	}
	input := "#EXTM3U\n" + strings.Repeat("segment.ts\n", maxPlaylistEntries+1)
	if _, err := Parse("https://example.invalid/media.m3u8", []byte(input)); !errors.Is(err, ErrInvalidPlaylist) {
		t.Fatalf("entry-bound error = %v", err)
	}
	adInput := "#EXTM3U\n#ANVATO-SEGMENT-INFO:type=ad\n" + strings.Repeat("ad.ts\n", maxPlaylistEntries+1)
	if _, err := Parse("https://example.invalid/media.m3u8", []byte(adInput)); !errors.Is(err, ErrInvalidPlaylist) {
		t.Fatalf("advertisement entry-bound error = %v", err)
	}
}

func TestParseLowLatencyPartsAndDeltaSkip(t *testing.T) {
	playlist, err := Parse("https://example.invalid/live/media.m3u8", []byte(`#EXTM3U
#EXT-X-MEDIA-SEQUENCE:40
#EXT-X-PART-INF:PART-TARGET=0.5
#EXT-X-SKIP:SKIPPED-SEGMENTS=2
#EXT-X-MAP:URI="init.mp4"
#EXT-X-PART:DURATION=0.5,URI="part.mp4",BYTERANGE="4@2",INDEPENDENT=YES
#EXT-X-PART:DURATION=0.5,URI="part.mp4",BYTERANGE="3"
#EXT-X-ENDLIST
`))
	if err != nil {
		t.Fatal(err)
	}
	media := playlist.Media
	if media == nil || media.PartTarget != 500*time.Millisecond || len(media.Segments) != 2 {
		t.Fatalf("media=%#v", media)
	}
	first, second := media.Segments[0], media.Segments[1]
	if !first.Partial || first.Sequence != 42 || first.PartIndex != 0 || first.RangeStart != 2 || first.RangeLength != 4 || first.Map == nil {
		t.Fatalf("first part=%#v", first)
	}
	if !second.Partial || second.Sequence != 42 || second.PartIndex != 1 || second.RangeStart != 6 || second.RangeLength != 3 {
		t.Fatalf("second part=%#v", second)
	}
}

func TestParseRejectsInvalidLowLatencyAttributes(t *testing.T) {
	for _, input := range []string{
		"#EXTM3U\n#EXT-X-MEDIA-SEQUENCE:-1\n",
		"#EXTM3U\n#EXT-X-PART-INF:PART-TARGET=0\n",
		"#EXTM3U\n#EXT-X-SKIP:SKIPPED-SEGMENTS=-1\n",
		"#EXTM3U\n#EXT-X-MEDIA-SEQUENCE:9223372036854775807\n#EXT-X-SKIP:SKIPPED-SEGMENTS=1\n",
		"#EXTM3U\n#EXT-X-PART:DURATION=0,URI=x\n",
		"#EXTM3U\n#EXT-X-PART:DURATION=1,URI=x,BYTERANGE=0\n",
	} {
		if _, err := Parse("https://example.invalid/live/media.m3u8", []byte(input)); !errors.Is(err, ErrInvalidPlaylist) {
			t.Fatalf("input=%q error=%v", input, err)
		}
	}
}

func FuzzParse(f *testing.F) {
	f.Add("https://example.invalid/media.m3u8", []byte("#EXTM3U\n#EXTINF:1,\nsegment.ts\n#EXT-X-ENDLIST\n"))
	f.Add("https://example.invalid/master.m3u8", []byte("#EXTM3U\n#EXT-X-STREAM-INF:BANDWIDTH=1\nmedia.m3u8\n"))
	f.Add("https://example.invalid/ads.m3u8", []byte("#EXTM3U\n#UPLYNK-SEGMENT,ad\n#EXT-X-PART:DURATION=0.5,URI=ad-part.ts\n#UPLYNK-SEGMENT,segment\n#EXTINF:1,\nmedia.ts\n"))
	f.Add("https://example.invalid/cue.m3u8", []byte("#EXTM3U\n#EXT-X-MEDIA-SEQUENCE:9\n#EXT-X-CUE-OUT:DURATION=2\n#EXT-X-PART:DURATION=0.5,URI=ad.0.ts\n#EXT-X-CUE-IN\n#EXTINF:1,\nmedia.ts\n#EXT-X-ENDLIST\n"))
	f.Add("https://example.invalid/cue-cont.m3u8", []byte("#EXTM3U\n#EXT-X-SKIP:SKIPPED-SEGMENTS=1\n#EXT-X-CUE-OUT-CONT:ElapsedTime=1\n#EXTINF:1,\nad.ts\n#EXT-X-CUE-IN\n#EXTINF:1,\nmedia.ts\n"))
	f.Fuzz(func(t *testing.T, rawURL string, input []byte) {
		if len(rawURL) > 4096 || len(input) > 1<<20 {
			t.Skip()
		}
		playlist, err := Parse(rawURL, input)
		if err != nil {
			return
		}
		assertPlaylistInvariants(t, playlist)

		// Marker advertisement state is local to each Parse call.
		reset, resetErr := Parse("https://example.invalid/reset.m3u8", []byte("#EXTM3U\n#EXTINF:1,\nmedia.ts\n#EXT-X-ENDLIST\n"))
		if resetErr != nil {
			t.Fatalf("reset parse failed: %v", resetErr)
		}
		if len(reset.Media.Segments) != 1 || reset.Media.Segments[0].Advertisement {
			t.Fatalf("advertisement state leaked across Parse: %#v", reset.Media.Segments)
		}
	})
}

func assertPlaylistInvariants(t *testing.T, playlist Playlist) {
	t.Helper()
	for _, variant := range playlist.Variants {
		assertSafeResolvedURL(t, variant.URL)
	}
	if playlist.Media == nil {
		return
	}
	lastSequence := int64(-1)
	lastPart := -1
	for index, segment := range playlist.Media.Segments {
		assertSafeResolvedURL(t, segment.URL)
		if segment.Map != nil {
			assertSafeResolvedURL(t, segment.Map.URL)
		}
		if segment.Key != nil && segment.Key.URL != "" {
			assertSafeResolvedURL(t, segment.Key.URL)
		}
		if segment.Sequence < 0 {
			t.Fatalf("segment[%d] sequence %d is negative", index, segment.Sequence)
		}
		if segment.Partial {
			if segment.PartIndex < 0 {
				t.Fatalf("segment[%d] part index %d is negative", index, segment.PartIndex)
			}
			// Part indexes advance only within one media sequence run. A later
			// EXT-X-MEDIA-SEQUENCE may legally restart numbering in fuzz input.
			if segment.Sequence == lastSequence && segment.PartIndex <= lastPart {
				t.Fatalf("segment[%d] part order regresses: %#v", index, segment)
			}
			lastPart = segment.PartIndex
		} else if segment.PartIndex != 0 {
			t.Fatalf("complete segment[%d] has non-zero part index: %#v", index, segment)
		} else {
			lastPart = -1
		}
		lastSequence = segment.Sequence
	}
}

func assertSafeResolvedURL(t *testing.T, raw string) {
	t.Helper()
	if raw == "" || strings.Contains(raw, "\x00") || strings.ContainsAny(raw, "\r\n") {
		t.Fatalf("unsafe resolved URL %q", raw)
	}
	if !(strings.HasPrefix(raw, "https://") || strings.HasPrefix(raw, "http://") ||
		strings.HasPrefix(raw, "file:") || strings.HasPrefix(raw, "/")) {
		// ResolveReference may yield scheme-relative or opaque forms for weird
		// inputs; reject only clearly transport-unsafe control material above.
		return
	}
}

func TestParseRejectsUnsupportedEncryption(t *testing.T) {
	_, err := Parse("https://example.invalid/media.m3u8", []byte("#EXTM3U\n#EXT-X-KEY:METHOD=SAMPLE-AES,URI=key\n#EXTINF:1,\nseg\n"))
	if !errors.Is(err, ErrUnsupportedEncryption) {
		t.Fatalf("Parse() error = %v", err)
	}
}

func TestAdvertisementMarkerExactGrammar(t *testing.T) {
	for _, test := range []struct {
		trimmed, raw string
		start, end   bool
	}{
		{"#ANVATO-SEGMENT-INFO:type=ad", "#ANVATO-SEGMENT-INFO:type=ad", true, false},
		{"#ANVATO-SEGMENT-INFORMATION:x,type=advertisement", "#ANVATO-SEGMENT-INFORMATION:x,type=advertisement", true, false},
		{"#ANVATO-SEGMENT-INFO:type=master", "#ANVATO-SEGMENT-INFO:type=master", false, true},
		{"#ANVATO-SEGMENT-INFO:type=master,type=ad", "#ANVATO-SEGMENT-INFO:type=master,type=ad", true, true},
		{"#UPLYNK-SEGMENT,ad", "#UPLYNK-SEGMENT,ad", true, false},
		{"#UPLYNK-SEGMENT:anything,ad", "#UPLYNK-SEGMENT:anything,ad", true, false},
		{"#UPLYNK-SEGMENT,segment", "#UPLYNK-SEGMENT,segment", false, true},
		{"#UPLYNK-SEGMENT:anything,segment", "#UPLYNK-SEGMENT:anything,segment", false, true},
		{"#UPLYNK-SEGMENT,ad ", "#UPLYNK-SEGMENT,ad ", false, false},
		{"#UPLYNK-SEGMENT,segment,extra", "#UPLYNK-SEGMENT,segment,extra", false, false},
		{"#uplynk-SEGMENT,ad", "#uplynk-SEGMENT,ad", false, false},
		{"#ANVATO-SEGMENT-INFO:TYPE=AD", "#ANVATO-SEGMENT-INFO:TYPE=AD", false, false},
		{"#EXT-X-CUE-OUT", "#EXT-X-CUE-OUT", true, false},
		{"#EXT-X-CUE-OUT:", "#EXT-X-CUE-OUT:", true, false},
		{"#EXT-X-CUE-OUT:DURATION=30.000", "#EXT-X-CUE-OUT:DURATION=30.000", true, false},
		{"#EXT-X-CUE-OUT:30.0", "#EXT-X-CUE-OUT:30.0", true, false},
		{"#EXT-X-CUE-OUT:type=ad", "#EXT-X-CUE-OUT:type=ad", true, false},
		{"#EXT-X-CUE-OUT", "#EXT-X-CUE-OUT  ", true, false},
		{"#EXT-X-CUE-OUT", "#EXT-X-CUE-OUT\t", true, false},
		{"#EXT-X-CUE-OUT-CONT", "#EXT-X-CUE-OUT-CONT", true, false},
		{"#EXT-X-CUE-OUT-CONT:ElapsedTime=2.5,Duration=30", "#EXT-X-CUE-OUT-CONT:ElapsedTime=2.5,Duration=30", true, false},
		{"#EXT-X-CUE-OUT-CONT", "#EXT-X-CUE-OUT-CONT  ", true, false},
		{"#EXT-X-CUE-IN", "#EXT-X-CUE-IN", false, true},
		{"#EXT-X-CUE-IN:", "#EXT-X-CUE-IN:", false, true},
		{"#EXT-X-CUE-IN:ignored", "#EXT-X-CUE-IN:ignored", false, true},
		{"#EXT-X-CUE-IN", "#EXT-X-CUE-IN  ", false, true},
		{"#ext-x-cue-out", "#ext-x-cue-out", false, false},
		{"#EXT-X-CUE-OUTING", "#EXT-X-CUE-OUTING", false, false},
		{"#EXT-X-CUE-OUT-CONTINUE", "#EXT-X-CUE-OUT-CONTINUE", false, false},
		{"#EXT-X-CUE-OUTCONT", "#EXT-X-CUE-OUTCONT", false, false},
		{"#EXT-X-CUE-IN-PROGRESS", "#EXT-X-CUE-IN-PROGRESS", false, false},
		{"# EXT-X-CUE-OUT", "# EXT-X-CUE-OUT", false, false},
		{"#EXT-X-CUE-OUT", " #EXT-X-CUE-OUT", false, false},
		{"#EXT-X-CUE-OUT", "\t#EXT-X-CUE-OUT", false, false},
		{"#EXT-X-CUE-OUT-CONT", " #EXT-X-CUE-OUT-CONT", false, false},
		{"#EXT-X-CUE-OUT-CONT", "\t#EXT-X-CUE-OUT-CONT", false, false},
		{"#EXT-X-CUE-IN", " #EXT-X-CUE-IN", false, false},
		{"#EXT-X-CUE-IN", "\t#EXT-X-CUE-IN", false, false},
		{"#EXT-X-CUE", "#EXT-X-CUE", false, false},
		{"#EXT-X-DATERANGE:CLASS=ad", "#EXT-X-DATERANGE:CLASS=ad", false, false},
		{"#EXT-X-DATERANGE:SCTE35-OUT=0xFC", "#EXT-X-DATERANGE:SCTE35-OUT=0xFC", false, false},
		{"#EXT-X-SCTE35:CUE-OUT=YES", "#EXT-X-SCTE35:CUE-OUT=YES", false, false},
	} {
		if start := isAdvertisementStart(test.trimmed, test.raw); start != test.start {
			t.Fatalf("start(trimmed=%q raw=%q)=%v want %v", test.trimmed, test.raw, start, test.start)
		}
		if end := isAdvertisementEnd(test.trimmed, test.raw); end != test.end {
			t.Fatalf("end(trimmed=%q raw=%q)=%v want %v", test.trimmed, test.raw, end, test.end)
		}
	}
}

func TestParseAdvertisementStateOrderSequencesAndReset(t *testing.T) {
	playlist, err := Parse("https://example.invalid/live.m3u8", []byte(`#EXTM3U
#EXT-X-MEDIA-SEQUENCE:7
  #ANVATO-SEGMENT-INFO:type=ad
#EXT-X-PART:DURATION=0.25,URI="ad-7-part.bin"
#EXTINF:1,
ad-7.bin
#ANVATO-SEGMENT-INFO:type=master
#ANVATO-SEGMENT-INFO:type=master,type=ad
#EXTINF:1,
ad-8.bin
#UPLYNK-SEGMENT,segment
#EXTINF:1,
media-9.bin
   #UPLYNK-SEGMENT,ad
#EXTINF:1,
ad-10.bin
#EXT-X-ENDLIST
`))
	if err != nil {
		t.Fatal(err)
	}
	segments := playlist.Media.Segments
	if len(segments) != 5 {
		t.Fatalf("segments=%#v", segments)
	}
	wantSequences := []int64{7, 7, 8, 9, 10}
	wantParts := []int{0, 0, 0, 0, 0}
	wantPartial := []bool{true, false, false, false, false}
	wantAds := []bool{true, true, true, false, true}
	for index, segment := range segments {
		if segment.Sequence != wantSequences[index] || segment.PartIndex != wantParts[index] ||
			segment.Partial != wantPartial[index] || segment.Advertisement != wantAds[index] {
			t.Fatalf("segment[%d]=%#v", index, segment)
		}
	}

	reset, err := Parse("https://example.invalid/next.m3u8", []byte("#EXTM3U\n#EXTINF:1,\nmedia.bin\n#EXT-X-ENDLIST\n"))
	if err != nil || len(reset.Media.Segments) != 1 || reset.Media.Segments[0].Advertisement {
		t.Fatalf("marker state leaked across Parse: playlist=%#v err=%v", reset, err)
	}
}

func TestParseCueAdvertisementStateBoundariesAndOrdering(t *testing.T) {
	playlist, err := Parse("https://example.invalid/cue.m3u8", []byte(`#EXTM3U
#EXT-X-MEDIA-SEQUENCE:20
#EXTINF:1,
media-20.bin
#EXT-X-CUE-OUT:DURATION=4.0
#EXT-X-PART:DURATION=0.5,URI="ad-21.0.bin"
#EXT-X-CUE-OUT-CONT:ElapsedTime=0.5,Duration=4.0
#EXT-X-PART:DURATION=0.5,URI="ad-21.1.bin"
#EXTINF:1,
ad-21.bin
#EXT-X-CUE-IN
#EXTINF:1,
media-22.bin
#EXT-X-CUE-IN
#EXT-X-CUE-OUT
#EXTINF:1,
ad-23.bin
#EXT-X-CUE-OUT
#EXT-X-CUE-IN
#EXTINF:1,
media-24.bin
#EXT-X-ENDLIST
`))
	if err != nil {
		t.Fatal(err)
	}
	segments := playlist.Media.Segments
	if len(segments) != 7 {
		t.Fatalf("segments=%#v", segments)
	}
	want := []struct {
		sequence int64
		partial  bool
		part     int
		ad       bool
	}{
		{20, false, 0, false},
		{21, true, 0, true},
		{21, true, 1, true},
		{21, false, 0, true},
		{22, false, 0, false},
		{23, false, 0, true},
		{24, false, 0, false},
	}
	for index, segment := range segments {
		expect := want[index]
		if segment.Sequence != expect.sequence || segment.Partial != expect.partial ||
			segment.PartIndex != expect.part || segment.Advertisement != expect.ad {
			t.Fatalf("segment[%d]=%#v want seq=%d partial=%v part=%d ad=%v",
				index, segment, expect.sequence, expect.partial, expect.part, expect.ad)
		}
	}

	midBreak, err := Parse("https://example.invalid/delta.m3u8", []byte(`#EXTM3U
#EXT-X-MEDIA-SEQUENCE:100
#EXT-X-SKIP:SKIPPED-SEGMENTS=3
#EXT-X-CUE-OUT-CONT:ElapsedTime=1.5,Duration=6
#EXTINF:1,
ad-103.bin
#EXT-X-CUE-IN
#EXTINF:1,
media-104.bin
#EXT-X-ENDLIST
`))
	if err != nil {
		t.Fatal(err)
	}
	if len(midBreak.Media.Segments) != 2 ||
		!midBreak.Media.Segments[0].Advertisement || midBreak.Media.Segments[0].Sequence != 103 ||
		midBreak.Media.Segments[1].Advertisement || midBreak.Media.Segments[1].Sequence != 104 {
		t.Fatalf("OUT-CONT mid-break=%#v", midBreak.Media.Segments)
	}

	reset, err := Parse("https://example.invalid/next.m3u8", []byte("#EXTM3U\n#EXTINF:1,\nmedia.bin\n#EXT-X-ENDLIST\n"))
	if err != nil || len(reset.Media.Segments) != 1 || reset.Media.Segments[0].Advertisement {
		t.Fatalf("cue marker state leaked across Parse: playlist=%#v err=%v", reset, err)
	}
}

func TestParseCueLeadingWhitespaceRejectedTrailingAccepted(t *testing.T) {
	playlist, err := Parse("https://example.invalid/cue-ws.m3u8", []byte("#EXTM3U\n"+
		"#EXT-X-MEDIA-SEQUENCE:1\n"+
		"#EXTINF:1,\n"+
		"media-1.bin\n"+
		" #EXT-X-CUE-OUT\n"+
		"#EXTINF:1,\n"+
		"media-2.bin\n"+
		"\t#EXT-X-CUE-OUT-CONT\n"+
		"#EXTINF:1,\n"+
		"media-3.bin\n"+
		"#EXT-X-CUE-OUT  \n"+
		"#EXTINF:1,\n"+
		"ad-4.bin\n"+
		" #EXT-X-CUE-IN\n"+
		"#EXTINF:1,\n"+
		"ad-5.bin\n"+
		"\t#EXT-X-CUE-IN\n"+
		"#EXTINF:1,\n"+
		"ad-6.bin\n"+
		"#EXT-X-CUE-IN\t\n"+
		"#EXTINF:1,\n"+
		"media-7.bin\n"+
		"#EXT-X-ENDLIST\n"))
	if err != nil {
		t.Fatal(err)
	}
	segments := playlist.Media.Segments
	if len(segments) != 7 {
		t.Fatalf("segments=%#v", segments)
	}
	wantAds := []bool{false, false, false, true, true, true, false}
	for index, segment := range segments {
		if segment.Advertisement != wantAds[index] || segment.Sequence != int64(index+1) {
			t.Fatalf("segment[%d]=%#v want ad=%v seq=%d", index, segment, wantAds[index], index+1)
		}
	}
}

func TestParseAdvertisementDeltaPartsPreserveMetadataAndRanges(t *testing.T) {
	playlist, err := Parse("https://example.invalid/live/media.m3u8", []byte(`#EXTM3U
#EXT-X-MEDIA-SEQUENCE:100
#EXT-X-SKIP:SKIPPED-SEGMENTS=2
#EXT-X-MAP:URI="init.mp4",BYTERANGE="8@3"
#EXT-X-KEY:METHOD=AES-128,URI="key.bin"
#EXT-X-DISCONTINUITY
#UPLYNK-SEGMENT,ad
#EXT-X-PART:DURATION=0.5,URI="parts.bin",BYTERANGE="4@10"
#UPLYNK-SEGMENT,segment
#EXT-X-PART:DURATION=0.5,URI="parts.bin",BYTERANGE="6"
#EXT-X-BYTERANGE:9@20
#EXTINF:1,
complete.bin
#EXT-X-ENDLIST
`))
	if err != nil {
		t.Fatal(err)
	}
	segments := playlist.Media.Segments
	if len(segments) != 3 {
		t.Fatalf("segments=%#v", segments)
	}
	adPart, mediaPart, complete := segments[0], segments[1], segments[2]
	if adPart.Sequence != 102 || adPart.PartIndex != 0 || !adPart.Partial || !adPart.Advertisement ||
		!adPart.Discontinuity || adPart.RangeStart != 10 || adPart.RangeLength != 4 {
		t.Fatalf("ad part=%#v", adPart)
	}
	if mediaPart.Sequence != 102 || mediaPart.PartIndex != 1 || !mediaPart.Partial || mediaPart.Advertisement ||
		mediaPart.Discontinuity || mediaPart.RangeStart != 14 || mediaPart.RangeLength != 6 {
		t.Fatalf("media part=%#v", mediaPart)
	}
	if complete.Sequence != 102 || complete.Partial || complete.Advertisement ||
		complete.RangeStart != 20 || complete.RangeLength != 9 {
		t.Fatalf("complete=%#v", complete)
	}
	for index, segment := range segments {
		if segment.Map == nil || segment.Map.URL != "https://example.invalid/live/init.mp4" ||
			segment.Key == nil || segment.Key.URL != "https://example.invalid/live/key.bin" {
			t.Fatalf("segment[%d] map/key=%#v/%#v", index, segment.Map, segment.Key)
		}
	}
}

func TestParseCueAdvertisementMapKeyDiscontinuityAndAdOnly(t *testing.T) {
	playlist, err := Parse("https://example.invalid/live/media.m3u8", []byte(`#EXTM3U
#EXT-X-MEDIA-SEQUENCE:5
#EXT-X-CUE-OUT
#EXT-X-DISCONTINUITY
#EXT-X-MAP:URI="ad-init.mp4"
#EXT-X-KEY:METHOD=AES-128,URI="ad-key.bin"
#EXT-X-PART:DURATION=0.5,URI="ad-part.bin",BYTERANGE="4@1"
#EXTINF:1,
ad-5.bin
#EXT-X-CUE-IN
#EXT-X-MAP:URI="media-init.mp4"
#EXT-X-KEY:METHOD=AES-128,URI="media-key.bin",IV=0x2
#EXTINF:1,
media-6.bin
#EXT-X-ENDLIST
`))
	if err != nil {
		t.Fatal(err)
	}
	segments := playlist.Media.Segments
	if len(segments) != 3 {
		t.Fatalf("segments=%#v", segments)
	}
	if !segments[0].Advertisement || !segments[0].Partial || !segments[0].Discontinuity ||
		segments[0].Map == nil || segments[0].Key == nil || segments[0].Sequence != 5 {
		t.Fatalf("cue ad part=%#v", segments[0])
	}
	if !segments[1].Advertisement || segments[1].Partial || segments[1].Sequence != 5 {
		t.Fatalf("cue ad complete=%#v", segments[1])
	}
	if segments[2].Advertisement || segments[2].Sequence != 6 ||
		segments[2].Map.URL != "https://example.invalid/live/media-init.mp4" ||
		segments[2].Key.URL != "https://example.invalid/live/media-key.bin" {
		t.Fatalf("retained media=%#v", segments[2])
	}

	adOnly, err := Parse("https://example.invalid/ads.m3u8", []byte(`#EXTM3U
#EXT-X-CUE-OUT:DURATION=2
#EXTINF:1,
only-ad.bin
#EXT-X-CUE-OUT-CONT
#EXTINF:1,
still-ad.bin
#EXT-X-ENDLIST
`))
	if err != nil {
		t.Fatal(err)
	}
	if len(adOnly.Media.Segments) != 2 || !adOnly.Media.Segments[0].Advertisement || !adOnly.Media.Segments[1].Advertisement {
		t.Fatalf("ad-only playlist=%#v", adOnly.Media.Segments)
	}

	fixture, err := os.ReadFile("../../../conformance/media/hls_ads/delta-cue-midbreak.m3u8")
	if err != nil {
		t.Fatal(err)
	}
	delta, err := Parse("https://example.invalid/delta-cue-midbreak.m3u8", fixture)
	if err != nil {
		t.Fatal(err)
	}
	if len(delta.Media.Segments) != 4 {
		t.Fatalf("delta fixture segments=%#v", delta.Media.Segments)
	}
	for index, segment := range delta.Media.Segments[:3] {
		if !segment.Advertisement || segment.Sequence != 202 {
			t.Fatalf("delta ad[%d]=%#v", index, segment)
		}
	}
	if delta.Media.Segments[3].Advertisement || delta.Media.Segments[3].Sequence != 203 || delta.Media.Segments[3].Partial {
		t.Fatalf("delta retained=%#v", delta.Media.Segments[3])
	}
}

func FuzzAdvertisementMarkers(f *testing.F) {
	for _, seed := range []string{
		"#ANVATO-SEGMENT-INFO:type=ad",
		"#ANVATO-SEGMENT-INFO:type=master,type=ad",
		"#UPLYNK-SEGMENT,ad",
		"#UPLYNK-SEGMENT,segment",
		"#EXT-X-CUE-OUT",
		"#EXT-X-CUE-OUT:DURATION=30",
		"#EXT-X-CUE-OUT-CONT:ElapsedTime=1",
		"#EXT-X-CUE-IN",
		"#EXT-X-CUE-OUT  ",
		" #EXT-X-CUE-OUT",
		"\t#EXT-X-CUE-IN",
		"#EXT-X-CUE-OUTING",
		"#ext-x-cue-out",
		"# EXT-X-CUE-OUT",
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, line string) {
		if len(line) > 1<<20 {
			t.Skip()
		}
		trimmed := strings.TrimSpace(line)
		wantStart := (strings.HasPrefix(trimmed, "#ANVATO-SEGMENT-INFO") && strings.Contains(trimmed, "type=ad")) ||
			(strings.HasPrefix(trimmed, "#UPLYNK-SEGMENT") && strings.HasSuffix(trimmed, ",ad")) ||
			fuzzCueStart(line)
		wantEnd := (strings.HasPrefix(trimmed, "#ANVATO-SEGMENT-INFO") && strings.Contains(trimmed, "type=master")) ||
			(strings.HasPrefix(trimmed, "#UPLYNK-SEGMENT") && strings.HasSuffix(trimmed, ",segment")) ||
			fuzzCueEnd(line)
		if isAdvertisementStart(trimmed, line) != wantStart || isAdvertisementEnd(trimmed, line) != wantEnd {
			t.Fatalf("marker mismatch for %q", line)
		}
		if cue, ok := cueRawLine(line); ok {
			if cue == "#EXT-X-CUE-OUT-CONT" || strings.HasPrefix(cue, "#EXT-X-CUE-OUT-CONT:") {
				if !isCueAdvertisementStart(line) || isCueTagName(cue, "#EXT-X-CUE-OUT") {
					t.Fatalf("OUT-CONT incorrectly classified against bare OUT for %q", line)
				}
			}
		}
	})
}

func fuzzCueStart(raw string) bool {
	line, ok := cueRawLine(raw)
	if !ok {
		return false
	}
	return line == "#EXT-X-CUE-OUT-CONT" || strings.HasPrefix(line, "#EXT-X-CUE-OUT-CONT:") ||
		line == "#EXT-X-CUE-OUT" || strings.HasPrefix(line, "#EXT-X-CUE-OUT:")
}

func fuzzCueEnd(raw string) bool {
	line, ok := cueRawLine(raw)
	if !ok {
		return false
	}
	return line == "#EXT-X-CUE-IN" || strings.HasPrefix(line, "#EXT-X-CUE-IN:")
}
