package ytdlp

import "testing"

func TestMergeOutputPreferences(t *testing.T) {
	if got := mergeOutputPreferences("mp4/mkv", false); len(got) != 2 || got[0] != "mp4" || got[1] != "mkv" {
		t.Fatalf("explicit = %#v", got)
	}
	if got := mergeOutputPreferences("", true); len(got) != 2 || got[0] != "webm" {
		t.Fatalf("prefer free = %#v", got)
	}
}
