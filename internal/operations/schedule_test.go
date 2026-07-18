package operations

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"
)

func fixturePolicy(suite Suite) ExecutionPolicy {
	const start = int64(1_700_000_000_000)
	_, digest, _ := suiteDigest(suite)
	expiries := make([]CanaryExpiry, len(suite.Canaries))
	for index, canary := range suite.Canaries {
		expiries[index] = CanaryExpiry{CanaryID: canary.ID, ExpiresUnixMS: start + 6*hourMS}
	}
	return ExecutionPolicy{
		SuiteSHA256:     digest,
		NotBeforeUnixMS: start, ExpiresUnixMS: start + 12*hourMS,
		MinIntervalMS: 15 * 60 * 1000, WindowMS: hourMS, MaxRunsPerWindow: 2,
		Canaries: expiries,
	}
}

func TestAuthorizeExecutionEnforcesValidityExpiryAndFrequency(t *testing.T) {
	suite := fixtureSuite(t)
	policy := fixturePolicy(suite)
	before := time.UnixMilli(policy.NotBeforeUnixMS - 1)
	if _, err := AuthorizeExecution(suite, policy, before, ExecutionLedger{}); !errors.Is(err, ErrNotYetValid) {
		t.Fatalf("before validity error = %v", err)
	}
	now := time.UnixMilli(policy.NotBeforeUnixMS)
	ledger, err := AuthorizeExecution(suite, policy, now, ExecutionLedger{})
	if err != nil || len(ledger.RunsUnixMS) != 1 || ledger.RunsUnixMS[0] != now.UnixMilli() {
		t.Fatalf("first authorization ledger=%+v err=%v", ledger, err)
	}
	if _, err := AuthorizeExecution(suite, policy, now.Add(14*time.Minute), ledger); !errors.Is(err, ErrRateLimited) {
		t.Fatalf("minimum interval error = %v", err)
	}
	ledger, err = AuthorizeExecution(suite, policy, now.Add(15*time.Minute), ledger)
	if err != nil || len(ledger.RunsUnixMS) != 2 {
		t.Fatalf("second authorization ledger=%+v err=%v", ledger, err)
	}
	if _, err := AuthorizeExecution(suite, policy, now.Add(45*time.Minute), ledger); !errors.Is(err, ErrRateLimited) {
		t.Fatalf("window rate error = %v", err)
	}
	ledger, err = AuthorizeExecution(suite, policy, now.Add(61*time.Minute), ledger)
	if err != nil || len(ledger.RunsUnixMS) != 2 || ledger.RunsUnixMS[0] != now.Add(15*time.Minute).UnixMilli() {
		t.Fatalf("pruned authorization ledger=%+v err=%v", ledger, err)
	}
	if _, err := AuthorizeExecution(suite, policy, time.UnixMilli(policy.Canaries[0].ExpiresUnixMS), ledger); !errors.Is(err, ErrExpired) {
		t.Fatalf("canary expiry error = %v", err)
	}
}

func TestExecutionPolicyFixtureIsCanonicalAndStrict(t *testing.T) {
	suite := fixtureSuite(t)
	data := fixture(t, "canary_policy_v1.json")
	policy, err := DecodeExecutionPolicy(context.Background(), bytes.NewReader(data), 4096, suite)
	if err != nil {
		t.Fatal(err)
	}
	canonical, err := MarshalExecutionPolicy(suite, policy)
	if err != nil {
		t.Fatal(err)
	}
	if string(canonical) != string(bytes.TrimSpace(data)) {
		t.Fatalf("policy is not canonical:\n%s", canonical)
	}
	duplicate := append([]byte(nil), bytes.TrimSpace(data)...)
	duplicate = bytes.Replace(duplicate, []byte(`"window_ms":3600000`), []byte(`"window_ms":3600000,"window_ms":3600000`), 1)
	if _, err := DecodeExecutionPolicy(context.Background(), bytes.NewReader(duplicate), 4096, suite); !errors.Is(err, ErrDecode) {
		t.Fatalf("duplicate field error = %v", err)
	}
}

func TestAuthorizeExecutionRejectsIncompletePolicyAndInvalidLedger(t *testing.T) {
	suite := fixtureSuite(t)
	policy := fixturePolicy(suite)
	now := time.UnixMilli(policy.NotBeforeUnixMS)
	policy.Canaries = policy.Canaries[:len(policy.Canaries)-1]
	if _, err := AuthorizeExecution(suite, policy, now, ExecutionLedger{}); !errors.Is(err, ErrInvalidSpec) {
		t.Fatalf("incomplete policy error = %v", err)
	}
	policy = fixturePolicy(suite)
	if _, err := AuthorizeExecution(suite, policy, now.Add(time.Hour), ExecutionLedger{RunsUnixMS: []int64{now.Add(time.Minute).UnixMilli(), now.UnixMilli()}}); !errors.Is(err, ErrInvalidSpec) {
		t.Fatalf("unordered ledger error = %v", err)
	}
	if _, err := AuthorizeExecution(suite, policy, now, ExecutionLedger{RunsUnixMS: []int64{now.Add(time.Minute).UnixMilli()}}); !errors.Is(err, ErrInvalidSpec) {
		t.Fatalf("future ledger error = %v", err)
	}
}

func TestExecuteControlledDoesNotInvokeRunnerWhenDenied(t *testing.T) {
	suite := fixtureSuite(t)
	policy := fixturePolicy(suite)
	clock := &fakeClock{now: time.UnixMilli(policy.ExpiresUnixMS)}
	called := false
	records, ledger, err := ExecuteControlled(context.Background(), suite, RunOptions{OptIn: true}, policy, ExecutionLedger{}, RunnerFunc(func(context.Context, Invocation) (Observation, error) {
		called = true
		return Observation{Outcome: OutcomeSuccess, Failure: FailureNone}, nil
	}), clock)
	if !errors.Is(err, ErrExpired) || called || len(records) != 0 || len(ledger.RunsUnixMS) != 0 {
		t.Fatalf("denied run records=%+v ledger=%+v called=%v err=%v", records, ledger, called, err)
	}
}

func TestExecuteControlledDoesNotConsumePermitWithoutOptIn(t *testing.T) {
	suite := fixtureSuite(t)
	policy := fixturePolicy(suite)
	clock := &fakeClock{now: time.UnixMilli(policy.NotBeforeUnixMS)}
	_, ledger, err := ExecuteControlled(context.Background(), suite, RunOptions{}, policy, ExecutionLedger{}, RunnerFunc(func(context.Context, Invocation) (Observation, error) {
		t.Fatal("runner invoked")
		return Observation{}, nil
	}), clock)
	if !errors.Is(err, ErrOptInRequired) || len(ledger.RunsUnixMS) != 0 {
		t.Fatalf("opt-in denial ledger=%+v err=%v", ledger, err)
	}
}

func FuzzAuthorizeExecution(f *testing.F) {
	f.Add(int64(1_700_000_000_000), int64(0), uint16(1))
	f.Fuzz(func(t *testing.T, nowMS, priorMS int64, maxRuns uint16) {
		suite := fixtureSuite(t)
		policy := fixturePolicy(suite)
		policy.MaxRunsPerWindow = int(maxRuns)
		ledger := ExecutionLedger{}
		if priorMS != 0 {
			ledger.RunsUnixMS = []int64{priorMS}
		}
		updated, err := AuthorizeExecution(suite, policy, time.UnixMilli(nowMS), ledger)
		if err == nil {
			if len(updated.RunsUnixMS) == 0 || len(updated.RunsUnixMS) > MaxExecutionHistory || updated.RunsUnixMS[len(updated.RunsUnixMS)-1] != nowMS {
				t.Fatalf("invalid authorized ledger: %+v", updated)
			}
		}
	})
}
