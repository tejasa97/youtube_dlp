// Package extractor contains concrete first-party extractors and preserves the
// legacy extraction surface through aliases to internal/extraction.
package extractor

import (
	"github.com/tejasa97/ytdlp-go/internal/extraction"
	youtubeprovider "github.com/tejasa97/ytdlp-go/internal/providers/youtube"
)

type Request struct {
	URL string
	// SearchQueryOverride carries the original bounded query for a product
	// routing decision that selected an opaque registered search extractor.
	// The URL itself is a fixed safe routing token so user input cannot become
	// URL syntax or leak through routing diagnostics. It is consumed only by
	// the matching search extractor and is never rendered by Request.String.
	SearchQueryOverride string
	// Referer is an optional validated HTTPS embedding page URL propagated from
	// bounded playlist recursion. It must never carry cookies, Authorization, or
	// arbitrary caller headers.
	Referer         string
	Transport       Transport
	ChallengeSolver YouTubeChallengeSolver
	// Credentials resolves a stable extractor machine key. It must never be
	// embedded in metadata, events, or diagnostic errors. The same is true of
	// VideoPassword, which is consumed by extractors that gate media behind a
	// per-video secret and is never echoed back by the formatter.
	Credentials               CredentialProvider
	VideoPassword             string
	YouTubePOT                *youtubeprovider.POTDirector
	YouTubeTranslatedCaptions bool
	YouTubeLiveFromStart      bool
	YouTubeComments           YouTubeCommentOptions
	SoundCloudComments        SoundCloudCommentOptions
	NHK                       NHKOptions
	// NoPlaylist mirrors yt-dlp's --no-playlist / --yes-playlist toggle.
	// When true, extractors that can return a video or a playlist for the same
	// URL should prefer the video result. Pure playlist URLs are unaffected.
	NoPlaylist bool
}

// NHKOptions carries narrowly scoped NHK extractor state. The Radiru area
// mirrors yt-dlp's `nhkradirulive:area` extractor argument and is the only
// currently supported knob. Empty RadiruArea selects the extractor's
// documented default at runtime.
type NHKOptions struct {
	RadiruArea string
}

// String and GoString deliberately render Request as a fixed opaque value so
// diagnostic formatting cannot expose URL credentials, transports, providers,
// or VideoPassword. Value receivers also cover *Request formatting.
func (Request) String() string                { return "[redacted extractor request]" }
func (Request) GoString() string              { return "extractor.Request{[redacted]}" }
func (request Request) ExtractionURL() string { return request.URL }

// NeutralRequest adapts the legacy mixed request to engine-owned state.
func (request Request) NeutralRequest() extraction.Request {
	return extraction.Request{
		URL:                 request.URL,
		SearchQueryOverride: request.SearchQueryOverride,
		Referer:             request.Referer,
		Transport:           request.Transport,
		Credentials:         request.Credentials,
		VideoPassword:       request.VideoPassword,
		NoPlaylist:          request.NoPlaylist,
	}
}

// YouTubeRequest adapts the legacy mixed request to the future complete
// YouTube provider's typed request without changing existing provider APIs.
func (request Request) YouTubeRequest() youtubeprovider.Request {
	return youtubeprovider.NewRequest(request.NeutralRequest(), youtubeprovider.Options{
		ChallengeSolver:    request.ChallengeSolver,
		POT:                request.YouTubePOT,
		TranslatedCaptions: request.YouTubeTranslatedCaptions,
		LiveFromStart:      request.YouTubeLiveFromStart,
		Comments:           request.YouTubeComments,
	})
}

// YouTubeCommentOptions controls opt-in comment retrieval. Zero Max selects
// the extractor's bounded default. Sort accepts "top" or "new".
type YouTubeCommentOptions = youtubeprovider.CommentOptions

type SoundCloudCommentOptions struct {
	Enabled     bool
	Sort        string
	MaxComments int
}

type YouTubeChallengeSolver = youtubeprovider.ChallengeSolver
