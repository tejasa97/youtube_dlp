package extractor

// Player-response metadata normalization for single YouTube videos. All
// helpers here are pure, bounded, and fail closed: malformed or over-budget
// service input omits the affected field rather than failing extraction or
// emitting a partial positive claim.

import (
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/tejasa97/youtube_dlp/internal/value"
)

const (
	youtubeMaxUploadDateBytes   = 64
	youtubeMaxOwnerProfileBytes = 256
	youtubeMaxCategoryBytes     = 256
	youtubeMaxTagBytes          = 1024
	youtubeMaxTags              = 64
	youtubeMaxThumbnailBytes    = 2048
	youtubeMaxThumbnails        = 64
	youtubeMaxOGImageScanBytes  = 1 << 19 // og:image lives in <head>; bound the scan
	youtubeMaxStretchKeyword    = 1024
)

var (
	youtubeUploadDateOnlyPattern = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)
	youtubeStretchRatioPattern   = regexp.MustCompile(`(\d+)\s*:\s*(\d+)`)
	// Attribute-order variants are separate RE2 patterns (no lookahead).
	youtubeOGImagePattern = regexp.MustCompile(
		`(?i)<meta[^>]*\b(?:property|name)=["'](og:image|twitter:image)["'][^>]*\bcontent=["']([^"']+)["'][^>]*>`)
	youtubeOGImageContentFirstPattern = regexp.MustCompile(
		`(?i)<meta[^>]*\bcontent=["']([^"']+)["'][^>]*\b(?:property|name)=["'](og:image|twitter:image)["'][^>]*>`)
)

// youtubeThumbnailNames is the deterministic i.ytimg.com ladder, best first.
// The order and preference formula mirror the pinned reference exactly.
var youtubeThumbnailNames = []string{
	"maxresdefault", "hq720", "sddefault", "hqdefault", "0", "mqdefault", "default",
	"sd1", "sd2", "sd3", "hq1", "hq2", "hq3", "mq1", "mq2", "mq3", "1", "2", "3",
}

func youtubeHasControlChars(raw string) bool {
	for _, r := range raw {
		if r < 0x20 || r == 0x7f {
			return true
		}
	}
	return false
}

// youtubeUploadDate normalizes the microformat uploadDate. A timezone-bearing
// RFC3339 value yields both the UTC upload_date and an attributable Unix
// timestamp; a bare YYYY-MM-DD yields upload_date only (time and timezone are
// not attributable, matching the pinned reference's timezone=NO_DEFAULT rule);
// anything else is omitted.
func youtubeUploadDate(raw string) (uploadDate string, timestamp int64, hasTimestamp, ok bool) {
	if raw == "" || len(raw) > youtubeMaxUploadDateBytes || youtubeHasControlChars(raw) {
		return "", 0, false, false
	}
	if parsed, err := time.Parse(time.RFC3339, raw); err == nil {
		return parsed.UTC().Format("20060102"), parsed.Unix(), true, true
	}
	if parsed, err := time.Parse("2006-01-02", raw); err == nil {
		return parsed.Format("20060102"), 0, false, true
	}
	return "", 0, false, false
}

// youtubeOwnerHandle extracts a validated @handle from the microformat
// ownerProfileUrl. Unlike the pinned reference's prefix match, the path must
// be exactly "/@handle": query strings, fragments, encoded paths (RawPath),
// ports, userinfo, and non-YouTube hosts fail closed. The handle grammar is
// the shared Unicode-aware @[\w.-]{3,30} rule (validYouTubeHandle).
func youtubeOwnerHandle(ownerProfileURL string) string {
	if ownerProfileURL == "" || len(ownerProfileURL) > youtubeMaxOwnerProfileBytes || youtubeHasControlChars(ownerProfileURL) {
		return ""
	}
	parsed, err := url.Parse(ownerProfileURL)
	if err != nil || parsed.User != nil || strings.Contains(parsed.Host, ":") {
		return ""
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return ""
	}
	host := strings.ToLower(strings.TrimSuffix(parsed.Hostname(), "."))
	if host != "youtube.com" && host != "www.youtube.com" {
		return ""
	}
	if parsed.RawQuery != "" || parsed.Fragment != "" || (parsed.RawPath != "" && parsed.RawPath != parsed.Path) || !strings.HasPrefix(parsed.Path, "/") {
		return ""
	}
	handle := strings.TrimPrefix(parsed.Path, "/")
	if handle == "" || handle == parsed.Path || !validYouTubeHandle(handle) {
		return ""
	}
	return handle
}

// youtubeChannelURL builds the canonical channel URL only for channel IDs that
// satisfy the exact public UCID grammar (UC + 22 URL-safe characters).
func youtubeChannelURL(channelID string) string {
	if !youtubeChannelIDPattern.MatchString(channelID) {
		return ""
	}
	return "https://www.youtube.com/channel/" + channelID
}

// youtubeTags filters and caps videoDetails keywords. Oversized, empty, and
// control-character-bearing tags are dropped; at most youtubeMaxTags remain.
func youtubeTags(keywords []string) []string {
	tags := make([]string, 0, len(keywords))
	for _, keyword := range keywords {
		if len(tags) >= youtubeMaxTags {
			break
		}
		if keyword == "" || len(keyword) > youtubeMaxTagBytes || youtubeHasControlChars(keyword) {
			continue
		}
		tags = append(tags, keyword)
	}
	if len(tags) == 0 {
		return nil
	}
	return tags
}

func youtubeCategory(category string) string {
	if category == "" || len(category) > youtubeMaxCategoryBytes || youtubeHasControlChars(category) {
		return ""
	}
	return category
}

// youtubeAgeLimit mirrors the pinned reference's player-response rule: 18 when
// isFamilySafe is explicitly false, otherwise 0. The pinned
// og:restrictions:age fallback is deferred and documented.
func youtubeAgeLimit(isFamilySafe *bool) int {
	if isFamilySafe != nil && !*isFamilySafe {
		return 18
	}
	return 0
}

// youtubePlayerAvailability derives the partial availability claim available
// from player data alone: private, needs_auth, or unlisted. Public, premium,
// and subscriber_only states need watch-page badge data and remain absent
// until watch-page enrichment lands.
func youtubePlayerAvailability(isPrivate *bool, isUnlisted *bool, ageLimit int) string {
	switch {
	case isPrivate != nil && *isPrivate:
		return "private"
	case ageLimit >= 18:
		return "needs_auth"
	case isUnlisted != nil && *isUnlisted:
		return "unlisted"
	default:
		return ""
	}
}

// youtubeMergedAvailability combines the watch-page badge availability with
// player-derived signals through the shared precedence normalizer. The
// badge claim is empty when unattributable, so the merged result degrades
// to the player-only partial claim.
//
// Pin: yt_dlp/extractor/common.py:4010-4021 _availability. The public
// inference requires both is_private and is_unlisted to be known-false
// (non-nil and not true), matching the pinned yt_dlp/extractor/youtube/
// _video.py:4555-4571 construction that supplies is_unlisted=None only
// when is_private is None and otherwise carries the watched-page
// unlisted signal. The state name "premium_only" matches the pinned
// yt_dlp/extractor/common.py:4016 emission.
func youtubeMergedAvailability(badgeAvailability string, isPrivate *bool, isUnlisted *bool, ageLimit int) string {
	private := badgeAvailability == "private"
	premiumOnly := badgeAvailability == "premium_only"
	subscriberOnly := badgeAvailability == "subscriber_only"
	unlisted := badgeAvailability == "unlisted"
	public := badgeAvailability == "public"
	if isPrivate != nil && *isPrivate {
		private = true
	}
	if isUnlisted != nil && *isUnlisted {
		unlisted = true
	}
	// Pinned all-known public inference: when is_private and is_unlisted
	// are both known-false and no signal elevated the video, the merged
	// claim is "public". Unknown signals leave the merged claim as the
	// badge-only partial.
	if isPrivate != nil && !*isPrivate && isUnlisted != nil && !*isUnlisted &&
		!private && !premiumOnly && !subscriberOnly && !unlisted && badgeAvailability == "" {
		public = true
	}
	return youtubeAvailabilityPrecedence(private, premiumOnly, subscriberOnly, ageLimit >= 18, unlisted, public)
}

// youtubeMediaType classifies the video from player data, matching the pinned
// reference's livestream > short > video precedence.
func youtubeMediaType(isLiveContent, isShortsEligible *bool) string {
	switch {
	case isLiveContent != nil && *isLiveContent:
		return "livestream"
	case isShortsEligible != nil && *isShortsEligible:
		return "short"
	default:
		return "video"
	}
}

func youtubeThumbnailURL(raw string) string {
	if raw == "" || len(raw) > youtubeMaxThumbnailBytes || youtubeHasControlChars(raw) {
		return ""
	}
	parsed, err := url.Parse(raw)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return ""
	}
	return raw
}

// youtubeUnescapeHTMLEntities unescapes the named entities that can appear in
// service-supplied meta content. Numeric entities are intentionally not
// claimed.
func youtubeUnescapeHTMLEntities(raw string) string {
	replacer := strings.NewReplacer("&amp;", "&", "&lt;", "<", "&gt;", ">", "&quot;", `"`, "&#39;", "'")
	return replacer.Replace(raw)
}

// youtubeOGImage returns an attributable og:image (or twitter:image) URL from
// the watch page <head>, or "" when absent or hostile. og:image wins over
// twitter:image regardless of document order, matching the pinned reference's
// ordered search_meta semantics. The scan is bounded to the first
// youtubeMaxOGImageScanBytes of the page.
func youtubeOGImage(page []byte) string {
	if len(page) > youtubeMaxOGImageScanBytes {
		page = page[:youtubeMaxOGImageScanBytes]
	}
	if raw := youtubeOGImageNamed(page, "og:image"); raw != "" {
		return raw
	}
	return youtubeOGImageNamed(page, "twitter:image")
}

func youtubeOGImageNamed(page []byte, name string) string {
	for _, match := range youtubeOGImagePattern.FindAllSubmatch(page, -1) {
		if string(match[1]) == name {
			return youtubeOGImageCandidate(string(match[2]))
		}
	}
	for _, match := range youtubeOGImageContentFirstPattern.FindAllSubmatch(page, -1) {
		if string(match[2]) == name {
			return youtubeOGImageCandidate(string(match[1]))
		}
	}
	return ""
}

func youtubeOGImageCandidate(raw string) string {
	raw = youtubeUnescapeHTMLEntities(raw)
	if raw == "" || len(raw) > youtubeMaxThumbnailBytes || youtubeHasControlChars(raw) {
		return ""
	}
	parsed, err := url.Parse(raw)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return ""
	}
	return raw
}

// youtubeMaxOriginalThumbnails reserves the full 38-entry generated ladder:
// originals are capped separately so the deterministic ladder is always
// complete, while youtubeMaxThumbnails remains the overall resource bound.
var youtubeMaxOriginalThumbnails = youtubeMaxThumbnails - 2*len(youtubeThumbnailNames)

// youtubeThumbnailCollection builds the normalized thumbnails list: original
// player and microformat thumbnails, an attributable og:image, then the
// deterministic i.ytimg.com JPG/WebP ladder with stable preferences. Earlier
// URLs win on duplicates; hostile or over-budget URLs are omitted. The best
// original is selected by sorting a copy of the originals with the pinned
// _sort_thumbnails ordering (preference, width, height, id, url ascending) so
// an og:image never wins merely because it was appended last.
func youtubeThumbnailCollection(page []byte, videoID string, details youtubeVideoDetails, microformat youtubePlayerMicroformat, liveStatus string) (thumbnails []value.Value, best string) {
	seen := make(map[string]struct{})
	var originals []value.Value
	appendOriginal := func(raw string, width, height int64) {
		if len(originals) >= youtubeMaxOriginalThumbnails {
			return
		}
		if raw = youtubeThumbnailURL(raw); raw == "" {
			return
		}
		if _, ok := seen[raw]; ok {
			return
		}
		seen[raw] = struct{}{}
		fields := []value.Field{{Key: "url", Value: value.String(raw)}}
		if width > 0 {
			fields = append(fields, value.Field{Key: "width", Value: value.Int(width)})
		}
		if height > 0 {
			fields = append(fields, value.Field{Key: "height", Value: value.Int(height)})
		}
		originals = append(originals, value.ObjectValue(value.NewObject(fields...)))
	}
	for _, thumb := range details.Thumbnail.Thumbnails {
		appendOriginal(thumb.URL, thumb.Width, thumb.Height)
	}
	for _, thumb := range microformat.Thumbnail.Thumbnails {
		appendOriginal(thumb.URL, thumb.Width, thumb.Height)
	}
	if ogImage := youtubeOGImage(page); ogImage != "" {
		appendOriginal(ogImage, 0, 0)
	}
	if len(originals) > 0 {
		sorted := make([]value.Value, len(originals))
		copy(sorted, originals)
		sort.SliceStable(sorted, func(i, j int) bool {
			return youtubeThumbnailLess(sorted[i], sorted[j])
		})
		object, _ := sorted[len(sorted)-1].Object()
		best, _ = object.Lookup("url").StringValue()
	}
	thumbnails = append(thumbnails, originals...)
	live := ""
	if liveStatus == "is_live" {
		live = "_live"
	}
	for index, name := range youtubeThumbnailNames {
		for _, extension := range []string{"webp", "jpg"} {
			if len(thumbnails) >= youtubeMaxThumbnails {
				return thumbnails, best
			}
			webp := ""
			preference := int64(-1) - 2*int64(index)
			if extension == "webp" {
				webp = "_webp"
				preference++
			}
			raw := fmt.Sprintf("https://i.ytimg.com/vi%s/%s/%s%s.%s", webp, videoID, name, live, extension)
			if _, ok := seen[raw]; ok {
				continue
			}
			seen[raw] = struct{}{}
			thumbnails = append(thumbnails, value.ObjectValue(value.NewObject(
				value.Field{Key: "url", Value: value.String(raw)},
				value.Field{Key: "preference", Value: value.Int(preference)},
			)))
		}
	}
	return thumbnails, best
}

// youtubeThumbnailLess is the pinned _sort_thumbnails ascending ordering:
// preference (default -1), width (default 0), height (default 0), id
// (default ""), then url (default "").
func youtubeThumbnailLess(left, right value.Value) bool {
	leftObject, leftOK := left.Object()
	rightObject, rightOK := right.Object()
	if !leftOK || !rightOK {
		return !leftOK && rightOK
	}
	preference := func(object *value.Object) int64 {
		if number, ok := object.Lookup("preference").Int(); ok {
			return number
		}
		return -1
	}
	dimension := func(object *value.Object, key string) int64 {
		if number, ok := object.Lookup(key).Int(); ok {
			return number
		}
		return 0
	}
	text := func(object *value.Object, key string) string {
		value, ok := object.Lookup(key).StringValue()
		if !ok {
			return ""
		}
		return value
	}
	leftValues := []any{preference(leftObject), dimension(leftObject, "width"), dimension(leftObject, "height"), text(leftObject, "id"), text(leftObject, "url")}
	rightValues := []any{preference(rightObject), dimension(rightObject, "width"), dimension(rightObject, "height"), text(rightObject, "id"), text(rightObject, "url")}
	for index := range leftValues {
		switch left := leftValues[index].(type) {
		case int64:
			right := rightValues[index].(int64)
			if left != right {
				return left < right
			}
		case string:
			right := rightValues[index].(string)
			if left != right {
				return left < right
			}
		}
	}
	return false
}

// youtubeStretchedRatio derives the first valid yt:stretch=W:H aspect ratio
// from the video keywords, mirroring the pinned reference's search semantics
// (unanchored, first match wins, both dimensions positive).
func youtubeStretchedRatio(keywords []string) (float64, bool) {
	for _, keyword := range keywords {
		if len(keyword) > youtubeMaxStretchKeyword || !strings.HasPrefix(keyword, "yt:stretch=") {
			continue
		}
		match := youtubeStretchRatioPattern.FindStringSubmatch(keyword)
		if match == nil {
			continue
		}
		width, widthErr := strconv.ParseInt(match[1], 10, 64)
		height, heightErr := strconv.ParseInt(match[2], 10, 64)
		if widthErr != nil || heightErr != nil || width <= 0 || height <= 0 {
			continue
		}
		return float64(width) / float64(height), true
	}
	return 0, false
}

// applyYouTubeStretchedRatio annotates every format whose vcodec is not "none"
// (including manifest formats without a vcodec field), matching the pinned
// reference's per-format application.
func applyYouTubeStretchedRatio(formats []value.Value, ratio float64) {
	for _, format := range formats {
		object, ok := format.Object()
		if !ok {
			continue
		}
		if vcodec, present := object.Lookup("vcodec").StringValue(); !present || vcodec != "none" {
			object.Set("stretched_ratio", value.Float(ratio))
		}
	}
}
