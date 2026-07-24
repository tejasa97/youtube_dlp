package sponsorblock

import (
	"strings"
	"testing"
)

func TestRemapCueIntervalDropsFullyRemoved(t *testing.T) {
	cuts := []Range{{Start: 10, End: 20}}
	if _, _, keep := RemapCueInterval(12, 18, cuts); keep {
		t.Fatal("cue entirely inside cut must drop")
	}
	start, end, keep := RemapCueInterval(5, 8, cuts)
	if !keep || start != 5 || end != 8 {
		t.Fatalf("pre-cut cue = %v-%v keep=%v", start, end, keep)
	}
	start, end, keep = RemapCueInterval(25, 30, cuts)
	if !keep || start != 15 || end != 20 {
		t.Fatalf("post-cut cue = %v-%v keep=%v", start, end, keep)
	}
	start, end, keep = RemapCueInterval(5, 15, cuts)
	if !keep || start != 5 || end != 10 {
		t.Fatalf("overlapping cue = %v-%v keep=%v", start, end, keep)
	}
}

func TestCutSRTTimingCases(t *testing.T) {
	cuts := []Range{{Start: 10, End: 20}}
	input := "" +
		"1\n00:00:05,000 --> 00:00:08,000\nbefore\n\n" +
		"2\n00:00:12,000 --> 00:00:18,000\ninside\n\n" +
		"3\n00:00:05,000 --> 00:00:15,000\noverlap\n\n" +
		"4\n00:00:25,000 --> 00:00:30,000\nafter\n"
	got, err := CutSubtitle("srt", []byte(input), cuts)
	if err != nil {
		t.Fatal(err)
	}
	text := string(got)
	if strings.Contains(text, "inside") {
		t.Fatalf("inside cue survived: %s", text)
	}
	if !strings.Contains(text, "before") || !strings.Contains(text, "overlap") || !strings.Contains(text, "after") {
		t.Fatalf("missing survivors: %s", text)
	}
	if !strings.Contains(text, "00:00:05,000 --> 00:00:08,000") {
		t.Fatalf("before timing wrong: %s", text)
	}
	if !strings.Contains(text, "00:00:05,000 --> 00:00:10,000") {
		t.Fatalf("overlap timing wrong: %s", text)
	}
	if !strings.Contains(text, "00:00:15,000 --> 00:00:20,000") {
		t.Fatalf("after timing wrong: %s", text)
	}
	// Indices must be renumbered densely.
	if !strings.HasPrefix(strings.TrimSpace(text), "1\n") || strings.Count(text, "\n2\n") != 1 || strings.Count(text, "\n3\n") != 1 {
		t.Fatalf("index rewrite wrong: %s", text)
	}
}

func TestCutVTTTimingCases(t *testing.T) {
	cuts := []Range{{Start: 10, End: 20}}
	input := "WEBVTT\n\n" +
		"00:05.000 --> 00:08.000\nbefore\n\n" +
		"00:12.000 --> 00:18.000\ninside\n\n" +
		"00:05.000 --> 00:15.000\noverlap\n\n" +
		"00:25.000 --> 00:30.000\nafter\n"
	got, err := CutSubtitle("vtt", []byte(input), cuts)
	if err != nil {
		t.Fatal(err)
	}
	text := string(got)
	if !strings.HasPrefix(text, "WEBVTT") {
		t.Fatalf("missing header: %s", text)
	}
	if strings.Contains(text, "inside") {
		t.Fatalf("inside cue survived: %s", text)
	}
	if !strings.Contains(text, "00:05.000 --> 00:08.000") || !strings.Contains(text, "00:05.000 --> 00:10.000") || !strings.Contains(text, "00:15.000 --> 00:20.000") {
		t.Fatalf("vtt timings wrong: %s", text)
	}
}

func TestCutASSTimingCases(t *testing.T) {
	cuts := []Range{{Start: 10, End: 20}}
	input := "[Events]\nFormat: Layer,Start,End,Style,Text\n" +
		"Dialogue: 0,0:00:05.00,0:00:08.00,Default,before\n" +
		"Dialogue: 0,0:00:12.00,0:00:18.00,Default,inside\n" +
		"Dialogue: 0,0:00:05.00,0:00:15.00,Default,overlap\n" +
		"Dialogue: 0,0:00:25.00,0:00:30.00,Default,after\n"
	got, err := CutSubtitle("ass", []byte(input), cuts)
	if err != nil {
		t.Fatal(err)
	}
	text := string(got)
	if strings.Contains(text, "inside") {
		t.Fatalf("inside dialogue survived: %s", text)
	}
	if !strings.Contains(text, "0:00:05.00,0:00:08.00") || !strings.Contains(text, "0:00:05.00,0:00:10.00") || !strings.Contains(text, "0:00:15.00,0:00:20.00") {
		t.Fatalf("ass timings wrong: %s", text)
	}
}

func TestCutLRCTimingCases(t *testing.T) {
	cuts := []Range{{Start: 10, End: 20}}
	input := "[00:05.000]before\n[00:12.000]inside\n[00:25.000]after\n"
	got, err := CutSubtitle("lrc", []byte(input), cuts)
	if err != nil {
		t.Fatal(err)
	}
	text := string(got)
	if strings.Contains(text, "inside") {
		t.Fatalf("inside lrc survived: %s", text)
	}
	if !strings.Contains(text, "[00:05.000]before") || !strings.Contains(text, "[00:15.000]after") {
		t.Fatalf("lrc timings wrong: %s", text)
	}
}

func TestCutSubtitleRejectsUnknown(t *testing.T) {
	if _, err := CutSubtitle("json3", []byte("{}"), nil); !IsCategory(err) && err == nil {
		t.Fatal("expected unsupported")
	}
	if _, err := CutSubtitle("json3", []byte("{}"), nil); err == nil || !errorsIsUnsupported(err) {
		t.Fatalf("err = %v", err)
	}
}

func errorsIsUnsupported(err error) bool {
	return err != nil && (err == ErrUnsupported || strings.Contains(err.Error(), "unsupported") || strings.Contains(err.Error(), ErrUnsupported.Error()))
}

func IsCategory(err error) bool { return err != nil }
