package operations

import (
	"context"
	"encoding/json"
	"io"
	"sort"
	"time"
)

const (
	MaxExecutionHistory = 10_000
	maxPolicyDurationMS = int64(365 * 24 * time.Hour / time.Millisecond)
)

// CanaryExpiry binds a policy expiry to every canary in a Suite. Requiring an
// exact ID set prevents a newly-added canary from inheriting an older policy.
type CanaryExpiry struct {
	CanaryID      string `json:"canary_id"`
	ExpiresUnixMS int64  `json:"expires_unix_ms"`
}

// ExecutionPolicy is a deployment-supplied, bounded schedule. It contains no
// target, credential, or region material. Both MinIntervalMS and the rolling
// window apply to whole-suite starts, before any Runner is invoked.
type ExecutionPolicy struct {
	SuiteSHA256      string         `json:"suite_sha256"`
	NotBeforeUnixMS  int64          `json:"not_before_unix_ms"`
	ExpiresUnixMS    int64          `json:"expires_unix_ms"`
	MinIntervalMS    int64          `json:"min_interval_ms"`
	WindowMS         int64          `json:"window_ms"`
	MaxRunsPerWindow int            `json:"max_runs_per_window"`
	Canaries         []CanaryExpiry `json:"canaries"`
}

// ExecutionLedger is the bounded set of accepted suite-start timestamps.
// Callers must persist the returned value atomically before concurrent runs.
type ExecutionLedger struct {
	RunsUnixMS []int64 `json:"runs_unix_ms"`
}

// AuthorizeExecution checks time validity and frequency without side effects.
// The returned ledger is canonical, pruned to the current rolling window, and
// includes now. A failed authorization never returns an updated ledger.
func AuthorizeExecution(suite Suite, policy ExecutionPolicy, now time.Time, ledger ExecutionLedger) (ExecutionLedger, error) {
	canonical, err := NewSuite(suite.Canaries)
	if err != nil || suite.SchemaVersion != SchemaVersion || !validExecutionPolicy(canonical, policy) || len(ledger.RunsUnixMS) > MaxExecutionHistory {
		return ExecutionLedger{}, ErrInvalidSpec
	}
	nowMS := now.UTC().UnixMilli()
	if nowMS < policy.NotBeforeUnixMS {
		return ExecutionLedger{}, ErrNotYetValid
	}
	if nowMS >= policy.ExpiresUnixMS {
		return ExecutionLedger{}, ErrExpired
	}
	for _, expiry := range policy.Canaries {
		if nowMS >= expiry.ExpiresUnixMS {
			return ExecutionLedger{}, ErrExpired
		}
	}
	runs := append([]int64(nil), ledger.RunsUnixMS...)
	if !sort.SliceIsSorted(runs, func(i, j int) bool { return runs[i] < runs[j] }) {
		return ExecutionLedger{}, ErrInvalidSpec
	}
	for index := 1; index < len(runs); index++ {
		if runs[index]-runs[index-1] < policy.MinIntervalMS {
			return ExecutionLedger{}, ErrInvalidSpec
		}
	}
	windowStart := nowMS - policy.WindowMS
	kept := runs[:0]
	for _, run := range runs {
		if run < 0 || run > nowMS {
			return ExecutionLedger{}, ErrInvalidSpec
		}
		if run > windowStart {
			kept = append(kept, run)
		}
	}
	if len(kept) > 0 && nowMS-kept[len(kept)-1] < policy.MinIntervalMS {
		return ExecutionLedger{}, ErrRateLimited
	}
	if len(kept) >= policy.MaxRunsPerWindow || len(kept) >= MaxExecutionHistory {
		return ExecutionLedger{}, ErrRateLimited
	}
	kept = append(kept, nowMS)
	return ExecutionLedger{RunsUnixMS: append([]int64(nil), kept...)}, nil
}

func validExecutionPolicy(suite Suite, policy ExecutionPolicy) bool {
	_, digest, err := suiteDigest(suite)
	if err != nil || policy.SuiteSHA256 != digest || policy.NotBeforeUnixMS < 0 || policy.ExpiresUnixMS <= policy.NotBeforeUnixMS ||
		policy.ExpiresUnixMS-policy.NotBeforeUnixMS > maxPolicyDurationMS ||
		policy.MinIntervalMS < 1 || policy.WindowMS < policy.MinIntervalMS || policy.WindowMS > maxPolicyDurationMS ||
		policy.MaxRunsPerWindow < 1 || policy.MaxRunsPerWindow > MaxExecutionHistory ||
		len(policy.Canaries) != len(suite.Canaries) {
		return false
	}
	canaries := append([]CanaryExpiry(nil), policy.Canaries...)
	sort.Slice(canaries, func(i, j int) bool { return canaries[i].CanaryID < canaries[j].CanaryID })
	for index, expiry := range canaries {
		if expiry.CanaryID != suite.Canaries[index].ID || expiry.ExpiresUnixMS <= policy.NotBeforeUnixMS || expiry.ExpiresUnixMS > policy.ExpiresUnixMS {
			return false
		}
	}
	return true
}

// MarshalExecutionPolicy validates against the exact suite and emits canaries
// in canonical ID order.
func MarshalExecutionPolicy(suite Suite, policy ExecutionPolicy) ([]byte, error) {
	canonical, err := NewSuite(suite.Canaries)
	if err != nil || suite.SchemaVersion != SchemaVersion || !validExecutionPolicy(canonical, policy) {
		return nil, ErrInvalidSpec
	}
	result := policy
	result.Canaries = append([]CanaryExpiry(nil), policy.Canaries...)
	sort.Slice(result.Canaries, func(i, j int) bool { return result.Canaries[i].CanaryID < result.Canaries[j].CanaryID })
	return json.Marshal(result)
}

func DecodeExecutionPolicy(ctx context.Context, reader io.Reader, maxBytes int64, suite Suite) (ExecutionPolicy, error) {
	data, err := readDocument(ctx, reader, maxBytes)
	if err != nil {
		return ExecutionPolicy{}, err
	}
	var policy ExecutionPolicy
	if err := decodeStrict(data, &policy); err != nil {
		return ExecutionPolicy{}, ErrDecode
	}
	canonical, err := MarshalExecutionPolicy(suite, policy)
	if err != nil {
		return ExecutionPolicy{}, err
	}
	if err := json.Unmarshal(canonical, &policy); err != nil {
		return ExecutionPolicy{}, ErrDecode
	}
	return policy, nil
}

// ExecuteControlled is the deployment boundary for scheduled canaries. It
// authorizes before invoking the Runner and returns the updated ledger for
// atomic persistence by the deployment owner.
func ExecuteControlled(ctx context.Context, suite Suite, options RunOptions, policy ExecutionPolicy, ledger ExecutionLedger, runner Runner, clock Clock) ([]Record, ExecutionLedger, error) {
	if !options.OptIn {
		return nil, ledger, ErrOptInRequired
	}
	if ctx == nil || runner == nil {
		return nil, ledger, ErrInvalidSpec
	}
	if clock == nil {
		clock = realClock{}
	}
	updated, err := AuthorizeExecution(suite, policy, clock.Now(), ledger)
	if err != nil {
		return nil, ledger, err
	}
	records, err := Execute(ctx, suite, options, runner, clock)
	return records, updated, err
}
