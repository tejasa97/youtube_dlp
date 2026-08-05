// Package youtube owns the typed request options for the complete first-party
// YouTube provider family. Concrete provider implementations remain in
// internal/extractor until their dependency closure moves in a later change.
package youtube

import (
	"context"

	"github.com/tejasa97/youtube_dlp/internal/extraction"
	"github.com/tejasa97/youtube_dlp/internal/javascript/ejs"
	"github.com/tejasa97/youtube_dlp/internal/youtubepot"
)

// ChallengeSolver solves YouTube player JavaScript challenges.
type ChallengeSolver interface {
	SolvePlayer(context.Context, string, string, []ejs.ChallengeRequest, bool) (ejs.Result, error)
}

// POTDirector preserves the concrete proof-of-origin token plumbing required
// by the current provider family while assigning its request ownership here.
type POTDirector = youtubepot.Director

// CommentOptions controls opt-in comment retrieval. Zero Max selects the
// provider's bounded default. Sort accepts "top" or "new".
type CommentOptions struct {
	Enabled             bool
	Sort                string
	MaxComments         int
	MaxParents          int
	MaxReplies          int
	MaxRepliesPerThread int
	MaxDepth            int
}

// Options contains all YouTube-specific state supplied by product
// composition. Credentials and transports remain operation-scoped in the
// embedded provider-neutral request.
type Options struct {
	ChallengeSolver    ChallengeSolver
	POT                *POTDirector
	TranslatedCaptions bool
	LiveFromStart      bool
	Comments           CommentOptions
}

// Request combines provider-neutral engine state with typed YouTube options.
type Request struct {
	extraction.Request
	Options Options
}

func NewRequest(base extraction.Request, options Options) Request {
	return Request{Request: base, Options: options}
}

// Redacted formatting prevents provider options and embedded engine secrets
// from being exposed by diagnostics.
func (Request) String() string   { return "[redacted youtube request]" }
func (Request) GoString() string { return "youtube.Request{[redacted]}" }
