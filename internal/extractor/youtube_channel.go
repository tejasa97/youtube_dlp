package extractor

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	"github.com/ytdlp-go/ytdlp/internal/value"
)

var youtubeChannelIDPattern = regexp.MustCompile(`^UC[A-Za-z0-9_-]{22}$`)

const youtubeMaxTabEntryTitleBytes = 4096

var (
	ErrYouTubeChannelRateLimited = errors.New("YouTube channel rate limited")
	ErrYouTubeChannelNetwork     = errors.New("YouTube channel network failure")
)

// YouTubeChannelTab handles only explicit, public channel tab URLs. Register
// it before YouTube: the video extractor intentionally remains responsible for
// watch pages and all non-tab YouTube routes.
type YouTubeChannelTab struct{}

func NewYouTubeChannelTab() YouTubeChannelTab { return YouTubeChannelTab{} }
func (YouTubeChannelTab) Name() string        { return "youtube_channel_tab" }

func (YouTubeChannelTab) Suitable(parsed *url.URL) bool {
	channelID, tab, ok := youtubeChannelTabTarget(parsed)
	if !ok {
		return false
	}
	if tab == "search" {
		return youtubeChannelSearchQuery(parsed) != ""
	}
	_ = channelID
	return true
}

func (YouTubeChannelTab) Extract(ctx context.Context, request Request) (Extraction, error) {
	if err := ctx.Err(); err != nil {
		return Extraction{}, err
	}
	parsed, err := url.Parse(request.URL)
	if err != nil || request.Transport == nil {
		return Extraction{}, ErrUnsupported
	}
	channelID, tab, ok := youtubeChannelTabTarget(parsed)
	if !ok {
		return Extraction{}, fmt.Errorf("%w: unsupported YouTube channel tab", ErrUnsupported)
	}
	if tab == "" {
		canonical := "https://www.youtube.com/channel/" + channelID
		return extractYouTubeBareChannelUploads(ctx, request.Transport, youtubeBareChannelSpec{
			canonical: canonical, videosURL: canonical + "/videos",
			fallbackID: channelID, subject: "channel",
			categorize: categorizeYouTubeChannelError,
			extractTab: func(ctx context.Context, transport Transport, tab string) (Extraction, error) {
				return extractYouTubeChannelTab(ctx, transport, channelID, tab)
			},
		})
	}
	if tab == "search" {
		query := youtubeChannelSearchQuery(parsed)
		return extractYouTubeChannelTabWithQuery(ctx, request.Transport, channelID, tab, query)
	}
	return extractYouTubeChannelTab(ctx, request.Transport, channelID, tab)
}

func youtubeChannelSearchQuery(parsed *url.URL) string {
	if parsed == nil {
		return ""
	}
	values, err := url.ParseQuery(parsed.RawQuery)
	if err != nil {
		return ""
	}
	query := values.Get("query")
	if query == "" {
		query = values.Get("q")
	}
	if !validYouTubeSearchQuery(query) {
		return ""
	}
	return query
}

// youtubeChannelTabTarget is the one strict route policy used by Suitable and
// Extract. It accepts exact public web hosts only and rejects the broad class
// of video URLs that may happen to contain a channel-looking path.
func youtubeChannelTabTarget(parsed *url.URL) (channelID, tab string, ok bool) {
	if parsed == nil || parsed.Fragment != "" || len(parsed.String()) > 4096 {
		return "", "", false
	}
	if _, _, err := validateYouTubeURLPolicy(parsed); err != nil {
		return "", "", false
	}
	host := strings.ToLower(strings.TrimSuffix(parsed.Hostname(), "."))
	if host != "youtube.com" && host != "www.youtube.com" {
		return "", "", false
	}
	raw := strings.ToLower(parsed.RawQuery)
	if strings.Contains(raw, "%2f") || strings.Contains(raw, "%5c") || strings.Contains(raw, "%00") {
		return "", "", false
	}
	// These are deliberately exact routes. Refusing RawPath prevents an
	// encoded spelling from becoming an alternate canonical form.
	if parsed.RawPath != "" {
		return "", "", false
	}
	parts := strings.Split(parsed.Path, "/")
	if (len(parts) != 3 && len(parts) != 4) || parts[0] != "" || parts[1] != "channel" || !youtubeChannelIDPattern.MatchString(parts[2]) {
		return "", "", false
	}
	if len(parts) == 3 {
		return parts[2], "", true
	}
	tab = parts[3]
	if youtubePublicTabType(tab) != youtubeTabUnsupported || tab == "search" || validYouTubeCustomTabSegment(tab) {
		return parts[2], tab, true
	}
	return "", "", false
}

func extractYouTubeChannelTab(ctx context.Context, transport Transport, channelID, tab string) (Extraction, error) {
	return extractYouTubeChannelTabWithQuery(ctx, transport, channelID, tab, "")
}

func extractYouTubeChannelTabWithQuery(ctx context.Context, transport Transport, channelID, tab, query string) (Extraction, error) {
	canonical := "https://www.youtube.com/channel/" + channelID + "/" + tab
	if tab == "search" {
		if !validYouTubeSearchQuery(query) {
			return Extraction{}, fmt.Errorf("%w: unsupported YouTube channel search", ErrUnsupported)
		}
		canonical = "https://www.youtube.com/channel/" + channelID + "/search?" + url.Values{"query": {query}}.Encode()
	}
	page, _, err := transport.ReadPage(ctx, canonical)
	if err != nil {
		return Extraction{}, categorizeYouTubeChannelError(err)
	}
	raw, err := extractJSONObject(page, youtubeInitialDataMarker)
	if err != nil {
		return Extraction{}, fmt.Errorf("%w: YouTube channel tab initial data", ErrInvalidMetadata)
	}
	if redirect, ok, err := youtubeConditionalRedirectResult(raw, canonical, tab); err != nil || ok {
		return redirect, err
	}
	identity := youtubeChannelIdentity{ChannelID: channelID}
	policy := youtubeRendererPolicyForTab(tab)
	if tab == "search" {
		policy = youtubeRendererPolicy{kinds: youtubeRendererVideo | youtubeRendererPlaylist | youtubeRendererChannel}
	}
	parsed, err := parseYouTubeRendererData(raw, policy)
	if err != nil {
		return Extraction{}, err
	}
	if youtubePublicTabType(tab) == youtubeTabUnsupported && tab != "search" {
		if err := youtubeCustomTabSelectedAndBound(raw, tab, identity); err != nil {
			return Extraction{}, err
		}
	} else if err := validateYouTubeSelectedTab(raw, tab); err != nil {
		return Extraction{}, err
	}
	if parsed.alert != "" && len(parsed.entries) == 0 {
		return Extraction{}, youtubeChannelTabAlertError(parsed.alert)
	}
	if parsed.title == "" {
		return Extraction{}, fmt.Errorf("%w: missing YouTube channel metadata", ErrInvalidMetadata)
	}
	if bound, err := youtubeBindAdvertisedTabs(raw, identity); err != nil {
		return Extraction{}, err
	} else {
		parsed.tabs = bound
	}
	config := extractYouTubePlaylistConfig(page)
	visitorData := parsed.visitorData
	if visitorData == "" {
		visitorData = config.VisitorData
	}
	auth, err := youtubeBrowseAuthFromPage(page, transport)
	if err != nil {
		return Extraction{}, categorizeYouTubeChannelError(err)
	}
	entries, err := StatefulContinuationEntries(parsed.entries, parsed.continuation, visitorData, func(ctx context.Context, token, visitorData string) ([]Entry, string, string, error) {
		return fetchYouTubeBrowseContinuation(ctx, transport, token, visitorData, config, policy, "channel", categorizeYouTubeChannelError, auth)
	})
	if err != nil {
		return Extraction{}, err
	}
	title := parsed.title
	if tab == "search" && query != "" {
		title = parsed.title + " - Search - " + query
	}
	return Playlist(youtubeRendererPlaylistInfoWithCounts(channelID, title, canonical, parsed.tabs, parsed.playlistCount, parsed.hasCount, parsed.viewCount, parsed.hasViewCount), entries)
}

// parseYouTubeChannelTabData keeps the historical test helper name while
// routing through the shared renderer walker.
func parseYouTubeChannelTabData(data []byte, tab string) (youtubeRendererPage, error) {
	return parseYouTubeRendererData(data, youtubeRendererPolicyForTab(tab))
}

func youtubeTabPlaylistEntry(renderer *value.Object) (Entry, bool) {
	playlistID := objectString(renderer, "playlistId")
	if !youtubePlaylistIDPattern.MatchString(playlistID) {
		return Entry{}, false
	}
	return youtubeTabPlaylistResult(playlistID, rendererText(renderer.Lookup("title"))), true
}

func youtubeTabPlaylistLockupEntry(viewModel *value.Object) (Entry, bool) {
	switch objectString(viewModel, "contentType") {
	case "LOCKUP_CONTENT_TYPE_PLAYLIST", "LOCKUP_CONTENT_TYPE_PODCAST":
	default:
		return Entry{}, false
	}
	playlistID := objectString(viewModel, "contentId")
	if !youtubePlaylistIDPattern.MatchString(playlistID) {
		return Entry{}, false
	}
	title := objectString(viewModel, "metadata", "lockupMetadataViewModel", "title", "content")
	return youtubeTabPlaylistResult(playlistID, title), true
}

func youtubeTabPlaylistResult(playlistID, title string) Entry {
	if len(title) > youtubeMaxTabEntryTitleBytes || strings.ContainsRune(title, 0) {
		title = ""
	}
	return Entry{
		URL: "https://www.youtube.com/playlist?list=" + playlistID, ExtractorKey: "youtube",
		ID: playlistID, Title: title,
	}
}

func fetchYouTubeChannelContinuation(ctx context.Context, transport Transport, token, visitorData string, config youtubePlaylistConfig, tab string) ([]Entry, string, string, error) {
	return fetchYouTubeBrowseContinuation(ctx, transport, token, visitorData, config, youtubeRendererPolicyForTab(tab), "channel", categorizeYouTubeChannelError, nil)
}

func youtubeChannelTabAlertError(alert string) error {
	lower := strings.ToLower(alert)
	if strings.Contains(lower, "private") || strings.Contains(lower, "sign in") ||
		strings.Contains(lower, "login") || strings.Contains(lower, "members only") ||
		strings.Contains(lower, "members-only") || strings.Contains(lower, "member only") ||
		strings.Contains(lower, "member-only") {
		return fmt.Errorf("%w: channel tab access denied", ErrAuthentication)
	}
	return fmt.Errorf("%w: channel tab unavailable", ErrUnavailable)
}

func categorizeYouTubeChannelError(err error) error {
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	if errors.Is(err, ErrInvalidMetadata) || errors.Is(err, ErrJSONResponseTooLarge) ||
		errors.Is(err, ErrInvalidPlaylist) || errors.Is(err, ErrAuthentication) ||
		errors.Is(err, ErrUnavailable) {
		return err
	}
	var status *HTTPStatusError
	if errors.As(err, &status) {
		switch status.Code {
		case http.StatusUnauthorized, http.StatusForbidden:
			return ErrAuthentication
		case http.StatusNotFound, http.StatusGone:
			return ErrUnavailable
		case http.StatusTooManyRequests:
			return ErrYouTubeChannelRateLimited
		}
	}
	return fmt.Errorf("%w: request failed", ErrYouTubeChannelNetwork)
}
