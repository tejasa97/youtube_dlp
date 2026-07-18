package ejs

import (
	"context"
	"encoding/json"
	"os"
	"reflect"
	"testing"

	"github.com/ytdlp-go/ytdlp/internal/javascript/engine"
)

type corpus struct {
	Version         int    `json:"version"`
	EJSVersion      string `json:"ejs_version"`
	ReferenceCommit string `json:"reference_commit"`
	Requests        []struct {
		Type       ChallengeType     `json:"type"`
		Challenges []string          `json:"challenges"`
		Expected   map[string]string `json:"expected"`
	} `json:"requests"`
}

func TestPinnedEJSCorpus(t *testing.T) {
	player, err := os.ReadFile("../../../conformance/javascript/ejs-0.8.0/synthetic-player.js")
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile("../../../conformance/javascript/ejs-0.8.0/corpus.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture corpus
	if err := json.Unmarshal(data, &fixture); err != nil {
		t.Fatal(err)
	}
	if fixture.Version != 1 || fixture.EJSVersion != Version || fixture.ReferenceCommit == "" {
		t.Fatalf("invalid fixture provenance: %#v", fixture)
	}
	requests := make([]ChallengeRequest, len(fixture.Requests))
	for index, request := range fixture.Requests {
		requests[index] = ChallengeRequest{Type: request.Type, Challenges: request.Challenges}
	}
	solver, err := New(engine.New(4))
	if err != nil {
		t.Fatal(err)
	}
	result, err := solver.SolvePlayer(context.Background(), "ejs-corpus", string(player), requests, true)
	if err != nil {
		t.Fatal(err)
	}
	if result.PreprocessedPlayer == "" || len(result.Responses) != len(fixture.Requests) {
		t.Fatalf("result = %#v", result)
	}
	for index, response := range result.Responses {
		if response.Error != "" || !reflect.DeepEqual(response.Data, fixture.Requests[index].Expected) {
			t.Fatalf("response %d = %#v, want %#v", index, response, fixture.Requests[index].Expected)
		}
	}
}

func TestVerifyAssetsAndInputBounds(t *testing.T) {
	if err := VerifyAssets(); err != nil {
		t.Fatal(err)
	}
	solver, err := New(engine.New(1))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := solver.SolvePlayer(context.Background(), "empty", "", nil, false); err == nil {
		t.Fatal("empty player succeeded")
	}
	if _, err := solver.SolvePlayer(context.Background(), "type", "1", []ChallengeRequest{{Type: "unknown"}}, false); err == nil {
		t.Fatal("unknown challenge type succeeded")
	}
}

func TestCanonicalEmbeddedScriptOnlyNormalizesCRLFPairs(t *testing.T) {
	got := canonicalEmbeddedScript("one\r\ntwo\rthree\n")
	if got != "one\ntwo\rthree\n" {
		t.Fatalf("canonical source = %q", got)
	}
}

func TestMissingChallengeFunctionIsSanitized(t *testing.T) {
	solver, err := New(engine.New(1))
	if err != nil {
		t.Fatal(err)
	}
	player := `(function(){var marker="secret-player-data"}).call(this);`
	result, err := solver.SolvePlayer(context.Background(), "missing", player, []ChallengeRequest{{Type: ChallengeN, Challenges: []string{"secret-challenge"}}}, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Responses) != 1 || result.Responses[0].Error != "EJS challenge execution failed" {
		t.Fatalf("result = %#v", result)
	}
}
