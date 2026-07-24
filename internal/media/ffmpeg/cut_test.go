package ffmpeg

import (
	"context"
	"errors"
	"math"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ytdlp-go/ytdlp/internal/events"
)

func TestForceKeyframesAndConcatRanges(t *testing.T) {
	tools, err := Discover(Config{})
	if errors.Is(err, ErrFFmpegUnavailable) || errors.Is(err, ErrFFprobeUnavailable) {
		t.Skipf("ffmpeg toolchain unavailable: %v", err)
	}
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	root := t.TempDir()
	input := filepath.Join(root, "input.mp4")
	if _, err := tools.execute(ctx, tools.ffmpeg, []string{
		"-nostdin", "-y", "-f", "lavfi", "-i", "color=c=black:s=16x16:d=3",
		"-an", "-c:v", "mpeg4", "-q:v", "5", input,
	}, nil); err != nil {
		t.Fatalf("generate input: %v", err)
	}
	cut := filepath.Join(root, "cut.mp4")
	ranges := []ConcatRange{
		{OutPoint: "1.000000"},
		{InPoint: "2.000000"},
	}
	var kinds []events.Kind
	sink := events.SinkFunc(func(_ context.Context, event events.Event) error {
		kinds = append(kinds, event.Kind)
		return nil
	})
	if err := tools.CutOutRanges(ctx, input, cut, ranges, []float64{1, 2}, true, false, sink); err != nil {
		t.Fatal(err)
	}
	probe, err := tools.Probe(ctx, cut)
	if err != nil {
		t.Fatal(err)
	}
	duration, err := strconv.ParseFloat(probe.Format.Duration, 64)
	if err != nil {
		t.Fatal(err)
	}
	if duration < 1.5 || duration > 2.5 {
		t.Fatalf("cut duration = %v, probe=%#v", duration, probe.Format)
	}
	if len(kinds) == 0 || kinds[0] != events.KindPostprocessStarting {
		t.Fatalf("events = %v", kinds)
	}
}

func TestConcatRangesValidation(t *testing.T) {
	tools := &Toolset{}
	if err := tools.ConcatRanges(context.Background(), "missing", "out.mp4", nil, false, nil); !errors.Is(err, ErrInvalidOperation) {
		t.Fatalf("empty ranges err = %v", err)
	}
	if err := tools.ForceKeyframes(context.Background(), "missing", "out.mp4", []float64{math.NaN()}, false, nil); err == nil {
		t.Fatal("expected force keyframe validation error")
	}
}

func TestNormalizeForceKeyframeTimestamps(t *testing.T) {
	got, err := normalizeForceKeyframeTimestamps([]float64{0, 1.5, 1.5, 2})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(got, ",") != "1.500000,2.000000" {
		t.Fatalf("got %v", got)
	}
}
