package youtubeump

import (
	"math"
	"testing"
)

func TestCeilMillisFromTicksExactAndFractional(t *testing.T) {
	for _, test := range []struct {
		ticks, scale int64
		want         int64
		ok           bool
	}{
		{1000, 1000, 1000, true},
		{1001, 1000, 1001, true},
		{1, 3, 334, true},
		{0, 1000, 0, false},
		{-1, 1000, 0, false},
		{1, 0, 0, false},
		{math.MaxInt64/1000 + 1, 1, 0, false},
		{math.MaxInt64/1000 + 1, math.MaxInt64, 2, true},
		{math.MaxInt64 / 1000, math.MaxInt64, 1, true},
	} {
		got, ok := ceilMillisFromTicks(test.ticks, test.scale)
		if ok != test.ok || got != test.want {
			t.Fatalf("ticks=%d scale=%d got=%d ok=%v want=%d %v", test.ticks, test.scale, got, ok, test.want, test.ok)
		}
	}
}

func TestCeilMillisFromTicksRejectsActualOverflow(t *testing.T) {
	ticks := int64(math.MaxInt64/1000 + 1)
	scale := int64(1)
	got, ok := ceilMillisFromTicks(ticks, scale)
	if ok || got != 0 {
		t.Fatalf("got=%d ok=%v", got, ok)
	}
}

func TestEffectiveDurationMsFromTimeRange(t *testing.T) {
	header := MediaHeader{TimeRange: TimeRange{DurationTicks: 1001, Timescale: 1000}}
	if got := header.effectiveDurationMs(); got != 1001 {
		t.Fatalf("got=%d", got)
	}
}
