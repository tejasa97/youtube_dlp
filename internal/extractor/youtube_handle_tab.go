package extractor

// This file intentionally implements only the small public subset of
// YoutubeTabIE needed for explicit handle browse tabs.  It does not attempt to
// resolve a handle, infer a tab, or emulate all renderer variants.

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/ytdlp-go/ytdlp/internal/value"
)

// validYouTubeHandle mirrors the pinned reference's Unicode-aware
// @[\w.-]{3,30} grammar. Python's Unicode \w accepts letters, numbers, and
// underscore; dots and hyphens are the two additional handle characters.
// Length is measured in Unicode code points, matching the reference regex.
func validYouTubeHandle(handle string) bool {
	if !utf8.ValidString(handle) || !strings.HasPrefix(handle, "@") {
		return false
	}
	value := strings.TrimPrefix(handle, "@")
	count := utf8.RuneCountInString(value)
	if count < 3 || count > 30 {
		return false
	}
	for _, character := range value {
		if character == '_' || character == '.' || character == '-' ||
			unicode.IsLetter(character) || unicode.IsNumber(character) {
			continue
		}
		return false
	}
	return true
}

var (
	ErrYouTubeHandleTabRateLimited = errors.New("YouTube handle tab rate limited")
	ErrYouTubeHandleTabNetwork     = errors.New("YouTube handle tab network failure")
)

// YouTubeHandleTab handles explicit public /@handle tab URLs. Registration is
// intentionally owned by the client package.
type YouTubeHandleTab struct{}

func NewYouTubeHandleTab() YouTubeHandleTab { return YouTubeHandleTab{} }
func (YouTubeHandleTab) Name() string       { return "youtube_handle_tab" }

func (YouTubeHandleTab) Suitable(parsed *url.URL) bool {
	_, tab, ok := youtubeHandleTabTarget(parsed)
	if !ok {
		return false
	}
	if tab == "search" {
		return youtubeChannelSearchQuery(parsed) != ""
	}
	return true
}

func (YouTubeHandleTab) Extract(ctx context.Context, request Request) (Extraction, error) {
	if err := ctx.Err(); err != nil {
		return Extraction{}, err
	}
	if request.Transport == nil {
		return Extraction{}, ErrUnsupported
	}
	parsed, err := url.Parse(request.URL)
	if err != nil {
		return Extraction{}, fmt.Errorf("%w: invalid YouTube handle tab URL", ErrUnsupported)
	}
	handle, tab, ok := youtubeHandleTabTarget(parsed)
	if !ok {
		return Extraction{}, fmt.Errorf("%w: unsupported YouTube handle tab", ErrUnsupported)
	}
	if tab == "" {
		canonical := "https://www.youtube.com/" + handle
		return extractYouTubeBareChannelUploads(ctx, request.Transport, youtubeBareChannelSpec{
			canonical: canonical, videosURL: canonical + "/videos",
			fallbackID: "handle:" + handle, subject: "handle",
			categorize: categorizeYouTubeHandleTabError,
			extractTab: func(ctx context.Context, transport Transport, tab string) (Extraction, error) {
				return extractYouTubeHandleTab(ctx, transport, handle, tab, "")
			},
		})
	}
	query := ""
	if tab == "search" {
		query = youtubeChannelSearchQuery(parsed)
	}
	return extractYouTubeHandleTab(ctx, request.Transport, handle, tab, query)
}

// youtubeHandleTabTarget is shared by Suitable and Extract.  It admits only
// canonicalizable, exact web routes; query strings are allowed but are never
// preserved in the canonical fetch URL except for channel-local search.
func youtubeHandleTabTarget(parsed *url.URL) (handle, tab string, ok bool) {
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
	if raw := strings.ToLower(parsed.RawQuery); strings.Contains(raw, "%2f") || strings.Contains(raw, "%5c") || strings.Contains(raw, "%00") {
		return "", "", false
	}
	parts := strings.Split(parsed.Path, "/")
	if (len(parts) != 2 && len(parts) != 3) || parts[0] != "" || !validYouTubeHandle(parts[1]) {
		return "", "", false
	}
	if len(parts) == 2 {
		return parts[1], "", true
	}
	tab = parts[2]
	if youtubePublicTabType(tab) != youtubeTabUnsupported || tab == "search" || validYouTubeCustomTabSegment(tab) {
		return parts[1], tab, true
	}
	return "", "", false
}

func extractYouTubeHandleTab(ctx context.Context, transport Transport, handle, tab, query string) (Extraction, error) {
	canonical := "https://www.youtube.com/" + handle + "/" + tab
	if tab == "search" {
		if !validYouTubeSearchQuery(query) {
			return Extraction{}, fmt.Errorf("%w: unsupported YouTube handle search", ErrUnsupported)
		}
		canonical = "https://www.youtube.com/" + handle + "/search?" + url.Values{"query": {query}}.Encode()
	}
	page, _, err := transport.ReadPage(ctx, canonical)
	if err != nil {
		return Extraction{}, categorizeYouTubeHandleTabError(err)
	}
	raw, err := extractJSONObject(page, youtubeInitialDataMarker)
	if err != nil {
		return Extraction{}, fmt.Errorf("%w: YouTube handle tab initial data", ErrInvalidMetadata)
	}
	if redirect, ok, err := youtubeConditionalRedirectResult(raw, canonical, tab); err != nil || ok {
		return redirect, err
	}
	identity := youtubeChannelIdentity{Handle: handle}
	if youtubePublicTabType(tab) == youtubeTabUnsupported && tab != "search" {
		if err := youtubeCustomTabSelectedAndBound(raw, tab, identity); err != nil {
			return Extraction{}, err
		}
	} else if err := validateYouTubeSelectedTab(raw, tab); err != nil {
		return Extraction{}, err
	}
	policy := youtubeRendererPolicyForTab(tab)
	if tab == "search" {
		policy = youtubeRendererPolicy{kinds: youtubeRendererVideo | youtubeRendererPlaylist | youtubeRendererChannel}
	}
	parsed, err := parseYouTubeRendererData(raw, policy)
	if err != nil {
		return Extraction{}, err
	}
	if parsed.alert != "" && len(parsed.entries) == 0 {
		return Extraction{}, youtubeHandleTabAlertError(parsed.alert)
	}
	if parsed.title == "" {
		return Extraction{}, fmt.Errorf("%w: missing YouTube handle tab metadata", ErrInvalidMetadata)
	}
	id := "handle:" + handle
	if youtubeChannelIDPattern.MatchString(parsed.channelID) {
		id = parsed.channelID
		identity.ChannelID = parsed.channelID
	}
	config := extractYouTubePlaylistConfig(page)
	visitorData := parsed.visitorData
	if visitorData == "" {
		visitorData = config.VisitorData
	}
	auth := youtubeBrowseAuthFromPage(page, transport)
	entries, err := StatefulContinuationEntries(parsed.entries, parsed.continuation, visitorData, func(ctx context.Context, token, visitorData string) ([]Entry, string, string, error) {
		return fetchYouTubeBrowseContinuation(ctx, transport, token, visitorData, config, policy, "handle", categorizeYouTubeHandleTabError, auth)
	})
	if err != nil {
		return Extraction{}, err
	}
	title := parsed.title
	if tab == "search" && query != "" {
		title = parsed.title + " - Search - " + query
	}
	return Playlist(youtubeRendererPlaylistInfo(id, title, canonical, parsed.tabs), entries)
}

// parseYouTubeHandleTabData keeps the historical helper name while routing
// through the shared renderer walker.
func parseYouTubeHandleTabData(data []byte, tab string) (youtubeRendererPage, error) {
	return parseYouTubeRendererData(data, youtubeRendererPolicyForTab(tab))
}

func youtubeHandleTabVideoEntry(renderer *value.Object) (Entry, bool) {
	return youtubeRendererVideoEntry(renderer)
}

func fetchYouTubeHandleTabContinuation(ctx context.Context, transport Transport, token, visitorData string, config youtubePlaylistConfig, tab string) ([]Entry, string, string, error) {
	return fetchYouTubeBrowseContinuation(ctx, transport, token, visitorData, config, youtubeRendererPolicyForTab(tab), "handle", categorizeYouTubeHandleTabError, nil)
}

func categorizeYouTubeHandleTabError(err error) error {
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
			return ErrYouTubeHandleTabRateLimited
		}
	}
	return fmt.Errorf("%w: request failed", ErrYouTubeHandleTabNetwork)
}

func youtubeHandleTabAlertError(alert string) error {
	lower := strings.ToLower(alert)
	if strings.Contains(lower, "private") || strings.Contains(lower, "sign in") ||
		strings.Contains(lower, "login") || strings.Contains(lower, "members only") ||
		strings.Contains(lower, "members-only") || strings.Contains(lower, "member only") ||
		strings.Contains(lower, "member-only") {
		return fmt.Errorf("%w: handle tab access denied", ErrAuthentication)
	}
	return fmt.Errorf("%w: handle tab unavailable", ErrUnavailable)
}
