package ytdlp

import (
	"testing"

	mediaformat "github.com/ytdlp-go/ytdlp/internal/format"
)

func TestMergedOutputExtensionFollowsSelectedTracks(t *testing.T) {
	video := func(ext string) mediaformat.Selection {
		return mediaformat.Selection{Ext: ext, VCodec: "video", ACodec: "none"}
	}
	audio := func(ext string) mediaformat.Selection {
		return mediaformat.Selection{Ext: ext, VCodec: "none", ACodec: "audio"}
	}
	for name, test := range map[string]struct {
		selections []mediaformat.Selection
		want       string
	}{
		"single":     {[]mediaformat.Selection{{Ext: "mp4", VCodec: "video", ACodec: "audio"}}, "mp4"},
		"mp4 pair":   {[]mediaformat.Selection{video("mp4"), audio("m4a")}, "mp4"},
		"webm pair":  {[]mediaformat.Selection{video("webm"), audio("webm")}, "webm"},
		"mixed pair": {[]mediaformat.Selection{video("mp4"), audio("webm")}, "mkv"},
	} {
		t.Run(name, func(t *testing.T) {
			if got := mergedOutputExtension(test.selections); got != test.want {
				t.Fatalf("mergedOutputExtension() = %q, want %q", got, test.want)
			}
		})
	}
}
