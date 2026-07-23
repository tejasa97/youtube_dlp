package extractor

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"mime"
	"net/url"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/ytdlp-go/ytdlp/internal/javascript/ejs"
	"github.com/ytdlp-go/ytdlp/internal/value"
)

const youtubePlayerMarker = "ytInitialPlayerResponse"

var youtubeIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{11}$`)

const (
	youtubeMaxPageConfigs       = 8
	youtubeMaxConfigStartOffset = 64
)

type youtubePageConfig struct {
	PlayerJSURL    string `json:"PLAYER_JS_URL"`
	VisitorData    string `json:"VISITOR_DATA"`
	LoggedIn       *bool  `json:"LOGGED_IN"`
	PlayerContexts map[string]struct {
		JSURL string `json:"jsUrl"`
	} `json:"WEB_PLAYER_CONTEXT_CONFIGS"`
}

// youtubeHostKind classifies a parsed URL host into one of three categories.
// Both Suitable and parseYouTubeTarget use classifyYouTubeHost so the two
// views of the host policy cannot drift apart.
type youtubeHostKind int

const (
	hostUnknown  youtubeHostKind = iota // unrecognized host
	hostStandard                        // youtube.com, *.youtube.com, youtu.be
	hostNoCookie                        // youtube-nocookie.com, www.youtube-nocookie.com
)

// classifyYouTubeHost lower-cases the hostname of parsed (stripping any
// trailing dot) and returns the normalized host string together with its
// classification. Only the exact apex and www nocookie hosts are recognized;
// subdomains, lookalikes, and suffix-confusion domains yield hostUnknown.
func classifyYouTubeHost(parsed *url.URL) (string, youtubeHostKind) {
	if parsed == nil {
		return "", hostUnknown
	}
	host := strings.ToLower(strings.TrimSuffix(parsed.Hostname(), "."))
	switch {
	case host == "youtube.com" || strings.HasSuffix(host, ".youtube.com") || host == "youtu.be":
		return host, hostStandard
	case host == "youtube-nocookie.com" || host == "www.youtube-nocookie.com":
		return host, hostNoCookie
	default:
		return host, hostUnknown
	}
}

// validateYouTubeURLPolicy enforces the shared URL-security policy that every
// YouTube route (video, playlist, live-alias) must satisfy before dispatch.
// It rejects non-HTTP(S) schemes, userinfo, explicit ports, encoded path
// separators (%2f, %5c), encoded NUL bytes (%00), and unrecognized hosts.
// On success it returns the normalized host and its classification.
func validateYouTubeURLPolicy(parsed *url.URL) (string, youtubeHostKind, error) {
	if parsed.Scheme != "http" && parsed.Scheme != "https" && parsed.Scheme != "" {
		return "", hostUnknown, fmt.Errorf("%w: unsupported URL scheme", ErrUnsupported)
	}
	if parsed.User != nil || strings.Contains(parsed.Host, ":") {
		return "", hostUnknown, fmt.Errorf("%w: unsupported YouTube URL form", ErrUnsupported)
	}
	if rawPath := strings.ToLower(parsed.EscapedPath()); strings.Contains(rawPath, "%2f") || strings.Contains(rawPath, "%5c") || strings.Contains(rawPath, "%00") {
		return "", hostUnknown, fmt.Errorf("%w: unsupported YouTube URL form", ErrUnsupported)
	}
	host, kind := classifyYouTubeHost(parsed)
	if kind == hostUnknown {
		return "", hostUnknown, fmt.Errorf("%w: unsupported host", ErrUnsupported)
	}
	return host, kind, nil
}

type YouTube struct{}

func NewYouTube() YouTube { return YouTube{} }

func (YouTube) Name() string { return "youtube" }

func (YouTube) Suitable(parsed *url.URL) bool {
	_, kind := classifyYouTubeHost(parsed)
	return kind != hostUnknown
}

func (YouTube) Extract(ctx context.Context, request Request) (Extraction, error) {
	// Apply the shared URL-security policy before any routing so that
	// playlist, live-alias, and video paths cannot be reached with hostile
	// URL forms (bad schemes, userinfo, ports, encoded separators, or
	// unrecognized hosts).
	parsed, parseErr := url.Parse(request.URL)
	if parseErr != nil {
		return Extraction{}, fmt.Errorf("%w: invalid YouTube URL", ErrUnsupported)
	}
	_, kind, policyErr := validateYouTubeURLPolicy(parsed)
	if policyErr != nil {
		return Extraction{}, policyErr
	}
	// Privacy-enhanced embed hosts never serve playlists or channel-live
	// alias pages; skip those branches so parseYouTubeTarget can reject
	// every non-/embed/ path uniformly on nocookie hosts.
	if kind == hostStandard {
		if playlistID, ok := youtubePlaylistID(request.URL); ok {
			return extractYouTubePlaylist(ctx, request, playlistID)
		}
		if aliasURL, ok := youtubeChannelLiveAliasURL(request.URL); ok {
			request.URL = aliasURL
			return extractYouTubeChannelLiveAlias(ctx, request)
		}
	}
	target, err := parseYouTubeTarget(request.URL)
	if err != nil {
		return Extraction{}, err
	}
	videoID := target.videoID
	webpageURL := "https://www.youtube.com/watch?v=" + videoID
	page, _, err := request.Transport.ReadPage(ctx, webpageURL)
	if err != nil {
		return Extraction{}, err
	}
	rawPlayer, err := extractJSONObject(page, youtubePlayerMarker)
	if err != nil {
		return Extraction{}, fmt.Errorf("%w: player response: %v", ErrInvalidMetadata, err)
	}
	var player youtubePlayerResponse
	if err := json.Unmarshal(rawPlayer, &player); err != nil {
		return Extraction{}, fmt.Errorf("%w: decode player response: %v", ErrInvalidMetadata, err)
	}
	if player.VideoDetails.VideoID != "" && player.VideoDetails.VideoID != videoID {
		return Extraction{}, fmt.Errorf("%w: response video id mismatch", ErrInvalidMetadata)
	}
	if err := checkYouTubeAvailability(player.PlayabilityStatus); err != nil {
		return Extraction{}, err
	}
	pageConfig := discoverYouTubePageConfig(page)
	playerPath := pageConfig.playerPath(player.Assets.JS)
	player.clientName = "WEB"
	player.visitorData = pageConfig.visitorData(player.ResponseContext.VisitorData)
	player.playerURL = playerPath
	formatPlayers := []youtubePlayerResponse{player}
	if !hasYouTubeFormatCandidates(player) {
		if pageConfig.LoggedIn != nil && *pageConfig.LoggedIn {
			return Extraction{}, fmt.Errorf("%w: authenticated YouTube format recovery is not implemented", ErrAuthentication)
		}
		visitorData := pageConfig.visitorData(player.ResponseContext.VisitorData)
		recovered, err := recoverYouTubeFormats(ctx, request.Transport, videoID, visitorData, playerPath, request.YouTubePOT)
		if err != nil {
			return Extraction{}, err
		}
		formatPlayers = append(formatPlayers, recovered...)
		for _, recoveredPlayer := range recovered {
			if playerPath == "" {
				playerPath = recoveredPlayer.Assets.JS
			}
			if player.VideoDetails.Title == "" {
				player.VideoDetails = recoveredPlayer.VideoDetails
			}
		}
	}

	captionResult, err := normalizeYouTubeCaptions(ctx, formatPlayers, videoID, request.YouTubePOT, request.YouTubeTranslatedCaptions)
	if err != nil {
		return Extraction{}, err
	}
	formats := mergeYouTubeFormats(formatPlayers)
	applyYouTubeAudioLanguage(formats, captionResult.audioLanguage)
	resolved, err := resolveYouTubeURLs(ctx, request, webpageURL, videoID, playerPath, formats)
	if err != nil {
		return Extraction{}, err
	}
	details := firstYouTubeVideoDetails(formatPlayers)
	if details.Title == "" {
		return Extraction{}, fmt.Errorf("%w: missing title", ErrInvalidMetadata)
	}
	liveStatus := youtubeLiveStatusFromPlayers(formatPlayers)
	activeFromStart := liveStatus == "is_live" && request.YouTubeLiveFromStart
	startTimestamp, hasStart := firstYouTubeLiveTimestamp(formatPlayers, true)
	endTimestamp, hasEnd := firstYouTubeLiveTimestamp(formatPlayers, false)
	duration, hasDuration := int64(0), false
	if parsed, parseErr := strconv.ParseInt(details.LengthSeconds, 10, 64); parseErr == nil && parsed >= 0 {
		duration, hasDuration = parsed, true
	} else if hasStart && hasEnd && endTimestamp >= startTimestamp {
		duration, hasDuration = endTimestamp-startTimestamp, true
	}
	formatValues := make([]value.Value, 0, len(resolved)+2)
	for _, format := range resolved {
		targetDurationValid := format.TargetDurationSec > 0 &&
			!math.IsNaN(format.TargetDurationSec) && !math.IsInf(format.TargetDurationSec, 0)
		if activeFromStart && !targetDurationValid {
			continue
		}
		if normalized, ok := normalizeYouTubeFormat(format); ok {
			if activeFromStart {
				normalized.Set("protocol", value.String("http_dash_segments_generator"))
				normalized.Set("target_duration", value.Float(format.TargetDurationSec))
				normalized.Set("_youtube_live_from_start", value.Bool(true))
				normalized.Set("_youtube_itag", value.Int(int64(format.Itag)))
				normalized.Set("_youtube_client", value.String(format.clientName))
				normalized.Set("_youtube_source_url", value.String(webpageURL))
				if hasStart {
					normalized.Set("live_start_timestamp", value.Int(startTimestamp))
				}
			} else if liveStatus == "post_live" && targetDurationValid {
				normalized.Set("protocol", value.String("http_dash_segments"))
				normalized.Set("target_duration", value.Float(format.TargetDurationSec))
				normalized.Set("_youtube_post_live", value.Bool(true))
				normalized.Set("_youtube_itag", value.Int(int64(format.Itag)))
				normalized.Set("_youtube_client", value.String(format.clientName))
				normalized.Set("_youtube_source_url", value.String(webpageURL))
				if hasStart {
					normalized.Set("live_start_timestamp", value.Int(startTimestamp))
				}
			} else if liveStatus == "post_live" {
				// Keep incomplete current-edge formats available for explicit
				// format-ID selection while preferring the finite DVR tracks.
				normalized.Set("preference", value.Int(-10))
			}
			formatValues = append(formatValues, value.ObjectValue(normalized))
		}
	}
	for _, candidate := range formatPlayers {
		if liveStatus != "post_live" && !activeFromStart && candidate.StreamingData.HLSManifestURL != "" {
			formatValues = append(formatValues, value.ObjectValue(manifestFormat("hls", candidate.StreamingData.HLSManifestURL, "m3u8_native")))
		}
		if !activeFromStart && candidate.StreamingData.DASHManifestURL != "" && !(liveStatus == "post_live" && hasDuration && duration > 2*60*60) {
			formatValues = append(formatValues, value.ObjectValue(manifestFormat("dash", candidate.StreamingData.DASHManifestURL, "http_dash_segments")))
		}
	}
	if len(formatValues) == 0 {
		if hasYouTubeSABR(formatPlayers) {
			return Extraction{}, fmt.Errorf("%w: YouTube returned SABR-only formats", ErrUnavailable)
		}
		return Extraction{}, fmt.Errorf("%w: no downloadable formats", ErrUnavailable)
	}

	info := value.NewObject(
		value.Field{Key: "id", Value: value.String(videoID)},
		value.Field{Key: "title", Value: value.String(details.Title)},
		value.Field{Key: "description", Value: value.String(details.ShortDescription)},
		value.Field{Key: "uploader", Value: value.String(details.Author)},
		value.Field{Key: "channel_id", Value: value.String(details.ChannelID)},
		value.Field{Key: "webpage_url", Value: value.String(webpageURL)},
		value.Field{Key: "ext", Value: value.String("mp4")},
		value.Field{Key: "formats", Value: value.List(formatValues...)},
	)
	if hasDuration {
		info.Set("duration", value.Int(duration))
	}
	if hasStart {
		info.Set("release_timestamp", value.Int(startTimestamp))
	}
	if views, err := strconv.ParseInt(details.ViewCount, 10, 64); err == nil {
		info.Set("view_count", value.Int(views))
	}
	if len(details.Thumbnail.Thumbnails) > 0 {
		thumbnail := details.Thumbnail.Thumbnails[len(details.Thumbnail.Thumbnails)-1]
		info.Set("thumbnail", value.String(thumbnail.URL))
	}
	if liveStatus != "" {
		info.Set("live_status", value.String(liveStatus))
	}
	if captionResult.subtitles.Len() != 0 {
		info.Set("subtitles", value.ObjectValue(captionResult.subtitles))
	}
	if captionResult.automaticCaptions.Len() != 0 {
		info.Set("automatic_captions", value.ObjectValue(captionResult.automaticCaptions))
	}
	if target.startTime != nil {
		setYouTubeOffset(info, "start_time", *target.startTime)
	}
	if target.endTime != nil {
		setYouTubeOffset(info, "end_time", *target.endTime)
	}
	result := Media(value.NewInfo(info))
	if request.YouTubeComments.Enabled {
		options := request.YouTubeComments
		result.Enrich = func(ctx context.Context, target *value.Info) error {
			comments, disabled, err := extractYouTubeComments(ctx, request.Transport, page, videoID, options)
			if err != nil {
				return err
			}
			fields := target.Fields()
			if disabled {
				fields.Set("comments", value.Null())
				fields.Set("comment_count", value.Null())
				return nil
			}
			fields.Set("comments", value.List(comments...))
			fields.Set("comment_count", value.Int(int64(len(comments))))
			return nil
		}
	}
	return result, nil
}

func youtubeChannelLiveAlias(rawURL string) bool {
	_, ok := youtubeChannelLiveAliasURL(rawURL)
	return ok
}

func youtubeChannelLiveAliasURL(rawURL string) (string, bool) {
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.User != nil || strings.Contains(parsed.Host, ":") || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return "", false
	}
	rawPath := strings.ToLower(parsed.EscapedPath())
	if strings.Contains(rawPath, "%2f") || strings.Contains(rawPath, "%5c") || strings.Contains(rawPath, "%00") {
		return "", false
	}
	host := strings.ToLower(strings.TrimSuffix(parsed.Hostname(), "."))
	if host != "youtube.com" && !strings.HasSuffix(host, ".youtube.com") {
		return "", false
	}
	pathValue := strings.Trim(parsed.Path, "/")
	parts := strings.Split(pathValue, "/")
	validName := func(name string) bool {
		return name != "" && len(name) <= 200 && !strings.ContainsAny(name, "\x00\\")
	}
	valid := false
	switch {
	case len(parts) == 2 && strings.HasPrefix(parts[0], "@"):
		valid = parts[1] == "live" && validName(strings.TrimPrefix(parts[0], "@"))
	case len(parts) == 3 && (parts[0] == "channel" || parts[0] == "user" || parts[0] == "c"):
		valid = parts[2] == "live" && validName(parts[1])
	case len(parts) == 2 && !youtubeReservedChannelName(parts[0]):
		valid = parts[1] == "live" && validName(parts[0])
	}
	if !valid {
		return "", false
	}
	canonical := &url.URL{Scheme: "https", Host: "www.youtube.com", Path: "/" + pathValue}
	return canonical.String(), true
}

func youtubeReservedChannelName(name string) bool {
	switch strings.ToLower(name) {
	case "about", "account", "api", "browse", "c", "channel", "clip", "e", "embed", "explore",
		"feed", "feeds", "get_video_info", "hashtag", "iframe_api", "index", "live", "logout",
		"movies", "oembed", "oops", "playlist", "redirect", "results", "s", "search", "shared",
		"shorts", "signin", "source", "storefront", "t", "trending", "upload", "user", "v", "w",
		"watch", "watch_popup", "youtubei":
		return true
	default:
		return false
	}
}

func extractYouTubeChannelLiveAlias(ctx context.Context, request Request) (Extraction, error) {
	page, _, err := request.Transport.ReadPage(ctx, request.URL)
	if err != nil {
		return Extraction{}, err
	}
	rawPlayer, err := extractJSONObject(page, youtubePlayerMarker)
	if err != nil {
		return Extraction{}, fmt.Errorf("%w: channel is not currently live", ErrUnavailable)
	}
	var player youtubePlayerResponse
	if err := json.Unmarshal(rawPlayer, &player); err != nil {
		return Extraction{}, fmt.Errorf("%w: decode channel live player response", ErrInvalidMetadata)
	}
	if err := checkYouTubeAvailability(player.PlayabilityStatus); err != nil {
		return Extraction{}, err
	}
	videoID := player.VideoDetails.VideoID
	if !youtubeIDPattern.MatchString(videoID) {
		return Extraction{}, fmt.Errorf("%w: channel live page has no valid video", ErrUnavailable)
	}
	request.URL = "https://www.youtube.com/watch?v=" + videoID
	return NewYouTube().Extract(ctx, request)
}

type youtubePlayerResponse struct {
	PlayabilityStatus youtubePlayabilityStatus `json:"playabilityStatus"`
	VideoDetails      youtubeVideoDetails      `json:"videoDetails"`
	StreamingData     struct {
		Formats         []youtubeFormat `json:"formats"`
		AdaptiveFormats []youtubeFormat `json:"adaptiveFormats"`
		HLSManifestURL  string          `json:"hlsManifestUrl"`
		DASHManifestURL string          `json:"dashManifestUrl"`
		ServerABRURL    string          `json:"serverAbrStreamingUrl"`
	} `json:"streamingData"`
	Assets struct {
		JS string `json:"js"`
	} `json:"assets"`
	ResponseContext struct {
		VisitorData string `json:"visitorData"`
	} `json:"responseContext"`
	Captions struct {
		Tracklist youtubeCaptionTracklist `json:"playerCaptionsTracklistRenderer"`
	} `json:"captions"`
	Microformat struct {
		PlayerMicroformatRenderer struct {
			LiveBroadcastDetails struct {
				StartTimestamp string `json:"startTimestamp"`
				EndTimestamp   string `json:"endTimestamp"`
			} `json:"liveBroadcastDetails"`
		} `json:"playerMicroformatRenderer"`
	} `json:"microformat"`

	clientName          string
	visitorData         string
	playerURL           string
	playerTokenProvided bool
	subsPolicy          youtubePOTPolicy
}

func (config youtubePageConfig) playerPath(assetPath string) string {
	if config.PlayerJSURL != "" {
		return config.PlayerJSURL
	}
	keys := make([]string, 0, len(config.PlayerContexts))
	for key := range config.PlayerContexts {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		if playerPath := config.PlayerContexts[key].JSURL; playerPath != "" {
			return playerPath
		}
	}
	return assetPath
}

func (config youtubePageConfig) visitorData(responseVisitorData string) string {
	if config.VisitorData != "" {
		return config.VisitorData
	}
	return responseVisitorData
}

func discoverYouTubePageConfig(page []byte) youtubePageConfig {
	var result youtubePageConfig
	searchOffset := 0
	for count := 0; count < youtubeMaxPageConfigs && searchOffset < len(page); count++ {
		markerOffset, markerLength := nextYouTubeConfigMarker(page, searchOffset)
		if markerOffset < 0 {
			break
		}
		rawConfig, end, err := extractJSONObjectFrom(page, markerOffset+markerLength, youtubeMaxConfigStartOffset)
		if err != nil {
			searchOffset = markerOffset + markerLength
			continue
		}
		var candidate youtubePageConfig
		if json.Unmarshal(rawConfig, &candidate) == nil {
			mergeYouTubePageConfig(&result, candidate)
		}
		searchOffset = end
	}
	return result
}

func nextYouTubeConfigMarker(page []byte, offset int) (int, int) {
	bestOffset, bestLength := -1, 0
	for _, marker := range []string{"ytcfg.set", "ytcfg.data_"} {
		relative := bytes.Index(page[offset:], []byte(marker))
		if relative < 0 {
			continue
		}
		absolute := offset + relative
		if bestOffset < 0 || absolute < bestOffset {
			bestOffset, bestLength = absolute, len(marker)
		}
	}
	return bestOffset, bestLength
}

func mergeYouTubePageConfig(target *youtubePageConfig, source youtubePageConfig) {
	if source.PlayerJSURL != "" {
		target.PlayerJSURL = source.PlayerJSURL
	}
	if source.VisitorData != "" {
		target.VisitorData = source.VisitorData
	}
	if source.LoggedIn != nil {
		loggedIn := *source.LoggedIn
		target.LoggedIn = &loggedIn
	}
	if len(source.PlayerContexts) > 0 {
		if target.PlayerContexts == nil {
			target.PlayerContexts = make(map[string]struct {
				JSURL string `json:"jsUrl"`
			})
		}
		for key, context := range source.PlayerContexts {
			target.PlayerContexts[key] = context
		}
	}
}

func hasYouTubeFormatCandidates(player youtubePlayerResponse) bool {
	if player.StreamingData.HLSManifestURL != "" || player.StreamingData.DASHManifestURL != "" {
		return true
	}
	formats := append(append([]youtubeFormat(nil), player.StreamingData.Formats...), player.StreamingData.AdaptiveFormats...)
	for _, format := range formats {
		if format.URL != "" {
			return true
		}
		if cipher, err := url.ParseQuery(format.SignatureCipher); err == nil && cipher.Get("url") != "" {
			return true
		}
	}
	return false
}

func hasYouTubeSABR(players []youtubePlayerResponse) bool {
	for _, player := range players {
		if player.StreamingData.ServerABRURL != "" {
			return true
		}
	}
	return false
}

func mergeYouTubeFormats(players []youtubePlayerResponse) []youtubeFormat {
	var merged []youtubeFormat
	seen := make(map[string]struct{})
	for _, player := range players {
		formats := append(append([]youtubeFormat(nil), player.StreamingData.Formats...), player.StreamingData.AdaptiveFormats...)
		for _, format := range formats {
			format.clientName = player.clientName
			key := strconv.Itoa(format.Itag) + "\x00" + format.MimeType + "\x00" + format.URL + "\x00" + format.SignatureCipher
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = struct{}{}
			merged = append(merged, format)
		}
	}
	return merged
}

type youtubeVideoDetails struct {
	VideoID          string `json:"videoId"`
	Title            string `json:"title"`
	LengthSeconds    string `json:"lengthSeconds"`
	Author           string `json:"author"`
	ChannelID        string `json:"channelId"`
	ShortDescription string `json:"shortDescription"`
	ViewCount        string `json:"viewCount"`
	IsLive           *bool  `json:"isLive"`
	IsLiveContent    *bool  `json:"isLiveContent"`
	IsUpcoming       *bool  `json:"isUpcoming"`
	IsPostLiveDVR    *bool  `json:"isPostLiveDvr"`
	Thumbnail        struct {
		Thumbnails []struct {
			URL string `json:"url"`
		} `json:"thumbnails"`
	} `json:"thumbnail"`
}

func youtubeLiveStatus(details youtubeVideoDetails) string {
	switch {
	case details.IsPostLiveDVR != nil && *details.IsPostLiveDVR:
		return "post_live"
	case details.IsLive != nil && *details.IsLive:
		return "is_live"
	case details.IsUpcoming != nil && *details.IsUpcoming:
		return "is_upcoming"
	case details.IsLiveContent != nil && *details.IsLiveContent:
		return "was_live"
	case details.IsLive != nil && !*details.IsLive || details.IsLiveContent != nil && !*details.IsLiveContent:
		return "not_live"
	default:
		return ""
	}
}

func firstYouTubeVideoDetails(players []youtubePlayerResponse) youtubeVideoDetails {
	var result youtubeVideoDetails
	for _, player := range players {
		candidate := player.VideoDetails
		if result.VideoID == "" {
			result.VideoID = candidate.VideoID
		}
		if result.Title == "" {
			result.Title = candidate.Title
		}
		if result.LengthSeconds == "" {
			result.LengthSeconds = candidate.LengthSeconds
		}
		if result.Author == "" {
			result.Author = candidate.Author
		}
		if result.ChannelID == "" {
			result.ChannelID = candidate.ChannelID
		}
		if result.ShortDescription == "" {
			result.ShortDescription = candidate.ShortDescription
		}
		if result.ViewCount == "" {
			result.ViewCount = candidate.ViewCount
		}
		if len(result.Thumbnail.Thumbnails) == 0 {
			result.Thumbnail = candidate.Thumbnail
		}
		if result.IsPostLiveDVR == nil && candidate.IsPostLiveDVR != nil {
			value := *candidate.IsPostLiveDVR
			result.IsPostLiveDVR = &value
		}
		if result.IsUpcoming == nil && candidate.IsUpcoming != nil {
			value := *candidate.IsUpcoming
			result.IsUpcoming = &value
		}
		if result.IsLive == nil && candidate.IsLive != nil {
			value := *candidate.IsLive
			result.IsLive = &value
		}
		if result.IsLiveContent == nil && candidate.IsLiveContent != nil {
			value := *candidate.IsLiveContent
			result.IsLiveContent = &value
		}
	}
	return result
}

func youtubeLiveStatusFromPlayers(players []youtubePlayerResponse) string {
	return youtubeLiveStatus(firstYouTubeVideoDetails(players))
}

func firstYouTubeLiveTimestamp(players []youtubePlayerResponse, start bool) (int64, bool) {
	for _, player := range players {
		details := player.Microformat.PlayerMicroformatRenderer.LiveBroadcastDetails
		raw := details.EndTimestamp
		if start {
			raw = details.StartTimestamp
		}
		if timestamp, ok := parseYouTubeLiveTimestamp(raw); ok {
			return timestamp, true
		}
	}
	return 0, false
}

func parseYouTubeLiveTimestamp(raw string) (int64, bool) {
	if raw == "" || len(raw) > 64 || strings.ContainsAny(raw, "\x00\r\n") {
		return 0, false
	}
	parsed, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return 0, false
	}
	return parsed.Unix(), true
}

type youtubePlayabilityStatus struct {
	Status string `json:"status"`
	Reason string `json:"reason"`
}

type youtubeFormat struct {
	Itag              int     `json:"itag"`
	URL               string  `json:"url"`
	SignatureCipher   string  `json:"signatureCipher"`
	MimeType          string  `json:"mimeType"`
	Bitrate           int64   `json:"bitrate"`
	ContentLength     string  `json:"contentLength"`
	Width             int64   `json:"width"`
	Height            int64   `json:"height"`
	FPS               int64   `json:"fps"`
	Language          string  `json:"language"`
	TargetDurationSec float64 `json:"targetDurationSec"`
	clientName        string
}

type pendingYouTubeFormat struct {
	format youtubeFormat
	rawURL string
	sig    string
	sp     string
	n      string
}

func resolveYouTubeURLs(ctx context.Context, request Request, webpageURL, videoID, playerPath string, formats []youtubeFormat) ([]youtubeFormat, error) {
	pending := make([]pendingYouTubeFormat, 0, len(formats))
	var nChallenges, sigChallenges []string
	for _, format := range formats {
		item := pendingYouTubeFormat{format: format, rawURL: format.URL}
		if item.rawURL == "" && format.SignatureCipher != "" {
			cipher, err := url.ParseQuery(format.SignatureCipher)
			if err != nil || cipher.Get("url") == "" {
				continue
			}
			item.rawURL, item.sig, item.sp = cipher.Get("url"), cipher.Get("s"), cipher.Get("sp")
			if item.sp == "" {
				item.sp = "signature"
			}
		}
		parsed, err := url.Parse(item.rawURL)
		if err != nil || parsed.Scheme == "" || parsed.Host == "" {
			continue
		}
		item.n = parsed.Query().Get("n")
		if item.n != "" {
			nChallenges = appendUnique(nChallenges, item.n)
		}
		if item.sig != "" {
			sigChallenges = appendUnique(sigChallenges, item.sig)
		}
		pending = append(pending, item)
	}
	if len(nChallenges) == 0 && len(sigChallenges) == 0 {
		resolved := make([]youtubeFormat, len(pending))
		for index := range pending {
			pending[index].format.URL = pending[index].rawURL
			resolved[index] = pending[index].format
		}
		return resolved, nil
	}
	if request.ChallengeSolver == nil {
		return nil, ErrChallengeSolver
	}
	if playerPath == "" {
		return nil, fmt.Errorf("%w: missing player JavaScript URL", ErrInvalidMetadata)
	}
	playerURL, err := resolveYouTubePlayerURL(webpageURL, playerPath)
	if err != nil {
		return nil, err
	}
	playerSource, _, err := request.Transport.ReadPage(ctx, playerURL)
	if err != nil {
		return nil, err
	}
	challengeRequests := make([]ejs.ChallengeRequest, 0, 2)
	if len(nChallenges) > 0 {
		challengeRequests = append(challengeRequests, ejs.ChallengeRequest{Type: ejs.ChallengeN, Challenges: nChallenges})
	}
	if len(sigChallenges) > 0 {
		challengeRequests = append(challengeRequests, ejs.ChallengeRequest{Type: ejs.ChallengeSig, Challenges: sigChallenges})
	}
	solved, err := request.ChallengeSolver.SolvePlayer(ctx, "youtube-"+videoID, string(playerSource), challengeRequests, false)
	if err != nil {
		// Preserve context cancellation and deadline expiry so callers can
		// observe them with errors.Is; do not recategorize as ErrChallengeSolver.
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return nil, err
		}
		return nil, fmt.Errorf("%w: %v", ErrChallengeSolver, err)
	}
	results := make(map[ejs.ChallengeType]map[string]string, len(solved.Responses))
	for _, response := range solved.Responses {
		if response.Error != "" {
			return nil, fmt.Errorf("%w: %s", ErrChallengeSolver, response.Error)
		}
		results[response.Type] = response.Data
	}
	resolved := make([]youtubeFormat, 0, len(pending))
	for _, item := range pending {
		parsed, _ := url.Parse(item.rawURL)
		query := parsed.Query()
		if item.n != "" {
			answer, ok := results[ejs.ChallengeN][item.n]
			if !ok {
				return nil, fmt.Errorf("%w: missing n result", ErrChallengeSolver)
			}
			query.Set("n", answer)
		}
		if item.sig != "" {
			answer, ok := results[ejs.ChallengeSig][item.sig]
			if !ok {
				return nil, fmt.Errorf("%w: missing signature result", ErrChallengeSolver)
			}
			query.Set(item.sp, answer)
		}
		parsed.RawQuery = query.Encode()
		item.format.URL = parsed.String()
		resolved = append(resolved, item.format)
	}
	return resolved, nil
}

func normalizeYouTubeFormat(format youtubeFormat) (*value.Object, bool) {
	if format.URL == "" {
		return nil, false
	}
	mediaType, parameters, _ := mime.ParseMediaType(format.MimeType)
	extension := youtubeExtension(mediaType, format.URL)
	object := value.NewObject(
		value.Field{Key: "format_id", Value: value.String(strconv.Itoa(format.Itag))},
		value.Field{Key: "url", Value: value.String(format.URL)},
		value.Field{Key: "ext", Value: value.String(extension)},
	)
	codecs := strings.Split(parameters["codecs"], ",")
	for index := range codecs {
		codecs[index] = strings.TrimSpace(codecs[index])
	}
	if strings.HasPrefix(mediaType, "audio/") {
		object.Set("vcodec", value.String("none"))
		if len(codecs) > 0 && codecs[0] != "" {
			object.Set("acodec", value.String(codecs[0]))
		}
	} else if strings.HasPrefix(mediaType, "video/") {
		if len(codecs) > 0 && codecs[0] != "" {
			object.Set("vcodec", value.String(codecs[0]))
		}
		if len(codecs) > 1 {
			object.Set("acodec", value.String(codecs[1]))
		} else {
			object.Set("acodec", value.String("none"))
		}
	}
	if format.Bitrate > 0 {
		object.Set("tbr", value.Float(float64(format.Bitrate)/1000))
	}
	if size, err := strconv.ParseInt(format.ContentLength, 10, 64); err == nil {
		object.Set("filesize", value.Int(size))
	}
	if format.Width > 0 {
		object.Set("width", value.Int(format.Width))
	}
	if format.Height > 0 {
		object.Set("height", value.Int(format.Height))
	}
	if format.FPS > 0 {
		object.Set("fps", value.Int(format.FPS))
	}
	if format.Language != "" {
		object.Set("language", value.String(format.Language))
	}
	return object, true
}

func manifestFormat(id, rawURL, protocolName string) *value.Object {
	return value.NewObject(
		value.Field{Key: "format_id", Value: value.String(id)},
		value.Field{Key: "url", Value: value.String(rawURL)},
		value.Field{Key: "ext", Value: value.String("mp4")},
		value.Field{Key: "protocol", Value: value.String(protocolName)},
	)
}

func youtubeVideoID(rawURL string) (string, error) {
	target, err := parseYouTubeTarget(rawURL)
	return target.videoID, err
}

type youtubeTarget struct {
	videoID            string
	startTime, endTime *float64
	startSet, endSet   bool
}

// youtubeNoCookieEmbedPath is the literal path prefix that privacy-enhanced
// embeds require. The path must be exactly "/embed/" + an 11-character ID;
// trailing slashes, doubled separators, empty segments, and extra components
// are rejected.
const youtubeNoCookieEmbedPath = "/embed/"

func parseYouTubeTarget(rawURL string) (youtubeTarget, error) {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return youtubeTarget{}, fmt.Errorf("%w: invalid YouTube URL", ErrUnsupported)
	}
	// Scheme gate: accept http, https, and protocol-relative (empty scheme).
	// Any other scheme (ftp, file, data, etc.) is rejected at the boundary.
	if parsed.Scheme != "http" && parsed.Scheme != "https" && parsed.Scheme != "" {
		return youtubeTarget{}, fmt.Errorf("%w: unsupported URL scheme", ErrUnsupported)
	}
	// Userinfo and explicit ports are always rejected; they are common
	// hostile vectors and the pinned reference never produces them.
	// strings.Contains(parsed.Host, ":") catches both non-empty ports and
	// the empty-port form "host:" where Port() returns "".
	if parsed.User != nil || strings.Contains(parsed.Host, ":") {
		return youtubeTarget{}, fmt.Errorf("%w: unsupported YouTube URL form", ErrUnsupported)
	}
	// Encoded path separators and NULs: defense in depth. url.Parse does
	// not reject these; EscapedPath is the raw form.
	if rawPath := strings.ToLower(parsed.EscapedPath()); strings.Contains(rawPath, "%2f") || strings.Contains(rawPath, "%5c") || strings.Contains(rawPath, "%00") {
		return youtubeTarget{}, fmt.Errorf("%w: unsupported YouTube URL form", ErrUnsupported)
	}

	host, kind := classifyYouTubeHost(parsed)
	var id string
	switch {
	case kind == hostNoCookie:
		// Privacy-enhanced embeds: accept exactly "/embed/" + 11-char ID.
		if !strings.HasPrefix(parsed.Path, youtubeNoCookieEmbedPath) {
			return youtubeTarget{}, fmt.Errorf("%w: unsupported nocookie path", ErrUnsupported)
		}
		tail := parsed.Path[len(youtubeNoCookieEmbedPath):]
		if len(tail) != 11 {
			return youtubeTarget{}, fmt.Errorf("%w: invalid YouTube video id", ErrUnsupported)
		}
		id = tail
	case kind == hostStandard && host == "youtu.be":
		id = strings.TrimSpace(strings.Trim(parsed.Path, "/"))
	case kind == hostStandard:
		if parsed.Path == "/watch" {
			id = parsed.Query().Get("v")
		} else {
			parts := strings.Split(strings.Trim(parsed.Path, "/"), "/")
			if len(parts) == 2 && (parts[0] == "shorts" || parts[0] == "embed" || parts[0] == "live") {
				id = parts[1]
			}
		}
	default:
		return youtubeTarget{}, fmt.Errorf("%w: unsupported host", ErrUnsupported)
	}
	if !youtubeIDPattern.MatchString(id) {
		return youtubeTarget{}, fmt.Errorf("%w: invalid YouTube video id", ErrUnsupported)
	}
	target := youtubeTarget{videoID: id}
	for _, component := range []string{parsed.Fragment, parsed.RawQuery} {
		for _, pair := range strings.Split(component, "&") {
			key, rawValue, ok := strings.Cut(pair, "=")
			if !ok {
				continue
			}
			key, keyErr := url.QueryUnescape(key)
			rawValue, valueErr := url.QueryUnescape(rawValue)
			if keyErr != nil || valueErr != nil {
				continue
			}
			switch key {
			case "start", "t":
				if !target.startSet {
					target.startSet = true
					if seconds, ok := parseYouTubeOffset(rawValue); ok {
						target.startTime = &seconds
					}
				}
			case "end":
				if !target.endSet {
					target.endSet = true
					if seconds, ok := parseYouTubeOffset(rawValue); ok {
						target.endTime = &seconds
					}
				}
			}
		}
	}
	return target, nil
}

var (
	youtubeOffsetPattern     = regexp.MustCompile(`(?i)^(?:P?(?:[0-9]+\s*y(?:ears?)?,?\s*)?(?:[0-9]+\s*m(?:onths?)?,?\s*)?(?:[0-9]+\s*w(?:eeks?)?,?\s*)?(?:(\d+)\s*d(?:ays?)?,?\s*)?T)?(?:(\d+)\s*h(?:(?:ou)?rs?)?,?\s*)?(?:(\d+)\s*m(?:in(?:ute)?s?)?,?\s*)?(?:(\d+(?:\.\d+)?)\s*s(?:ec(?:ond)?s?)?\s*)?Z?$`)
	youtubeWordOffsetPattern = regexp.MustCompile(`(?i)^(?:([0-9.]+)\s*hours?|([0-9.]+)\s*mins?\.?|([0-9.]+)\s*minutes?)Z?$`)
)

func parseYouTubeOffset(input string) (float64, bool) {
	input = strings.TrimSpace(input)
	if input == "" || len(input) > 64 {
		return 0, false
	}
	if seconds, err := strconv.ParseFloat(input, 64); err == nil {
		return validYouTubeOffset(seconds)
	}
	if strings.Contains(input, ":") {
		return parseYouTubeClockOffset(input)
	}
	match := youtubeOffsetPattern.FindStringSubmatch(input)
	if match != nil && (match[1] != "" || match[2] != "" || match[3] != "" || match[4] != "") {
		var seconds float64
		for index, scale := range []float64{86400, 3600, 60, 1} {
			if match[index+1] == "" {
				continue
			}
			part, err := strconv.ParseFloat(match[index+1], 64)
			if err != nil {
				return 0, false
			}
			seconds += part * scale
		}
		return validYouTubeOffset(seconds)
	}
	word := youtubeWordOffsetPattern.FindStringSubmatch(input)
	if word == nil {
		return 0, false
	}
	for index, scale := range []float64{3600, 60, 60} {
		if word[index+1] != "" {
			part, err := strconv.ParseFloat(word[index+1], 64)
			if err != nil {
				return 0, false
			}
			return validYouTubeOffset(part * scale)
		}
	}
	return 0, false
}

func parseYouTubeClockOffset(input string) (float64, bool) {
	input = strings.TrimSuffix(strings.TrimSuffix(input, "Z"), "z")
	parts := strings.Split(input, ":")
	if len(parts) < 2 || len(parts) > 5 {
		return 0, false
	}
	fraction := 0.0
	if len(parts) >= 2 && len(parts[len(parts)-1]) > 2 && !strings.Contains(parts[len(parts)-1], ".") {
		milliseconds, err := strconv.ParseFloat("0."+parts[len(parts)-1], 64)
		if err != nil {
			return 0, false
		}
		fraction = milliseconds
		parts = parts[:len(parts)-1]
	}
	if len(parts) < 1 || len(parts) > 4 {
		return 0, false
	}
	scales := []float64{86400, 3600, 60, 1}[4-len(parts):]
	var seconds float64
	for index, part := range parts {
		if part == "" {
			return 0, false
		}
		if index == len(parts)-1 && len(parts) > 1 {
			integer := strings.SplitN(part, ".", 2)[0]
			if len(integer) > 2 {
				return 0, false
			}
		}
		value, err := strconv.ParseFloat(part, 64)
		if err != nil || value < 0 {
			return 0, false
		}
		seconds += value * scales[index]
	}
	return validYouTubeOffset(seconds + fraction)
}

func validYouTubeOffset(seconds float64) (float64, bool) {
	if seconds < 0 || seconds > float64(1<<53) || math.IsNaN(seconds) || math.IsInf(seconds, 0) {
		return 0, false
	}
	return seconds, true
}

func setYouTubeOffset(info *value.Object, key string, seconds float64) {
	if seconds == float64(int64(seconds)) {
		info.Set(key, value.Int(int64(seconds)))
		return
	}
	info.Set(key, value.Float(seconds))
}

func resolveYouTubePlayerURL(webpageURL, playerPath string) (string, error) {
	base, err := url.Parse(webpageURL)
	if err != nil {
		return "", fmt.Errorf("%w: invalid webpage URL", ErrInvalidMetadata)
	}
	reference, err := url.Parse(playerPath)
	if err != nil {
		return "", fmt.Errorf("%w: invalid player URL", ErrInvalidMetadata)
	}
	resolved := base.ResolveReference(reference)
	host := strings.ToLower(strings.TrimSuffix(resolved.Hostname(), "."))
	allowedHost := host == "youtube.com" || strings.HasSuffix(host, ".youtube.com") ||
		host == "youtube-nocookie.com" || strings.HasSuffix(host, ".youtube-nocookie.com")
	if resolved.Scheme != "https" || !allowedHost || resolved.Port() != "" || resolved.User != nil || resolved.Fragment != "" ||
		resolved.RawPath != "" || !strings.HasPrefix(resolved.Path, "/s/player/") || path.Clean(resolved.Path) != resolved.Path {
		return "", fmt.Errorf("%w: untrusted player JavaScript URL", ErrInvalidMetadata)
	}
	return resolved.String(), nil
}

func checkYouTubeAvailability(status youtubePlayabilityStatus) error {
	switch status.Status {
	case "OK":
		return nil
	case "LOGIN_REQUIRED":
		return fmt.Errorf("%w: %s", ErrAuthentication, status.Reason)
	default:
		return fmt.Errorf("%w: %s", ErrUnavailable, status.Reason)
	}
}

func extractJSONObject(page []byte, marker string) ([]byte, error) {
	markerIndex := strings.Index(string(page), marker)
	if markerIndex < 0 {
		return nil, fmt.Errorf("marker %q not found", marker)
	}
	raw, _, err := extractJSONObjectFrom(page, markerIndex+len(marker), 0)
	return raw, err
}

func extractJSONObjectFrom(page []byte, offset, maxStartOffset int) ([]byte, int, error) {
	if offset < 0 || offset > len(page) {
		return nil, 0, errors.New("invalid JSON search offset")
	}
	startOffset := bytes.IndexByte(page[offset:], '{')
	if startOffset < 0 {
		return nil, 0, errors.New("JSON object start not found")
	}
	if maxStartOffset > 0 && startOffset > maxStartOffset {
		return nil, 0, errors.New("JSON object start is too far from marker")
	}
	start := offset + startOffset
	depth := 0
	inString, escaped := false, false
	for index := start; index < len(page); index++ {
		character := page[index]
		if inString {
			if escaped {
				escaped = false
			} else if character == '\\' {
				escaped = true
			} else if character == '"' {
				inString = false
			}
			continue
		}
		switch character {
		case '"':
			inString = true
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return page[start : index+1], index + 1, nil
			}
		}
	}
	return nil, 0, errors.New("JSON object is not closed")
}

func appendUnique(items []string, item string) []string {
	for _, existing := range items {
		if existing == item {
			return items
		}
	}
	return append(items, item)
}

func youtubeExtension(mediaType, rawURL string) string {
	switch mediaType {
	case "audio/mp4":
		return "m4a"
	case "video/mp4":
		return "mp4"
	case "audio/webm", "video/webm":
		return "webm"
	}
	parsed, _ := url.Parse(rawURL)
	if extension := strings.TrimPrefix(path.Ext(parsed.Path), "."); extension != "" {
		return extension
	}
	return "bin"
}
