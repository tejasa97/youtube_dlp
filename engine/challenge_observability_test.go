package engine

import (
	"context"
	"errors"
	"strings"
	"testing"

	providerapi "github.com/tejasa97/ytdlp-go/engine/provider"
)

type stubChallengeSolver struct {
	result      providerapi.ChallengeResult
	err         error
	available   bool
	unavailable bool
}

func (solver stubChallengeSolver) SolvePlayer(context.Context, string, string, []providerapi.ChallengeRequest, bool) (providerapi.ChallengeResult, error) {
	return solver.result, solver.err
}

func (solver stubChallengeSolver) Available() bool {
	if solver.unavailable {
		return false
	}
	return solver.available
}

func TestObservingChallengeSolverEmitsSecretFreeEvent(t *testing.T) {
	var events []Event
	client := &Client{handler: func(_ context.Context, event Event) error {
		events = append(events, event)
		return nil
	}}
	inner := stubChallengeSolver{
		result: providerapi.ChallengeResult{
			Diagnostics: providerapi.ChallengeDiagnostics{
				Cache:            providerapi.ChallengeCacheMiss,
				PreprocessBucket: providerapi.ChallengeBucket100ms1s,
				SolveBucket:      providerapi.ChallengeBucketLT10ms,
			},
		},
		available: true,
	}
	solver := newObservingChallengeSolver(inner, client)
	if availability, ok := solver.(interface{ Available() bool }); !ok || !availability.Available() {
		t.Fatal("observing solver must pass through Available")
	}
	if _, err := solver.SolvePlayer(context.Background(), "youtube-fixture0001", "player-source", nil, false); err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 {
		t.Fatalf("events=%d", len(events))
	}
	event := events[0]
	if event.Kind != EventJavaScriptChallenge {
		t.Fatalf("kind=%q", event.Kind)
	}
	if event.URL != "" || event.Path != "" || event.Extractor != "" {
		t.Fatalf("event leaked identity fields: %#v", event)
	}
	if event.Message != "stage=ejs cache=miss preprocess=100ms_1s solve=lt_10ms" {
		t.Fatalf("message=%q", event.Message)
	}
}

func TestObservingChallengeSolverSanitizesHostileDiagnostics(t *testing.T) {
	var events []Event
	client := &Client{handler: func(_ context.Context, event Event) error {
		events = append(events, event)
		return nil
	}}
	inner := stubChallengeSolver{
		err: &providerapi.ChallengeFailure{
			Diagnostics: providerapi.ChallengeDiagnostics{
				Cache:            "https://www.youtube.com/watch?v=dQw4w9WgXcQ",
				PreprocessBucket: "Cookie: SID=secret",
				SolveBucket:      "https://googlevideo.com/videoplayback",
				HelperCategory:   "n=IlLiA21ny7gqA2m4p37",
				Phase:            "player.js?hash=28035902e6a4",
			},
			Err: errors.New("https://example.test/player.js"),
		},
	}
	if _, err := newObservingChallengeSolver(inner, client).SolvePlayer(context.Background(), "id", "player", nil, false); err == nil {
		t.Fatal("expected failure")
	}
	if len(events) != 1 {
		t.Fatalf("events=%d", len(events))
	}
	message := events[0].Message
	for _, secret := range []string{
		"youtube.com", "dQw4w9WgXcQ", "SID=", "googlevideo", "IlLiA21ny7gq", "28035902", "player.js",
	} {
		if strings.Contains(message, secret) || strings.Contains(events[0].URL, secret) {
			t.Fatalf("leaked %q via %#v", secret, events[0])
		}
	}
	if events[0].URL != "" {
		t.Fatalf("url=%q", events[0].URL)
	}
	if !strings.Contains(message, "stage=ejs") || !strings.Contains(message, "error=unknown") {
		t.Fatalf("message=%q", message)
	}
}

func TestObservingChallengeSolverPreservesSolverAndEventFailures(t *testing.T) {
	solverErr := errors.New("solver failed")
	handlerErr := errors.New("handler failed")
	client := &Client{handler: func(context.Context, Event) error { return handlerErr }}
	solver := newObservingChallengeSolver(stubChallengeSolver{err: solverErr}, client)

	_, err := solver.SolvePlayer(context.Background(), "id", "player", nil, false)
	if !errors.Is(err, solverErr) || !errors.Is(err, handlerErr) {
		t.Fatalf("combined error lost cause: %v", err)
	}
	if !IsCategory(err, ErrorInternal) {
		t.Fatalf("event handler failure was not categorized internal: %v", err)
	}
}

func TestObservingChallengeSolverNilInnerIsUnavailable(t *testing.T) {
	var events []Event
	client := &Client{handler: func(_ context.Context, event Event) error {
		events = append(events, event)
		return nil
	}}
	solver := newObservingChallengeSolver(nil, client)
	if solver.(interface{ Available() bool }).Available() {
		t.Fatal("nil inner must be unavailable")
	}
	_, err := solver.SolvePlayer(context.Background(), "id", "player", nil, false)
	if !errors.Is(err, providerapi.ErrChallengeSolver) {
		t.Fatalf("err=%v", err)
	}
	if providerapi.ChallengePipelineStage(err) != providerapi.ChallengeStageEJS {
		t.Fatal("nil solver failure must remain an EJS stage")
	}
	if len(events) != 1 || !strings.Contains(events[0].Message, "error=unavailable") {
		t.Fatalf("events=%#v", events)
	}
}
