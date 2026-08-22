package youtube

// Bounded YouTube Clip (https://www.youtube.com/clip/<id>) extraction.
// Clips are transparent re-entries into the source video's watch extraction;
// the clip identity (clip id, media_type: clip, section_start/section_end)
// is overlaid on the source result so the product layer can derive the clip
// duration and produce a sectioned artifact.

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"github.com/tejasa97/ytdlp-go/internal/value"
)

// youtubeClipIDPattern bounds the clip-id grammar. Clip IDs are not 11-char
// video IDs (e.g. "UgytZKpehg-hEMBSn3F4AaABCQ"); they never pass through the
// youtubeIDPattern validator. The bounded grammar rejects empty, oversized,
// and hostile payloads.
var youtubeClipIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,200}$`)

// youtubeClipSortFields is the extractor-provided https-priority format sort
// used by clips, mirroring the pinned _clip.py _format_sort_fields so ffmpeg
// can consume the delegated URL.
var youtubeClipSortFields = []string{
	"proto:https", "quality", "res", "fps", "hdr:12", "source", "vcodec", "channels", "acodec", "lang",
}

// youtubeMaxClipTimingBytes bounds the length of a timing-value string.
// int64 millisecond values are at most 19 characters (including sign).
const youtubeMaxClipTimingBytes = 24

// youtubeClipID returns the clip id when r is a /clip/<id> URL on a standard
// youtube.com host, and false otherwise. nocookie hosts never serve clip
// pages, so they are rejected here and later by parseYouTubeTarget.
func youtubeClipID(rawURL string) (string, bool) {
	parsed, err := urlParseYouTube(rawURL)
	if err != nil {
		return "", false
	}
	host, kind := classifyYouTubeHost(parsed)
	if kind != hostStandard {
		return "", false
	}
	host = strings.ToLower(host)
	// Accept only www.youtube.com and youtube.com clip paths.
	if host != "www.youtube.com" && host != "youtube.com" && host != "m.youtube.com" {
		return "", false
	}
	parts := strings.Split(strings.Trim(parsed.Path, "/"), "/")
	if len(parts) != 2 || parts[0] != "clip" {
		return "", false
	}
	id := parts[1]
	if !youtubeClipIDPattern.MatchString(id) {
		return "", false
	}
	return id, true
}

// urlParseYouTube parses a YouTube URL without accepting userinfo, ports, or
// encoded separators. It reuses the same security posture as
// validateYouTubeURLPolicy so clip routing cannot be reached with hostile
// URL forms.
func urlParseYouTube(rawURL string) (*url.URL, error) {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return nil, err
	}
	if parsed.User != nil || strings.Contains(parsed.Host, ":") {
		return nil, fmt.Errorf("%w: unsupported URL form", ErrUnsupported)
	}
	rawPath := strings.ToLower(parsed.EscapedPath())
	if strings.Contains(rawPath, "%2f") || strings.Contains(rawPath, "%5c") || strings.Contains(rawPath, "%00") {
		return nil, fmt.Errorf("%w: unsupported URL form", ErrUnsupported)
	}
	return parsed, nil
}

// youtubeClipTiming is the parsed, validated clip section bounds in seconds.
// start is always set; end is nil when the clip is open-ended through the
// loop data.
type youtubeClipTiming struct {
	start   float64
	end     *float64
	startMs int64
	endMs   int64
}

// extractYouTubeClip resolves a /clip/<id> URL to its source video and the
// loop-section bounds, then re-enters the standard YouTube video extractor
// with a transparent URL rewrite and overlays the clip identity on the result
// Info. This mirrors the pinned YoutubeClipIE url_transparent semantics:
// source metadata stays authoritative while id/media_type/section fields are
// overlaid.
func extractYouTubeClip(ctx context.Context, request Request, clipID, webpageURL string) (Extraction, error) {
	page, _, err := request.Transport.ReadPage(ctx, webpageURL)
	if err != nil {
		return Extraction{}, err
	}
	sourceID, timing, err := parseYouTubeClipData(page)
	if err != nil {
		return Extraction{}, err
	}
	if !youtubeIDPattern.MatchString(sourceID) {
		return Extraction{}, fmt.Errorf("%w: Unable to find video ID", ErrInvalidMetadata)
	}
	if err := validateYouTubeClipTiming(timing); err != nil {
		return Extraction{}, err
	}
	request.URL = "https://www.youtube.com/watch?v=" + sourceID
	result, err := NewYouTube().Extract(ctx, request)
	if err != nil {
		return Extraction{}, err
	}
	applyYouTubeClipOverlay(result.Info, clipID, timing, webpageURL)
	return result, nil
}

// parseYouTubeClipData bounded-parses the clip page's ytInitialData for the
// source watch videoId and the loop-section start/end milliseconds. It fails
// closed: a missing videoId returns ErrInvalidMetadata (matching the pinned
// "Unable to find video ID" message), and malformed/absent loop timing
// returns an error rather than silently clipping nothing.
func parseYouTubeClipData(page []byte) (sourceID string, timing youtubeClipTiming, err error) {
	raw, err := extractJSONObject(page, youtubeInitialDataMarker)
	if err != nil {
		return "", youtubeClipTiming{}, fmt.Errorf("%w: clip page has no bounded initial data", ErrInvalidMetadata)
	}
	if len(raw) > youtubeMaxJSONBytes {
		return "", youtubeClipTiming{}, fmt.Errorf("%w: clip initial data exceeds bounds", ErrInvalidMetadata)
	}
	var root value.Value
	if err := json.Unmarshal(raw, &root); err != nil {
		return "", youtubeClipTiming{}, fmt.Errorf("%w: clip initial data is not bounded JSON", ErrInvalidMetadata)
	}
	if !youtubeRootWithinBounds(root) {
		return "", youtubeClipTiming{}, fmt.Errorf("%w: clip initial data exceeds structural bounds", ErrInvalidMetadata)
	}
	rootObject, ok := root.Object()
	if !ok {
		return "", youtubeClipTiming{}, fmt.Errorf("%w: clip initial data root is not an object", ErrInvalidMetadata)
	}
	watch := youtubeWatchChild(value.ObjectValue(rootObject), "currentVideoEndpoint")
	watchEndpoint := youtubeWatchChild(watch, "watchEndpoint")
	sourceID, _ = youtubeWatchChild(watchEndpoint, "videoId").StringValue()
	if !youtubeIDPattern.MatchString(sourceID) {
		return "", youtubeClipTiming{}, fmt.Errorf("%w: Unable to find video ID", ErrInvalidMetadata)
	}
	startMs, endMs, ok := youtubeClipLoopTiming(root)
	if !ok {
		return "", youtubeClipTiming{}, fmt.Errorf("%w: clip loop timing is missing or malformed", ErrInvalidMetadata)
	}
	timing = youtubeClipTiming{
		startMs: startMs, endMs: endMs,
		start: float64(startMs) / 1000,
	}
	end := float64(endMs) / 1000
	timing.end = &end
	return sourceID, timing, nil
}

// youtubeClipLoopTiming traverses only the pinned clip-attribution chain in
// ytInitialData:
//
//	engagementPanels -> ... -> engagementPanelSectionListRenderer -> content ->
//	clipSectionRenderer -> contents -> ... -> clipAttributionRenderer ->
//	onScrubExit -> commandExecutorCommand -> commands -> ... -> openPopupAction ->
//	popup -> notificationActionRenderer -> actionButton -> buttonRenderer ->
//	command -> commandExecutorCommand -> commands -> ... -> loopCommand
//
// and returns the loop-section start/end milliseconds. Unrelated loopCommand
// nodes elsewhere in the raw data are ignored. The traversal is bounded by the
// caller's youtubeRootWithinBounds pass, and each array step is capped by the
// documented structural node budget.
func youtubeClipLoopTiming(root value.Value) (startMs, endMs int64, ok bool) {
	panels, ok := youtubeWatchChild(root, "engagementPanels").ListValue()
	if !ok {
		return 0, 0, false
	}
	for _, panel := range panels {
		renderer := youtubeWatchChild(panel, "engagementPanelSectionListRenderer")
		section := youtubeWatchChild(youtubeWatchChild(renderer, "content"), "clipSectionRenderer")
		contents, ok := youtubeWatchChild(section, "contents").ListValue()
		if !ok {
			continue
		}
		for _, contentItem := range contents {
			attribution := youtubeWatchChild(contentItem, "clipAttributionRenderer")
			onScrubExit := youtubeWatchChild(attribution, "onScrubExit")
			commandExecutor := youtubeWatchChild(onScrubExit, "commandExecutorCommand")
			loop := youtubeClipLoopFromCommands(youtubeWatchChild(commandExecutor, "commands"))
			if loop.IsMissing() {
				continue
			}
			start, hasStart := youtubeClipTimingMs(youtubeWatchChild(loop, "startTimeMs"))
			end, hasEnd := youtubeClipTimingMs(youtubeWatchChild(loop, "endTimeMs"))
			if hasStart && hasEnd && start >= 0 && end >= 0 && end > start {
				return start, end, true
			}
		}
	}
	return 0, 0, false
}

// youtubeClipLoopFromCommands follows the pinned openPopupAction command chain
// from an onScrubExit commands array and returns the first reachable
// loopCommand value the ellipsis traversal would accept.
func youtubeClipLoopFromCommands(commands value.Value) value.Value {
	firstLevel, ok := commands.ListValue()
	if !ok {
		return value.Missing()
	}
	for _, command := range firstLevel {
		state := youtubeWatchChild(command, "openPopupAction")
		state = youtubeWatchChild(state, "popup")
		state = youtubeWatchChild(state, "notificationActionRenderer")
		state = youtubeWatchChild(state, "actionButton")
		state = youtubeWatchChild(state, "buttonRenderer")
		state = youtubeWatchChild(state, "command")
		state = youtubeWatchChild(state, "commandExecutorCommand")
		if items, itemsOK := youtubeWatchChild(state, "commands").ListValue(); itemsOK {
			for _, inner := range items {
				loop := youtubeWatchChild(inner, "loopCommand")
				if !loop.IsMissing() && !loop.IsNull() {
					return loop
				}
			}
		}
	}
	return value.Missing()
}

// youtubeClipTimingMs extracts a millisecond value that is either a JSON
// integer or a bounded integer string, matching the pinned int(...) coercion.
func youtubeClipTimingMs(item value.Value) (int64, bool) {
	if intValue, ok := item.Int(); ok {
		return intValue, true
	}
	text, ok := item.StringValue()
	if !ok {
		return 0, false
	}
	text = strings.TrimSpace(text)
	if text == "" || len(text) > youtubeMaxClipTimingBytes {
		return 0, false
	}
	parsed, err := strconv.ParseInt(text, 10, 64)
	if err != nil {
		return 0, false
	}
	return parsed, true
}

// validateYouTubeClipTiming ensures the clip section bounds are finite,
// nonnegative, and ordered before any output mutation.
func validateYouTubeClipTiming(timing youtubeClipTiming) error {
	if math.IsNaN(timing.start) || math.IsInf(timing.start, 0) || timing.start < 0 || timing.startMs < 0 {
		return fmt.Errorf("%w: invalid clip start", ErrInvalidMetadata)
	}
	if timing.end == nil || math.IsNaN(*timing.end) || math.IsInf(*timing.end, 0) || *timing.end < 0 || timing.endMs < 0 {
		return fmt.Errorf("%w: invalid clip end", ErrInvalidMetadata)
	}
	if *timing.end <= timing.start {
		return fmt.Errorf("%w: clip end must exceed start", ErrInvalidMetadata)
	}
	return nil
}

// applyYouTubeClipOverlay overrides the clip identity on the source Info. The
// pinned id-overlay rule says the clip id wins over the source video id when
// section fields are present; the source title/description/channel/formats
// remain authoritative. _format_sort_fields is set to the https-priority list
// so the product format selector prefers ffmpeg-delegatable URLs.
func applyYouTubeClipOverlay(info value.Info, clipID string, timing youtubeClipTiming, webpageURL string) {
	info.Set("id", value.String(clipID))
	info.Set("media_type", value.String("clip"))
	info.Set("section_start", value.Float(timing.start))
	info.Set("section_end", value.Float(*timing.end))
	info.Set("webpage_url", value.String(webpageURL))
	info.Set("webpage_url_basename", value.String("clip"))
	info.Set("webpage_url_domain", value.String("youtube.com"))
	fields := make([]value.Value, 0, len(youtubeClipSortFields))
	for _, field := range youtubeClipSortFields {
		fields = append(fields, value.String(field))
	}
	info.Set("_format_sort_fields", value.List(fields...))
}
