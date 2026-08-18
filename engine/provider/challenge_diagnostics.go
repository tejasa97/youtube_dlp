package provider

import (
	"errors"
	"strings"
	"time"
)

// Closed-vocabulary diagnostics for YouTube EJS challenge solving. Values are
// allowlisted tokens only; formatters must never interpolate caller data.
const (
	ChallengeStageEJS           = "ejs"
	ChallengeCacheHit           = "hit"
	ChallengeCacheMiss          = "miss"
	ChallengeCacheNone          = "none"
	ChallengeBucketNone         = "none"
	ChallengeBucketSkipped      = "skipped"
	ChallengeBucketLT10ms       = "lt_10ms"
	ChallengeBucket10ms100ms    = "10ms_100ms"
	ChallengeBucket100ms1s      = "100ms_1s"
	ChallengeBucket1s5s         = "1s_5s"
	ChallengeBucket5s10s        = "5s_10s"
	ChallengeBucket10s15s       = "10s_15s"
	ChallengeBucket15s30s       = "15s_30s"
	ChallengeBucket30s55s       = "30s_55s"
	ChallengeBucketGT55s        = "gt_55s"
	ChallengePhasePreprocess    = "preprocess"
	ChallengePhaseSolve         = "solve"
	ChallengePhaseNone          = "none"
	ChallengeHelperNone         = "none"
	ChallengeHelperUnknown      = "unknown"
	ChallengeHelperMalformed    = "malformed"
	ChallengeHelperEmptyPlayer  = "empty_player"
	ChallengeHelperInvalidInput = "invalid_input"
	ChallengeHelperUnavailable  = "unavailable"
	ChallengeHelperTimeout      = "timeout"
	ChallengeHelperCanceled     = "canceled"
	ChallengeHelperCrash        = "helper_crash"
	ChallengeHelperProtocol     = "protocol_error"
	ChallengeHelperSyntax       = "syntax_error"
	ChallengeHelperExecution    = "execution_error"
	ChallengeHelperFunction     = "function_missing"
	ChallengeHelperInputLimit   = "input_limit"
	ChallengeHelperOutputLimit  = "output_limit"
	ChallengeHelperMemoryLimit  = "memory_limit"
	ChallengeHelperModule       = "unsupported_module"
	ChallengeHelperInvalidReq   = "invalid_request"
	ChallengeHelperIncompatible = "incompatible_version"
)

// ChallengeDiagnostics is a secret-free summary of one SolvePlayer attempt.
type ChallengeDiagnostics struct {
	Cache            string
	PreprocessBucket string
	SolveBucket      string
	HelperCategory   string
	Phase            string
}

// ChallengeFailure carries closed diagnostics alongside a wrapped solver error.
type ChallengeFailure struct {
	Diagnostics ChallengeDiagnostics
	Err         error
}

func (failure *ChallengeFailure) Error() string {
	if failure == nil {
		return "EJS challenge failed"
	}
	if failure.Err != nil {
		return failure.Err.Error()
	}
	return "EJS challenge failed"
}

func (failure *ChallengeFailure) Unwrap() error {
	if failure == nil {
		return nil
	}
	return failure.Err
}

// ChallengePipelineStage reports the EJS stage when err is a challenge failure
// or ErrChallengeSolver. Other errors return empty so callers can distinguish
// pre-extraction and media-transfer failures via existing events.
func ChallengePipelineStage(err error) string {
	var failure *ChallengeFailure
	if errors.As(err, &failure) {
		return ChallengeStageEJS
	}
	if errors.Is(err, ErrChallengeSolver) {
		return ChallengeStageEJS
	}
	return ""
}

// ChallengeDurationBucket maps a duration onto a closed latency set.
func ChallengeDurationBucket(duration time.Duration) string {
	if duration < 0 {
		return ChallengeBucketNone
	}
	switch {
	case duration < 10*time.Millisecond:
		return ChallengeBucketLT10ms
	case duration < 100*time.Millisecond:
		return ChallengeBucket10ms100ms
	case duration < time.Second:
		return ChallengeBucket100ms1s
	case duration < 5*time.Second:
		return ChallengeBucket1s5s
	case duration < 10*time.Second:
		return ChallengeBucket5s10s
	case duration < 15*time.Second:
		return ChallengeBucket10s15s
	case duration < 30*time.Second:
		return ChallengeBucket15s30s
	case duration < 55*time.Second:
		return ChallengeBucket30s55s
	default:
		return ChallengeBucketGT55s
	}
}

// PreprocessBucket reports skipped on cache hits and a duration bucket otherwise.
func PreprocessBucket(cache string, duration time.Duration) string {
	if cache == ChallengeCacheHit {
		return ChallengeBucketSkipped
	}
	return ChallengeDurationBucket(duration)
}

// Sanitize replaces any non-allowlisted token with a closed default.
func (diagnostics ChallengeDiagnostics) Sanitize() ChallengeDiagnostics {
	return ChallengeDiagnostics{
		Cache:            allowOrDefault(diagnostics.Cache, challengeCacheAllowed, ChallengeCacheNone),
		PreprocessBucket: allowOrDefault(diagnostics.PreprocessBucket, challengeBucketAllowed, ChallengeBucketNone),
		SolveBucket:      allowOrDefault(diagnostics.SolveBucket, challengeBucketAllowed, ChallengeBucketNone),
		HelperCategory:   sanitizeHelperCategory(diagnostics.HelperCategory),
		Phase:            allowOrDefault(diagnostics.Phase, challengePhaseAllowed, ChallengePhaseNone),
	}
}

// EventMessage formats diagnostics as a closed key=value string. It never
// interpolates URLs, hashes, player source, challenges, tokens, or cookies.
func (diagnostics ChallengeDiagnostics) EventMessage() string {
	sanitized := diagnostics.Sanitize()
	parts := []string{
		"stage=" + ChallengeStageEJS,
		"cache=" + sanitized.Cache,
		"preprocess=" + sanitized.PreprocessBucket,
		"solve=" + sanitized.SolveBucket,
	}
	if sanitized.HelperCategory != ChallengeHelperNone && sanitized.HelperCategory != "" {
		parts = append(parts, "error="+sanitized.HelperCategory)
	}
	if sanitized.Phase != ChallengePhaseNone && sanitized.Phase != "" {
		parts = append(parts, "phase="+sanitized.Phase)
	}
	return strings.Join(parts, " ")
}

func allowOrDefault(value string, allowed map[string]struct{}, fallback string) string {
	if value == "" {
		return fallback
	}
	if _, ok := allowed[value]; ok {
		return value
	}
	return fallback
}

func sanitizeHelperCategory(value string) string {
	if value == "" {
		return ChallengeHelperNone
	}
	if _, ok := challengeHelperAllowed[value]; ok {
		return value
	}
	return ChallengeHelperUnknown
}

var (
	challengeCacheAllowed = map[string]struct{}{
		ChallengeCacheHit:  {},
		ChallengeCacheMiss: {},
		ChallengeCacheNone: {},
	}
	challengeBucketAllowed = map[string]struct{}{
		ChallengeBucketNone:      {},
		ChallengeBucketSkipped:   {},
		ChallengeBucketLT10ms:    {},
		ChallengeBucket10ms100ms: {},
		ChallengeBucket100ms1s:   {},
		ChallengeBucket1s5s:      {},
		ChallengeBucket5s10s:     {},
		ChallengeBucket10s15s:    {},
		ChallengeBucket15s30s:    {},
		ChallengeBucket30s55s:    {},
		ChallengeBucketGT55s:     {},
	}
	challengePhaseAllowed = map[string]struct{}{
		ChallengePhasePreprocess: {},
		ChallengePhaseSolve:      {},
		ChallengePhaseNone:       {},
	}
	challengeHelperAllowed = map[string]struct{}{
		ChallengeHelperNone:         {},
		ChallengeHelperUnknown:      {},
		ChallengeHelperMalformed:    {},
		ChallengeHelperEmptyPlayer:  {},
		ChallengeHelperInvalidInput: {},
		ChallengeHelperUnavailable:  {},
		ChallengeHelperTimeout:      {},
		ChallengeHelperCanceled:     {},
		ChallengeHelperCrash:        {},
		ChallengeHelperProtocol:     {},
		ChallengeHelperSyntax:       {},
		ChallengeHelperExecution:    {},
		ChallengeHelperFunction:     {},
		ChallengeHelperInputLimit:   {},
		ChallengeHelperOutputLimit:  {},
		ChallengeHelperMemoryLimit:  {},
		ChallengeHelperModule:       {},
		ChallengeHelperInvalidReq:   {},
		ChallengeHelperIncompatible: {},
	}
)
