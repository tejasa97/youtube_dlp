package ytdlp

import "testing"

func TestMergeOutputFormatPreferences(t *testing.T) {
	if got := mergeOutputFormatPreferences("mp4/mkv"); len(got) != 2 || got[0] != "mp4" || got[1] != "mkv" {
		t.Fatalf("explicit = %#v", got)
	}
	if got := mergeOutputFormatPreferences(""); got != nil {
		t.Fatalf("empty = %#v, want nil", got)
	}
	if got := mergeOutputFormatPreferences(" / "); got != nil {
		t.Fatalf("blank = %#v, want nil", got)
	}
}
