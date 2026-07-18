package downloader

import "time"

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
	detector.bytes += int64(bytes)
	elapsed := detector.now().Sub(detector.started)
	if elapsed < detector.window {
		return false
	}
	return detector.bytes < detector.rate*int64(elapsed)/int64(time.Second)
}
