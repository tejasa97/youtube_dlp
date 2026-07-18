package operations

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func (clock *fakeClock) Now() time.Time {
	clock.mu.Lock()
	defer clock.mu.Unlock()
	return clock.now
}

func (clock *fakeClock) advance(duration time.Duration) {
	clock.mu.Lock()
	clock.now = clock.now.Add(duration)
	clock.mu.Unlock()
}

func TestExecuteRequiresOptInAndEmitsRedactedSemanticRecords(t *testing.T) {
	suite := fixtureSuite(t)
	clock := &fakeClock{now: time.Unix(1_700_000_000, 0).UTC()}
	seen := make(map[string]Invocation)
	runner := RunnerFunc(func(_ context.Context, invocation Invocation) (Observation, error) {
		seen[invocation.ID] = invocation
		clock.advance(125 * time.Millisecond)
		switch invocation.Class {
		case ClassCredential:
			return Observation{Outcome: OutcomeCredentialUnavailable, Failure: FailureAuth, Capability: "extract"}, nil
		case ClassRegion:
			return Observation{Outcome: OutcomeFallback, Failure: FailureNone, Capability: "formats"}, nil
		default:
			return Observation{Outcome: OutcomeSuccess, Failure: FailureNone}, nil
		}
	})
	if _, err := Execute(context.Background(), suite, RunOptions{}, runner, clock); !errors.Is(err, ErrOptInRequired) {
		t.Fatalf("non-opt-in error = %v", err)
	}
	records, err := Execute(context.Background(), suite, RunOptions{OptIn: true}, runner, clock)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 3 || records[0].CanaryID != "auth.youtube" || records[0].Outcome != OutcomeCredentialUnavailable ||
		records[1].Outcome != OutcomeSuccess || records[2].Outcome != OutcomeFallback {
		t.Fatalf("records = %#v", records)
	}
	if records[0].DurationMS != 125 || seen["auth.youtube"].Secret.Name != "youtube.fixture" || seen["region.bbc"].Region != "GB" {
		t.Fatalf("invocations=%#v records=%#v", seen, records)
	}
	data, err := MarshalRecords(suite, records)
	if err != nil {
		t.Fatal(err)
	}
	serialized := string(data)
	for _, prohibited := range []string{"youtube.fixture", "keychain", "youtube.members.fixture", "bbc.public.fixture", `"region":"GB"`} {
		if strings.Contains(serialized, prohibited) {
			t.Fatalf("record export leaked %q: %s", prohibited, serialized)
		}
	}
}

func TestExecuteRedactsRunnerErrorsRejectsLabelsAndEnforcesTimeout(t *testing.T) {
	spec := CanarySpec{ID: "public.test", Class: ClassPublic, Extractor: "generic", TargetRef: "public.fixture", Capabilities: []string{"extract"}, TimeoutMS: 5}
	suite, err := NewSuite([]CanarySpec{spec})
	if err != nil {
		t.Fatal(err)
	}
	clock := &fakeClock{now: time.Unix(1_700_000_000, 0)}
	const secret = "runner-secret-must-not-leak"
	records, err := Execute(context.Background(), suite, RunOptions{OptIn: true}, RunnerFunc(func(context.Context, Invocation) (Observation, error) {
		return Observation{}, errors.New(secret)
	}), clock)
	if err != nil || len(records) != 1 || records[0].Failure != FailureRunner || records[0].Outcome != OutcomeBreakage {
		t.Fatalf("runner error records=%#v err=%v", records, err)
	}
	data, err := MarshalRecords(suite, records)
	if err != nil || strings.Contains(string(data), secret) {
		t.Fatalf("runner error leaked: %s (%v)", data, err)
	}

	records, err = Execute(context.Background(), suite, RunOptions{OptIn: true}, RunnerFunc(func(context.Context, Invocation) (Observation, error) {
		return Observation{Outcome: OutcomeSuccess, Failure: FailureNone, Capability: "user-label"}, nil
	}), clock)
	if err != nil || records[0].Failure != FailureContract || records[0].Outcome != OutcomeBreakage {
		t.Fatalf("invalid observation = %#v, %v", records, err)
	}

	records, err = Execute(context.Background(), suite, RunOptions{OptIn: true}, RunnerFunc(func(context.Context, Invocation) (Observation, error) {
		time.Sleep(50 * time.Millisecond) // deliberately violates the runner context contract
		return Observation{Outcome: OutcomeSuccess, Failure: FailureNone}, nil
	}), clock)
	if err != nil || records[0].Outcome != OutcomeTimeout || records[0].DurationMS < spec.TimeoutMS {
		t.Fatalf("timeout records=%#v err=%v", records, err)
	}
}

func TestExecuteHonorsParentCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	records, err := Execute(ctx, fixtureSuite(t), RunOptions{OptIn: true}, RunnerFunc(func(context.Context, Invocation) (Observation, error) {
		return Observation{Outcome: OutcomeSuccess, Failure: FailureNone}, nil
	}), nil)
	if !errors.Is(err, context.Canceled) || len(records) != 0 {
		t.Fatalf("canceled execution records=%#v err=%v", records, err)
	}
}

func TestExecuteRedactsRunnerPanics(t *testing.T) {
	spec := CanarySpec{ID: "public.test", Class: ClassPublic, Extractor: "generic", TargetRef: "public.fixture", Capabilities: []string{"extract"}, TimeoutMS: 1_000}
	suite, err := NewSuite([]CanarySpec{spec})
	if err != nil {
		t.Fatal(err)
	}
	clock := &fakeClock{now: time.Unix(1_700_000_000, 0)}
	records, err := Execute(context.Background(), suite, RunOptions{OptIn: true}, RunnerFunc(func(context.Context, Invocation) (Observation, error) {
		panic("secret=do-not-export")
	}), clock)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 || records[0].Outcome != OutcomeBreakage || records[0].Failure != FailureRunner {
		t.Fatalf("panic records = %+v", records)
	}
	data, err := MarshalRecords(suite, records)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "do-not-export") {
		t.Fatalf("panic value leaked: %s", data)
	}
}

func TestExecuteRejectsNilContext(t *testing.T) {
	_, err := Execute(nil, fixtureSuite(t), RunOptions{OptIn: true}, RunnerFunc(func(context.Context, Invocation) (Observation, error) {
		return Observation{Outcome: OutcomeSuccess, Failure: FailureNone}, nil
	}), nil)
	if !errors.Is(err, ErrInvalidSpec) {
		t.Fatalf("error = %v, want %v", err, ErrInvalidSpec)
	}
}

func TestMarshalRecordsRequiresSuiteAllowlist(t *testing.T) {
	suite := fixtureSuite(t)
	record := Record{CanaryID: "public.youtube", Class: ClassPublic, Extractor: "youtube", Outcome: OutcomeSuccess, Failure: FailureNone, Capability: "arbitrary-label", StartedUnixMS: 1}
	if _, err := MarshalRecords(suite, []Record{record}); !errors.Is(err, ErrInvalidOutcome) {
		t.Fatalf("arbitrary label export error = %v", err)
	}
}
