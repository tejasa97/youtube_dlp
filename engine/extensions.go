package engine

import (
	"context"
	"time"
)

// ChallengeType identifies one player-JavaScript transform family.
type ChallengeType string

const (
	ChallengeN   ChallengeType = "n"
	ChallengeSig ChallengeType = "sig"
)

// ChallengeRequest is one bounded group of player challenges.
type ChallengeRequest struct {
	Type       ChallengeType `json:"type"`
	Challenges []string      `json:"challenges"`
}

// ChallengeResponse contains solved values or a bounded provider error.
type ChallengeResponse struct {
	Type  ChallengeType
	Data  map[string]string
	Error string
}

type ChallengeResult struct {
	Responses          []ChallengeResponse
	PreprocessedPlayer string
}

// ChallengeSolver is the typed extension seam for providers that require
// player-JavaScript transformation. Implementations remain operation-scoped.
type ChallengeSolver interface {
	SolvePlayer(context.Context, string, string, []ChallengeRequest, bool) (ChallengeResult, error)
}

type POTContext string

const (
	POTContextGVS    POTContext = "gvs"
	POTContextPlayer POTContext = "player"
	POTContextSubs   POTContext = "subs"
)

// POTRequest contains the bounded, attributable binding inputs for a
// proof-of-origin token. Formatting implementations must redact these fields.
type POTRequest struct {
	Context       POTContext
	Client        string
	VisitorData   string
	DataSyncID    string
	VideoID       string
	PlayerURL     string
	Authenticated bool
	BypassCache   bool
}

func (POTRequest) String() string { return "[redacted YouTube PO-token request]" }
func (POTRequest) GoString() string {
	return "youtubepot.Request{[redacted]}"
}

type POTResponse struct {
	Token     string
	ExpiresAt time.Time
}

func (POTResponse) String() string { return "[redacted YouTube PO-token response]" }
func (POTResponse) GoString() string {
	return "youtubepot.Response{[redacted]}"
}

// POTResolver is the typed extension seam used by a provider to resolve
// optional or required proof-of-origin tokens.
type POTResolver interface {
	ResolvePolicy(context.Context, POTRequest, bool, bool) (string, bool, error)
}
