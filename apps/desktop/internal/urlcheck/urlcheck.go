// Package urlcheck validates incoming URLs at the desktop boundary.
//
// The desktop application is intentionally limited to single, public
// YouTube videos. Anything else (playlists, channels, search results,
// live streams, other sites) is rejected with a friendly reason that
// the UI surfaces directly.
package urlcheck

import (
	"errors"
	"net/url"
	"regexp"
	"strings"
)

// Reason is the machine-readable reason a URL was rejected. The UI
// translates the reason into user-facing copy.
type Reason string

const (
	ReasonEmpty          Reason = "empty"
	ReasonNotYouTube     Reason = "not_youtube"
	ReasonChannel        Reason = "channel"
	ReasonPlaylist       Reason = "playlist"
	ReasonSearch         Reason = "search"
	ReasonLive           Reason = "live"
	ReasonShorts         Reason = "shorts"
	ReasonInvalidScheme  Reason = "invalid_scheme"
	ReasonMalformed      Reason = "malformed"
	ReasonMissingVideoID Reason = "missing_video_id"
)

// Result describes a validated URL.
type Result struct {
	URL     string `json:"url"`
	VideoID string `json:"videoId"`
	Kind    string `json:"kind"`
}

// videoIDPattern matches the 11-character YouTube video ID.
var videoIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{11}$`)

// ErrRejected is the sentinel returned by Validate. Use errors.Is to
// detect it; the textual prefix is the Reason.
var ErrRejected = errors.New("rejected")

// Rejection keeps the machine-readable reason available to Go callers while
// exposing only clear, non-technical copy across the Wails boundary.
type Rejection struct {
	Reason  Reason
	Message string
}

func (rejection *Rejection) Error() string { return rejection.Message }

func (rejection *Rejection) Is(target error) bool { return target == ErrRejected }

func reject(reason Reason, message string) error {
	return &Rejection{Reason: reason, Message: message}
}

// Validate returns a populated Result when the URL is a single, public
// YouTube video. Otherwise it returns a Reason explaining why it was
// rejected. The Reason values are part of the app's stable contract with
// the UI.
func Validate(raw string) (Result, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return Result{}, reject(ReasonEmpty, "Paste a YouTube video link.")
	}

	parsed, err := url.Parse(trimmed)
	if err != nil {
		return Result{}, reject(ReasonMalformed, "That link is not a valid web address.")
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return Result{}, reject(ReasonInvalidScheme, "Paste a link that starts with http:// or https://.")
	}
	host := strings.ToLower(parsed.Host)
	host = strings.TrimPrefix(host, "www.")
	host = strings.TrimPrefix(host, "m.")
	if host != "youtube.com" && host != "youtu.be" && host != "youtube-nocookie.com" {
		return Result{}, reject(ReasonNotYouTube, "Only YouTube video links are supported.")
	}

	// Reject known non-video entry points early.
	switch {
	case parsed.Path == "/watch":
		if list := parsed.Query().Get("list"); list != "" {
			return Result{}, reject(ReasonPlaylist, "Playlist links are not supported yet. Paste a single YouTube video link.")
		}
	case strings.HasPrefix(parsed.Path, "/playlist"):
		return Result{}, reject(ReasonPlaylist, "Playlist links are not supported yet. Paste a single YouTube video link.")
	case parsed.Path == "/results" || strings.HasPrefix(parsed.Path, "/search"):
		return Result{}, reject(ReasonSearch, "YouTube search pages are not supported. Paste a single video link.")
	case strings.HasPrefix(parsed.Path, "/channel/") || strings.HasPrefix(parsed.Path, "/c/") || strings.HasPrefix(parsed.Path, "/@"):
		return Result{}, reject(ReasonChannel, "YouTube channel pages are not supported. Paste a single video link.")
	case strings.HasPrefix(parsed.Path, "/shorts"):
		return Result{}, reject(ReasonShorts, "YouTube Shorts are not supported in this version.")
	case parsed.Path == "/live" || strings.HasPrefix(parsed.Path, "/live/"):
		return Result{}, reject(ReasonLive, "YouTube live streams are not supported in this version.")
	}

	videoID := extractVideoID(parsed, host)
	if videoID == "" || !videoIDPattern.MatchString(videoID) {
		return Result{}, reject(ReasonMissingVideoID, "We could not find a video in that link. Copy the link directly from YouTube.")
	}

	return Result{
		URL:     canonical(parsed, videoID),
		VideoID: videoID,
		Kind:    "single_video",
	}, nil
}

// IsRejected reports whether err came from this package's validator.
func IsRejected(err error) bool { return errors.Is(err, ErrRejected) }

// ReasonOf extracts the Reason prefix from a rejection error. It returns
// an empty string when err is nil or not from this package.
func ReasonOf(err error) Reason {
	if err == nil {
		return ""
	}
	var rejection *Rejection
	if errors.As(err, &rejection) && isReason(string(rejection.Reason)) {
		return rejection.Reason
	}
	return ReasonMalformed
}

func isReason(s string) bool {
	switch Reason(s) {
	case ReasonEmpty, ReasonNotYouTube, ReasonChannel, ReasonPlaylist,
		ReasonSearch, ReasonLive, ReasonShorts, ReasonInvalidScheme,
		ReasonMalformed, ReasonMissingVideoID:
		return true
	}
	return false
}

func extractVideoID(parsed *url.URL, host string) string {
	if host == "youtu.be" {
		id := strings.TrimPrefix(parsed.Path, "/")
		id = strings.SplitN(id, "/", 2)[0]
		return id
	}
	if parsed.Path == "/watch" {
		return parsed.Query().Get("v")
	}
	if strings.HasPrefix(parsed.Path, "/embed/") || strings.HasPrefix(parsed.Path, "/v/") {
		parts := strings.Split(strings.TrimPrefix(parsed.Path, "/"), "/")
		if len(parts) >= 2 {
			return parts[1]
		}
	}
	return ""
}

func canonical(parsed *url.URL, id string) string {
	out := url.URL{
		Scheme:   "https",
		Host:     "www.youtube.com",
		Path:     "/watch",
		RawQuery: "v=" + id,
	}
	return out.String()
}
