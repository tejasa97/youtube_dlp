// Package youtube owns the typed request and complete concrete implementation
// dependency closure for the first-party YouTube provider family.
package youtube

import (
	"github.com/tejasa97/ytdlp-go/engine/provider"
	"github.com/tejasa97/ytdlp-go/internal/youtubepot"
)

// ChallengeSolver solves YouTube player JavaScript challenges.
type ChallengeSolver = provider.ChallengeSolver

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

// YouTubeCommentOptions preserves the implementation and test name while
// option ownership remains in this provider package.
type YouTubeCommentOptions = CommentOptions

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

// Request is the provider-owned projection of engine state plus typed YouTube
// options. NewRequest is the only product adapter; NeutralRequest allows
// provider-neutral orchestration state to be recovered without importing the
// legacy mixed extractor package.
type Request struct {
	URL                 string
	SearchQueryOverride string
	Referer             string
	Transport           provider.Transport
	Credentials         provider.CredentialProvider
	VideoPassword       string
	NoPlaylist          bool
	Options             Options
}

func NewRequest(base provider.Request, options Options) Request {
	return Request{
		URL:                 base.URL,
		SearchQueryOverride: base.SearchQueryOverride,
		Referer:             base.Referer,
		Transport:           base.Transport,
		Credentials:         base.Credentials,
		VideoPassword:       base.VideoPassword,
		NoPlaylist:          base.NoPlaylist,
		Options:             options,
	}
}

func (request Request) NeutralRequest() provider.Request {
	return provider.Request{
		URL:                 request.URL,
		SearchQueryOverride: request.SearchQueryOverride,
		Referer:             request.Referer,
		Transport:           request.Transport,
		Credentials:         request.Credentials,
		VideoPassword:       request.VideoPassword,
		NoPlaylist:          request.NoPlaylist,
	}
}

func (request Request) ExtractionURL() string { return request.URL }

// Redacted formatting prevents provider options and embedded engine secrets
// from being exposed by diagnostics.
func (Request) String() string   { return "[redacted youtube request]" }
func (Request) GoString() string { return "youtube.Request{[redacted]}" }
