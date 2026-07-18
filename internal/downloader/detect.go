package downloader

import (
	"math"
	"math/bits"
	"time"
)

type throttleDetector struct {
	rate    int64
	window  time.Duration
	now     func() time.Time
	started time.Time
	bytes   int64
}

func newThrottleDetector(rate int64, window time.Duration, now func() time.Time) *throttleDetector {
	if rate <= 0 {
		return &throttleDetector{}
	}
	if window <= 0 {
		window = 2 * time.Second
	}
	return &throttleDetector{rate: rate, window: window, now: now, started: now()}
}
func (detector *throttleDetector) Observe(bytes int) bool {
	if detector.rate <= 0 || bytes <= 0 {
		return false
	}
	if int64(bytes) > math.MaxInt64-detector.bytes {
		return false
	}
	detector.bytes += int64(bytes)
	elapsed := detector.now().Sub(detector.started)
	if elapsed < detector.window {
		return false
	}
	// Compare bytes/elapsed with rate using 128-bit products. This avoids
	// overflowing either side for intentionally large user-supplied rates.
	leftHigh, leftLow := bits.Mul64(uint64(detector.bytes), uint64(time.Second))
	rightHigh, rightLow := bits.Mul64(uint64(detector.rate), uint64(elapsed))
	return leftHigh < rightHigh || (leftHigh == rightHigh && leftLow < rightLow)
}
