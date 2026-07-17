package hls

import (
	"errors"
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

func TestParseRejectsUnsupportedEncryption(t *testing.T) {
	_, err := Parse("https://example.invalid/media.m3u8", []byte("#EXTM3U\n#EXT-X-KEY:METHOD=SAMPLE-AES,URI=key\n#EXTINF:1,\nseg\n"))
	if !errors.Is(err, ErrUnsupportedEncryption) {
		t.Fatalf("Parse() error = %v", err)
	}
}
