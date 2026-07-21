// Package ejs integrates the pinned yt-dlp EJS solver bundle with the isolated
// JavaScript helper.
package ejs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/ytdlp-go/ytdlp/internal/javascript/protocol"
)

const (
	MaxPlayerBytes    = 8 << 20
	MaxChallenges     = 256
	MaxChallengeBytes = 16 << 10
	SolverMemoryBytes = 128 << 20
	SolverOutputBytes = 8 << 20
	SolverSourceBytes = 2 << 20
	// Current YouTube player programs can take longer than ten seconds to
	// preprocess in the isolated pure-Go runtime. Keep execution bounded at the
	// protocol hard limit while allowing those valid programs to complete.
	SolverWallTimeMS = 30_000
)

type ChallengeType string

const (
	ChallengeN   ChallengeType = "n"
	ChallengeSig ChallengeType = "sig"
)

type Executor interface {
	Execute(context.Context, protocol.Request) protocol.Response
}

type ChallengeRequest struct {
	Type       ChallengeType `json:"type"`
	Challenges []string      `json:"challenges"`
}

type ChallengeResponse struct {
	Type  ChallengeType
	Data  map[string]string
	Error string
}

type Result struct {
	Responses          []ChallengeResponse
	PreprocessedPlayer string
}

type Solver struct {
	executor Executor
	script   string
}

func New(executor Executor) (*Solver, error) {
	if executor == nil {
		return nil, errors.New("EJS executor is required")
	}
	script, err := bundledScript()
	if err != nil {
		return nil, err
	}
	return &Solver{executor: executor, script: script}, nil
}

// SolvePlayer preprocesses one player and solves ordered n/sig request groups.
func (solver *Solver) SolvePlayer(ctx context.Context, id, player string, requests []ChallengeRequest, outputPreprocessed bool) (Result, error) {
	if len(player) == 0 || len(player) > MaxPlayerBytes {
		return Result{}, fmt.Errorf("player source must contain 1-%d bytes", MaxPlayerBytes)
	}
	if err := validateChallenges(requests); err != nil {
		return Result{}, err
	}
	input := struct {
		Type               string             `json:"type"`
		Player             string             `json:"player"`
		Requests           []ChallengeRequest `json:"requests"`
		OutputPreprocessed bool               `json:"output_preprocessed"`
	}{"player", player, requests, outputPreprocessed}
	argument, err := json.Marshal(input)
	if err != nil {
		return Result{}, fmt.Errorf("encode EJS input: %w", err)
	}
	response := solver.executor.Execute(ctx, protocol.Request{
		Version: protocol.Version, ID: id, Operation: protocol.OperationCall,
		Script: solver.script, Function: "jsc", Arguments: []json.RawMessage{argument},
		Limits: protocol.Limits{
			WallTimeMS: SolverWallTimeMS, MemoryBytes: SolverMemoryBytes,
			OutputBytes: SolverOutputBytes, SourceBytes: SolverSourceBytes,
		},
	})
	if response.Error != nil {
		return Result{}, fmt.Errorf("EJS helper %s: %s", response.Error.Code, response.Error.Message)
	}
	return decodeOutput(response.Result, requests)
}

func validateChallenges(requests []ChallengeRequest) error {
	total := 0
	for index, request := range requests {
		if request.Type != ChallengeN && request.Type != ChallengeSig {
			return fmt.Errorf("request %d has unsupported challenge type %q", index, request.Type)
		}
		total += len(request.Challenges)
		if total > MaxChallenges {
			return fmt.Errorf("challenge count exceeds %d", MaxChallenges)
		}
		for _, challenge := range request.Challenges {
			if len(challenge) > MaxChallengeBytes {
				return fmt.Errorf("challenge exceeds %d bytes", MaxChallengeBytes)
			}
		}
	}
	return nil
}

type outputEnvelope struct {
	Type               string `json:"type"`
	Error              string `json:"error"`
	PreprocessedPlayer string `json:"preprocessed_player"`
	Responses          []struct {
		Type  string            `json:"type"`
		Data  map[string]string `json:"data"`
		Error string            `json:"error"`
	} `json:"responses"`
}

func decodeOutput(payload []byte, requests []ChallengeRequest) (Result, error) {
	var output outputEnvelope
	if err := json.Unmarshal(payload, &output); err != nil {
		return Result{}, errors.New("EJS returned malformed JSON")
	}
	if output.Type != "result" {
		return Result{}, errors.New("EJS solver failed")
	}
	if len(output.Responses) != len(requests) {
		return Result{}, errors.New("EJS response count mismatch")
	}
	result := Result{PreprocessedPlayer: output.PreprocessedPlayer, Responses: make([]ChallengeResponse, len(requests))}
	for index, response := range output.Responses {
		result.Responses[index].Type = requests[index].Type
		switch response.Type {
		case "result":
			result.Responses[index].Data = response.Data
		case "error":
			result.Responses[index].Error = sanitizeSolverError(response.Error)
		default:
			return Result{}, fmt.Errorf("EJS response %d has invalid type", index)
		}
	}
	return result, nil
}

func sanitizeSolverError(message string) string {
	if strings.Contains(message, "Failed to extract") {
		return "EJS failed to extract challenge function"
	}
	return "EJS challenge execution failed"
}
