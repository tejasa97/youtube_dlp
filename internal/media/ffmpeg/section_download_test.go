package ffmpeg

import (
	"net/http"
	"strings"
	"testing"
)

func TestSectionFFmpegArgsSingleInputStreamCopy(t *testing.T) {
	inputs := []SectionInput{{URL: "https://media.example/v.mp4"}}
	end := 15.0
	args := sectionFFmpegArgs(inputs, []string{""}, SectionBounds{Start: 10, End: &end}, false, "/tmp/out.mp4")
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "-i https://media.example/v.mp4") {
		t.Fatalf("missing input: %s", joined)
	}
	if !strings.Contains(joined, "-map 0") {
		t.Fatalf("missing map 0 for single input: %s", joined)
	}
	if !strings.Contains(joined, "-c copy") {
		t.Fatalf("missing stream copy: %s", joined)
	}
	if !strings.Contains(joined, "-ss 10") || !strings.Contains(joined, "-t 5") {
		t.Fatalf("missing section cut args: %s", joined)
	}
}

func TestSectionFFmpegArgsHeadersPrecedeInput(t *testing.T) {
	inputs := []SectionInput{{URL: "https://media.example/v.mp4", Headers: http.Header{"Referer": {"https://page.example"}}}}
	end := 15.0
	args := sectionFFmpegArgs(inputs, []string{"-headers Referer: https://page.example\r\n"}, SectionBounds{Start: 10, End: &end}, false, "/tmp/out.mp4")
	joined := strings.Join(args, " ")
	headPos := strings.Index(joined, "-headers")
	inputPos := strings.Index(joined, "-i https://media.example/v.mp4")
	if headPos == -1 || inputPos == -1 || headPos > inputPos {
		t.Fatalf("-headers must precede -i: %s", joined)
	}
	// For a single input with stream copy, the seek is an output option
	// after the input; -ss must not appear before -i.
	ssPos := strings.Index(joined, "-ss 10")
	if ssPos == -1 || ssPos < inputPos {
		t.Fatalf("stream-copy -ss should be an output option after -i: %s", joined)
	}
}

func TestSectionFFmpegArgsSeparateAV(t *testing.T) {
	inputs := []SectionInput{
		{URL: "https://media.example/v.mp4", HasVideo: true},
		{URL: "https://media.example/a.m4a", HasAudio: true},
	}
	end := 30.0
	args := sectionFFmpegArgs(inputs, []string{"", ""}, SectionBounds{Start: 5, End: &end}, false, "/tmp/out.mp4")
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "-map 0:v:0") || !strings.Contains(joined, "-map 1:a:0") {
		t.Fatalf("missing separate A/V mapping: %s", joined)
	}
	if !strings.Contains(joined, "-ss 5") || !strings.Contains(joined, "-t 25") {
		t.Fatalf("missing cut args: %s", joined)
	}
}

func TestSectionFFmpegArgsOpenEnded(t *testing.T) {
	inputs := []SectionInput{{URL: "https://media.example/v.mp4"}}
	args := sectionFFmpegArgs(inputs, []string{""}, SectionBounds{Start: 10, End: nil}, false, "/tmp/out.mp4")
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "-ss 10") {
		t.Fatalf("missing start: %s", joined)
	}
	if strings.Contains(joined, " -t ") {
		t.Fatalf("open-ended must not set -t: %s", joined)
	}
}

func TestSectionFFmpegArgsForceKeyframesPerInput(t *testing.T) {
	inputs := []SectionInput{
		{URL: "https://media.example/v.mp4", HasVideo: true},
		{URL: "https://media.example/a.m4a", HasAudio: true},
	}
	end := 20.0
	args := sectionFFmpegArgs(inputs, []string{"", ""}, SectionBounds{Start: 5, End: &end}, true, "/tmp/out.mp4")
	joined := strings.Join(args, " ")
	// In force-keyframe mode the seek options are input options: -ss must
	// precede each -i so both A/V inputs are aligned.
	firstInput := strings.Index(joined, "-i https://media.example/v.mp4")
	secondInput := strings.Index(joined, "-i https://media.example/a.m4a")
	ssPos := strings.Index(joined, "-ss 5")
	if firstInput == -1 || secondInput == -1 || ssPos == -1 || ssPos > firstInput {
		t.Fatalf("force-keyframe -ss must precede first -i: %s", joined)
	}
	if ssPos > secondInput {
		t.Fatalf("force-keyframe -ss must precede second -i: %s", joined)
	}
	// The boundary is relative to the section start (0), not absolute.
	if !strings.Contains(joined, "-force_key_frames 0") {
		t.Fatalf("missing relative force_key_frames: %s", joined)
	}
	if strings.Contains(joined, "-c copy") {
		t.Fatalf("force-keyframes must not stream-copy: %s", joined)
	}
	if strings.Contains(joined, "libx264") {
		t.Fatalf("force-keyframes must not hardcode libx264: %s", joined)
	}
}

func TestValidateSectionBounds(t *testing.T) {
	nan := 10.0
	_ = nan
	cases := []struct {
		name   string
		bounds SectionBounds
		wantOK bool
	}{
		{"valid", SectionBounds{Start: 5, End: floatPtr(15)}, true},
		{"open ended", SectionBounds{Start: 5, End: nil}, true},
		{"zero start", SectionBounds{Start: 0, End: floatPtr(15)}, true},
		{"negative start", SectionBounds{Start: -1, End: floatPtr(15)}, false},
		{"end equals start", SectionBounds{Start: 5, End: floatPtr(5)}, false},
		{"end before start", SectionBounds{Start: 10, End: floatPtr(5)}, false},
		{"negative end", SectionBounds{Start: 5, End: floatPtr(-1)}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := validateSectionBounds(c.bounds)
			if (err == nil) != c.wantOK {
				t.Fatalf("validateSectionBounds(%#v) err = %v, wantOK=%v", c.bounds, err, c.wantOK)
			}
		})
	}
}

func TestValidateSectionInputsRejectsUnsafeHeader(t *testing.T) {
	inputs := []SectionInput{{URL: "https://media.example/v.mp4", Headers: http.Header{"Authorization": {"Bearer x"}}}}
	_, err := validateSectionInputs(inputs)
	if err == nil {
		t.Fatal("credential-isolated header accepted")
	}
}

func TestValidateSectionInputsRejectsBadURL(t *testing.T) {
	inputs := []SectionInput{{URL: "file:///etc/passwd"}}
	if _, err := validateSectionInputs(inputs); err == nil {
		t.Fatal("non-http URL accepted")
	}
}

func floatPtr(value float64) *float64 { return &value }
