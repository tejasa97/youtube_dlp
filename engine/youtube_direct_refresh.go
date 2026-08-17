package engine

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/tejasa97/youtube_dlp/internal/downloader"
	mediaformat "github.com/tejasa97/youtube_dlp/internal/format"
	"github.com/tejasa97/youtube_dlp/internal/value"
)

var errYouTubeDirectRefreshRejected = errors.New("youtube direct media refresh rejected")

// youtubeDirectRefresh returns a bounded downloader callback for an ordinary
// finite YouTube representation. Candidate selection may rotate Innertube
// clients, but it never authorizes appending bytes: the downloader separately
// validates the refreshed response against its durable range boundary.
func (operation *operation) youtubeDirectRefresh(original mediaformat.Selection) downloader.RefreshFunc {
	if operation != nil && original.YouTubeSourceURL == "" && isYouTubeWatchSourceURL(operation.request.URL) {
		annotateYouTubeDirectSelection(&original, operation.request.URL)
	}
	if operation == nil || (operation.youtubeDirectExtract == nil && (operation.client == nil || operation.transport == nil)) ||
		original.YouTubeSourceURL == "" || original.YouTubeItag <= 0 ||
		original.YouTubeSABR || original.YouTubeLiveFromStart || original.YouTubePostLive ||
		!isTrustedGoogleVideoURL(original.URL) {
		return nil
	}
	return func(ctx context.Context, request downloader.RefreshRequest) (downloader.RefreshResult, error) {
		if request.StatusCode != http.StatusForbidden {
			return downloader.RefreshResult{}, errYouTubeDirectRefreshRejected
		}
		extract := operation.extractProviderSource
		if operation.youtubeDirectExtract != nil {
			extract = operation.youtubeDirectExtract
		}
		extracted, err := extract(ctx, original.YouTubeSourceURL)
		if err != nil {
			return downloader.RefreshResult{}, err
		}
		formats, _ := extracted.Info.Formats()
		var matches []mediaformat.Selection
		for _, candidateValue := range formats {
			object, ok := candidateValue.Object()
			if !ok {
				continue
			}
			candidate, candidateErr := mediaformat.SelectionFromObject(object)
			annotateYouTubeDirectSelection(&candidate, original.YouTubeSourceURL)
			if candidateErr != nil || !youtubeDirectRepresentationMatches(original, candidate) || !isTrustedGoogleVideoURL(candidate.URL) {
				continue
			}
			candidate.Headers, candidateErr = mediaformat.MergeHeaders(extracted.Info.Lookup("http_headers"), object.Lookup("http_headers"))
			if candidateErr != nil {
				return downloader.RefreshResult{}, fmt.Errorf("%w: refreshed headers", errYouTubeDirectRefreshRejected)
			}
			matches = append(matches, candidate)
		}
		if len(matches) == 0 {
			return downloader.RefreshResult{}, fmt.Errorf("%w: no matching representation", errYouTubeDirectRefreshRejected)
		}
		// Extraction order is deterministic. Prefer the original client when it
		// is still available, but allow a different client to supply equivalent
		// bytes; the HTTP boundary remains the final authority.
		match := matches[0]
		for _, candidate := range matches {
			if candidate.YouTubeClient == original.YouTubeClient {
				match = candidate
				break
			}
		}
		return downloader.RefreshResult{URL: match.URL, Headers: match.Headers, ExpectedBytes: match.Filesize}, nil
	}
}

func annotateYouTubeDirectPlans(extractorName string, info value.Info, plans []mediaformat.OutputPlan) {
	if !strings.EqualFold(extractorName, "youtube") {
		return
	}
	sourceURL, _ := info.Lookup("webpage_url").StringValue()
	for planIndex := range plans {
		for trackIndex := range plans[planIndex].Tracks {
			annotateYouTubeDirectSelection(&plans[planIndex].Tracks[trackIndex], sourceURL)
		}
	}
}

func annotateYouTubeDirectSelection(selection *mediaformat.Selection, sourceURL string) {
	if selection == nil || sourceURL == "" || !isTrustedGoogleVideoURL(selection.URL) {
		return
	}
	baseID := strings.TrimSuffix(strings.TrimSuffix(selection.ID, "-drc"), "-sr")
	itag, err := strconv.ParseInt(baseID, 10, 64)
	if err != nil || itag <= 0 {
		return
	}
	selection.YouTubeSourceURL = sourceURL
	selection.YouTubeItag = itag
	selection.YouTubeDrc = strings.HasSuffix(selection.ID, "-drc")
}

func youtubeDirectRepresentationMatches(original, candidate mediaformat.Selection) bool {
	if original.YouTubeSourceURL == "" || candidate.YouTubeSourceURL != original.YouTubeSourceURL ||
		candidate.YouTubeItag != original.YouTubeItag || candidate.URL == "" ||
		candidate.YouTubeSABR || candidate.YouTubeLiveFromStart || candidate.YouTubePostLive ||
		candidate.VCodec != original.VCodec || candidate.ACodec != original.ACodec ||
		candidate.Ext != original.Ext || candidate.Width != original.Width ||
		candidate.Height != original.Height || candidate.FPS != original.FPS ||
		candidate.Language != original.Language || candidate.YouTubeDrc != original.YouTubeDrc ||
		candidate.YouTubeAudioTrackID != original.YouTubeAudioTrackID {
		return false
	}
	// A known extraction size is part of the representation identity. Unknown
	// sizes remain admissible, but the resumed HTTP response must prove its total.
	return original.Filesize <= 0 || (candidate.Filesize > 0 && candidate.Filesize == original.Filesize)
}

func isYouTubeWatchSourceURL(rawURL string) bool {
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Scheme != "https" || parsed.User != nil || parsed.Port() != "" {
		return false
	}
	host := strings.ToLower(parsed.Hostname())
	return host == "youtube.com" || strings.HasSuffix(host, ".youtube.com") || host == "youtu.be"
}

func isTrustedGoogleVideoURL(rawURL string) bool {
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Scheme != "https" || parsed.User != nil || parsed.Port() != "" || parsed.Fragment != "" {
		return false
	}
	host := strings.ToLower(parsed.Hostname())
	return host == "googlevideo.com" || strings.HasSuffix(host, ".googlevideo.com")
}
