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
		annotateYouTubeDirectSelection(&original, operation.request.URL, youtubeVideoIDFromSourceURL(operation.request.URL))
	}
	if operation == nil || (operation.youtubeDirectExtract == nil && (operation.client == nil || operation.transport == nil)) ||
		original.YouTubeSourceURL == "" || original.YouTubeVideoID == "" || original.YouTubeItag <= 0 ||
		original.YouTubeSABR || original.YouTubeLiveFromStart || original.YouTubePostLive ||
		!isTrustedGoogleVideoURL(original.URL) {
		return nil
	}
	rejected := map[youtubeDirectCandidateIdentity]struct{}{
		youtubeDirectCandidateIdentityOf(original): {},
	}
	rejectedURLs := map[string]struct{}{original.URL: {}}
	attemptedClients := map[string]struct{}{}
	if original.YouTubeClient != "" {
		attemptedClients[original.YouTubeClient] = struct{}{}
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
			refreshedVideoID, _ := extracted.Info.Lookup("id").StringValue()
			annotateYouTubeDirectSelection(&candidate, original.YouTubeSourceURL, refreshedVideoID)
			if candidateErr != nil || !youtubeDirectRepresentationMatches(original, candidate) || !isTrustedGoogleVideoURL(candidate.URL) {
				continue
			}
			candidate.Headers, candidateErr = mediaformat.MergeHeaders(extracted.Info.Lookup("http_headers"), object.Lookup("http_headers"))
			if candidateErr != nil {
				return downloader.RefreshResult{}, fmt.Errorf("%w: refreshed headers", errYouTubeDirectRefreshRejected)
			}
			matches = append(matches, candidate)
		}
		// Walk extraction order twice: unused clients first, then any remaining
		// unused URL/client pair. Never return a combination this download already
		// rejected, including the original media URL.
		match, ok := pickYouTubeDirectRefreshCandidate(matches, rejected, rejectedURLs, attemptedClients)
		if !ok {
			return downloader.RefreshResult{}, fmt.Errorf("%w: no distinct matching representation", errYouTubeDirectRefreshRejected)
		}
		rejected[youtubeDirectCandidateIdentityOf(match)] = struct{}{}
		rejectedURLs[match.URL] = struct{}{}
		if match.YouTubeClient != "" {
			attemptedClients[match.YouTubeClient] = struct{}{}
		}
		return downloader.RefreshResult{URL: match.URL, Headers: match.Headers, ExpectedBytes: match.Filesize}, nil
	}
}

type youtubeDirectCandidateIdentity struct {
	client string
	url    string
}

func youtubeDirectCandidateIdentityOf(selection mediaformat.Selection) youtubeDirectCandidateIdentity {
	return youtubeDirectCandidateIdentity{client: selection.YouTubeClient, url: selection.URL}
}

func pickYouTubeDirectRefreshCandidate(
	matches []mediaformat.Selection,
	rejected map[youtubeDirectCandidateIdentity]struct{},
	rejectedURLs map[string]struct{},
	attemptedClients map[string]struct{},
) (mediaformat.Selection, bool) {
	pick := func(requireUnusedClient bool) (mediaformat.Selection, bool) {
		for _, candidate := range matches {
			identity := youtubeDirectCandidateIdentityOf(candidate)
			if _, seen := rejected[identity]; seen {
				continue
			}
			if _, seen := rejectedURLs[candidate.URL]; seen {
				continue
			}
			if requireUnusedClient {
				if candidate.YouTubeClient == "" {
					continue
				}
				if _, attempted := attemptedClients[candidate.YouTubeClient]; attempted {
					continue
				}
			}
			return candidate, true
		}
		return mediaformat.Selection{}, false
	}
	if match, ok := pick(true); ok {
		return match, true
	}
	return pick(false)
}

func annotateYouTubeDirectPlans(extractorName string, info value.Info, plans []mediaformat.OutputPlan) {
	if !strings.EqualFold(extractorName, "youtube") {
		return
	}
	sourceURL, _ := info.Lookup("webpage_url").StringValue()
	videoID, _ := info.Lookup("id").StringValue()
	for planIndex := range plans {
		for trackIndex := range plans[planIndex].Tracks {
			annotateYouTubeDirectSelection(&plans[planIndex].Tracks[trackIndex], sourceURL, videoID)
		}
	}
}

func annotateYouTubeDirectSelection(selection *mediaformat.Selection, sourceURL, videoID string) {
	if selection == nil || sourceURL == "" || !isTrustedGoogleVideoURL(selection.URL) {
		return
	}
	itag, ok := youtubeDirectCanonicalItag(*selection)
	if !ok {
		return
	}
	selection.YouTubeSourceURL = sourceURL
	selection.YouTubeVideoID = videoID
	selection.YouTubeItag = itag
	selection.YouTubeDrc = selection.YouTubeDrc || strings.HasSuffix(selection.ID, "-drc")
}

func youtubeDirectCanonicalItag(selection mediaformat.Selection) (int64, bool) {
	if selection.YouTubeItag > 0 {
		return selection.YouTubeItag, true
	}
	baseID := selection.ID
	switch {
	case strings.HasSuffix(baseID, "-drc"):
		baseID = strings.TrimSuffix(baseID, "-drc")
	case strings.HasSuffix(baseID, "-sr"):
		baseID = strings.TrimSuffix(baseID, "-sr")
	}
	if baseID == "" {
		return 0, false
	}
	for _, character := range baseID {
		if character < '0' || character > '9' {
			return 0, false
		}
	}
	itag, err := strconv.ParseInt(baseID, 10, 64)
	return itag, err == nil && itag > 0
}

func youtubeDirectRepresentationMatches(original, candidate mediaformat.Selection) bool {
	if original.YouTubeSourceURL == "" || candidate.YouTubeSourceURL != original.YouTubeSourceURL ||
		original.YouTubeVideoID == "" || candidate.YouTubeVideoID != original.YouTubeVideoID ||
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

func youtubeVideoIDFromSourceURL(rawURL string) string {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}
	if strings.EqualFold(parsed.Hostname(), "youtu.be") {
		return strings.Trim(strings.TrimSpace(parsed.Path), "/")
	}
	return strings.TrimSpace(parsed.Query().Get("v"))
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
