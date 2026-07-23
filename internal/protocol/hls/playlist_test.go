package hls

import (
	"errors"
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
	f.Fuzz(func(t *testing.T, rawURL string, input []byte) {
		if len(rawURL) > 4096 || len(input) > 1<<20 {
			t.Skip()
		}
		_, _ = Parse(rawURL, input)
	})
}

func TestParseRejectsUnsupportedEncryption(t *testing.T) {
	_, err := Parse("https://example.invalid/media.m3u8", []byte("#EXTM3U\n#EXT-X-KEY:METHOD=SAMPLE-AES,URI=key\n#EXTINF:1,\nseg\n"))
	if !errors.Is(err, ErrUnsupportedEncryption) {
		t.Fatalf("Parse() error = %v", err)
	}
}

func TestAdvertisementMarkerExactGrammar(t *testing.T) {
	for _, test := range []struct {
		line       string
		start, end bool
	}{
		{"#ANVATO-SEGMENT-INFO:type=ad", true, false},
		{"#ANVATO-SEGMENT-INFORMATION:x,type=advertisement", true, false},
		{"#ANVATO-SEGMENT-INFO:type=master", false, true},
		{"#ANVATO-SEGMENT-INFO:type=master,type=ad", true, true},
		{"#UPLYNK-SEGMENT,ad", true, false},
		{"#UPLYNK-SEGMENT:anything,ad", true, false},
		{"#UPLYNK-SEGMENT,segment", false, true},
		{"#UPLYNK-SEGMENT:anything,segment", false, true},
		{"#UPLYNK-SEGMENT,ad ", false, false},
		{"#UPLYNK-SEGMENT,segment,extra", false, false},
		{"#uplynk-SEGMENT,ad", false, false},
		{"#ANVATO-SEGMENT-INFO:TYPE=AD", false, false},
		{"#EXT-X-CUE-OUT:type=ad", false, false},
		{"#EXT-X-DATERANGE:CLASS=ad", false, false},
	} {
		if start := isAdvertisementStart(test.line); start != test.start {
			t.Fatalf("start(%q)=%v want %v", test.line, start, test.start)
		}
		if end := isAdvertisementEnd(test.line); end != test.end {
			t.Fatalf("end(%q)=%v want %v", test.line, end, test.end)
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

func FuzzAdvertisementMarkers(f *testing.F) {
	for _, seed := range []string{
		"#ANVATO-SEGMENT-INFO:type=ad",
		"#ANVATO-SEGMENT-INFO:type=master,type=ad",
		"#UPLYNK-SEGMENT,ad",
		"#UPLYNK-SEGMENT,segment",
		"#EXT-X-CUE-OUT:type=ad",
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, line string) {
		if len(line) > 1<<20 {
			t.Skip()
		}
		wantStart := (strings.HasPrefix(line, "#ANVATO-SEGMENT-INFO") && strings.Contains(line, "type=ad")) ||
			(strings.HasPrefix(line, "#UPLYNK-SEGMENT") && strings.HasSuffix(line, ",ad"))
		wantEnd := (strings.HasPrefix(line, "#ANVATO-SEGMENT-INFO") && strings.Contains(line, "type=master")) ||
			(strings.HasPrefix(line, "#UPLYNK-SEGMENT") && strings.HasSuffix(line, ",segment"))
		if isAdvertisementStart(line) != wantStart || isAdvertisementEnd(line) != wantEnd {
			t.Fatalf("marker mismatch for %q", line)
		}
	})
}
