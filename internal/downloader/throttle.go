package downloader

import (
	"context"
	"time"
)

// throttle is deliberately deterministic: there is no random jitter, making
// CLI behavior and conformance tests reproducible. It permits one second of
// burst traffic, then accounts for all subsequent bytes.
type throttle struct {
	rate  int64
	next  time.Time
	now   func() time.Time
	sleep func(context.Context, time.Duration) error
}

func newThrottle(rate int64) *throttle { return newThrottleWithClock(rate, time.Now, waitFor) }

// newThrottleWithClock keeps pacing testable with a virtual clock. Production
// callers use time.Now and a context-aware timer through newThrottle.
func newThrottleWithClock(rate int64, now func() time.Time, sleep func(context.Context, time.Duration) error) *throttle {
	return &throttle{rate: rate, now: now, sleep: sleep}
}

func (limiter *throttle) Wait(ctx context.Context, bytes int) error {
	if limiter.rate <= 0 || bytes <= 0 {
		return nil
	}
	now := limiter.now()
	if limiter.next.IsZero() || limiter.next.Before(now) {
		limiter.next = now
	}
	delay := limiter.next.Sub(now)
	limiter.next = limiter.next.Add(time.Duration(int64(bytes) * int64(time.Second) / limiter.rate))
	if delay <= 0 {
		return nil
	}
	return limiter.sleep(ctx, delay)
}

func waitFor(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
