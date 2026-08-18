package provider

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestChallengeDurationBucketClosedSet(t *testing.T) {
	cases := []struct {
		duration time.Duration
		want     string
	}{
		{0, ChallengeBucketLT10ms},
		{9 * time.Millisecond, ChallengeBucketLT10ms},
		{10 * time.Millisecond, ChallengeBucket10ms100ms},
		{99 * time.Millisecond, ChallengeBucket10ms100ms},
		{time.Second, ChallengeBucket1s5s},
		{5 * time.Second, ChallengeBucket5s10s},
		{10 * time.Second, ChallengeBucket10s15s},
		{15 * time.Second, ChallengeBucket15s30s},
		{30 * time.Second, ChallengeBucket30s55s},
		{55 * time.Second, ChallengeBucketGT55s},
		{-time.Millisecond, ChallengeBucketNone},
	}
	for _, test := range cases {
		if got := ChallengeDurationBucket(test.duration); got != test.want {
			t.Fatalf("ChallengeDurationBucket(%s)=%q want %q", test.duration, got, test.want)
		}
	}
}

func TestPreprocessBucketSkipsCacheHits(t *testing.T) {
	if got := PreprocessBucket(ChallengeCacheHit, time.Second); got != ChallengeBucketSkipped {
		t.Fatalf("hit bucket=%q", got)
	}
	if got := PreprocessBucket(ChallengeCacheMiss, time.Millisecond); got != ChallengeBucketLT10ms {
		t.Fatalf("miss bucket=%q", got)
	}
}

func TestChallengeDiagnosticsSanitizeRejectsSecrets(t *testing.T) {
	hostile := ChallengeDiagnostics{
		Cache:            "https://www.youtube.com/watch?v=dQw4w9WgXcQ",
		PreprocessBucket: "player-sha256-28035902e6a4b878",
		SolveBucket:      "n=abc;sig=NJAJEij0",
		HelperCategory:   "Cookie: SID=secret; Authorization: Bearer tok",
		Phase:            "https://googlevideo.com/videoplayback?range=0-1024",
	}
	sanitized := hostile.Sanitize()
	if sanitized.Cache != ChallengeCacheNone || sanitized.PreprocessBucket != ChallengeBucketNone ||
		sanitized.SolveBucket != ChallengeBucketNone || sanitized.HelperCategory != ChallengeHelperUnknown ||
		sanitized.Phase != ChallengePhaseNone {
		t.Fatalf("sanitize=%#v", sanitized)
	}
	message := hostile.EventMessage()
	for _, secret := range []string{
		"youtube.com", "watch?v=", "dQw4w9WgXcQ", "28035902e6a4b878", "NJAJEij0",
		"SID=", "Bearer", "googlevideo", "range=", "videoplayback",
	} {
		if strings.Contains(message, secret) {
			t.Fatalf("EventMessage leaked %q: %s", secret, message)
		}
	}
	if !strings.Contains(message, "stage=ejs") || !strings.Contains(message, "cache=none") {
		t.Fatalf("message=%q", message)
	}
}

func TestChallengeDiagnosticsEventMessageOmitsEmptyError(t *testing.T) {
	message := ChallengeDiagnostics{
		Cache:            ChallengeCacheHit,
		PreprocessBucket: ChallengeBucketSkipped,
		SolveBucket:      ChallengeBucketLT10ms,
		HelperCategory:   ChallengeHelperNone,
		Phase:            ChallengePhaseNone,
	}.EventMessage()
	if strings.Contains(message, "error=") || strings.Contains(message, "phase=") {
		t.Fatalf("message=%q", message)
	}
	if message != "stage=ejs cache=hit preprocess=skipped solve=lt_10ms" {
		t.Fatalf("message=%q", message)
	}
}

func TestChallengePipelineStageSeparatesLayers(t *testing.T) {
	failure := &ChallengeFailure{Err: errors.New("EJS helper timeout")}
	if ChallengePipelineStage(failure) != ChallengeStageEJS {
		t.Fatal("ChallengeFailure must be EJS")
	}
	if ChallengePipelineStage(fmt.Errorf("%w: helper", ErrChallengeSolver)) != ChallengeStageEJS {
		t.Fatal("ErrChallengeSolver must be EJS")
	}
	if ChallengePipelineStage(errors.New("HTTP 429")) != "" {
		t.Fatal("pre-extraction errors must not be attributed to EJS")
	}
	if ChallengePipelineStage(errors.New("HTTP 403")) != "" {
		t.Fatal("media-transfer errors must not be attributed to EJS")
	}
}

func TestChallengeFailureUnwrapsContext(t *testing.T) {
	err := &ChallengeFailure{Err: context.Canceled}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Unwrap lost context cancellation: %v", err)
	}
}
