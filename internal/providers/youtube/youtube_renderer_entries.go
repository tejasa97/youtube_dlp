package youtube

import (
	"encoding/json"
	"fmt"
	"math"
	"net/url"
	"strconv"
	"strings"

	"github.com/tejasa97/ytdlp-go/internal/value"
)

func jsonUnmarshal(data []byte, target *value.Value) error {
	return json.Unmarshal(data, target)
}

func youtubeRendererFillMetadata(root *value.Object, page *youtubeRendererPage) error {
	metadataNodes := 0
	if err := walkOrderedJSON(root.Lookup("metadata"), 0, &metadataNodes, func(key string, object *value.Object) {
		if key == "channelMetadataRenderer" {
			if page.title == "" {
				page.title = objectString(object, "title")
			}
			if page.channelID == "" {
				page.channelID = objectString(object, "externalId")
			}
		}
		if key == "playlistMetadataRenderer" {
			if page.title == "" {
				page.title = objectString(object, "title")
			}
		}
	}); err != nil {
		return err
	}
	headerNodes := 0
	if err := walkOrderedJSON(root.Lookup("header"), 0, &headerNodes, func(key string, object *value.Object) {
		switch key {
		case "c4TabbedHeaderRenderer", "pageHeaderRenderer":
			if page.title == "" {
				page.title = rendererText(object.Lookup("title"))
				if page.title == "" {
					page.title = objectString(object, "pageTitle")
				}
			}
			if page.channelID == "" {
				page.channelID = objectString(object, "channelId")
			}
		case "playlistHeaderRenderer":
			if page.title == "" {
				page.title = rendererText(object.Lookup("title"))
			}
			if !page.hasViewCount {
				if count, ok := youtubeParseCountText(rendererText(object.Lookup("viewCountText"))); ok {
					page.viewCount, page.hasViewCount = count, true
				}
			}
			if !page.hasCount {
				if count, ok := youtubePlaylistHeaderCount(object); ok {
					page.playlistCount, page.hasCount = count, true
				}
			}
		case "hashtagHeaderRenderer":
			if page.title == "" {
				page.title = rendererText(object.Lookup("hashtag"))
			}
		}
	}); err != nil {
		return err
	}
	sidebarNodes := 0
	if err := walkOrderedJSON(root.Lookup("sidebar"), 0, &sidebarNodes, func(key string, object *value.Object) {
		if key != "playlistSidebarPrimaryInfoRenderer" {
			return
		}
		if !page.hasCount || !page.hasViewCount {
			stats, ok := object.Lookup("stats").ListValue()
			if !ok {
				stats, ok = object.Lookup("briefStats").ListValue()
			}
			if ok {
				if !page.hasCount && len(stats) > 0 {
					if count, parsed := youtubeParseCountText(rendererTextValue(stats[0])); parsed {
						page.playlistCount, page.hasCount = count, true
					}
				}
				if !page.hasViewCount && len(stats) > 1 {
					if count, parsed := youtubeParseCountText(rendererTextValue(stats[1])); parsed {
						page.viewCount, page.hasViewCount = count, true
					}
				}
			}
		}
	}); err != nil {
		return err
	}
	alertNodes := 0
	if err := walkOrderedJSON(root.Lookup("alerts"), 0, &alertNodes, func(key string, object *value.Object) {
		if key == "alertRenderer" && page.alert == "" {
			page.alert = rendererText(object.Lookup("text"))
		}
	}); err != nil {
		return err
	}
	page.visitorData = objectString(root, "responseContext", "visitorData")
	return nil
}

func youtubePlaylistHeaderCount(header *value.Object) (int64, bool) {
	bylines, ok := header.Lookup("byline").ListValue()
	if !ok || len(bylines) == 0 {
		return 0, false
	}
	first, ok := bylines[0].Object()
	if !ok {
		return 0, false
	}
	byline, ok := first.Lookup("playlistBylineRenderer").Object()
	if !ok {
		return 0, false
	}
	return youtubeParseCountText(rendererText(byline.Lookup("text")))
}

func rendererTextValue(item value.Value) string {
	if object, ok := item.Object(); ok {
		return rendererText(value.ObjectValue(object))
	}
	if text, ok := item.StringValue(); ok {
		return text
	}
	return ""
}

// youtubeRendererAvailability maps attributable badge styles/labels onto
// yt-dlp-style availability strings through the shared precedence normalizer.
// Unknown badges are ignored. Parser-limit / traversal errors omit availability
// rather than emitting a partial positive claim.
func youtubeRendererAvailability(renderer *value.Object) string {
	badges, ok := renderer.Lookup("badges").ListValue()
	if !ok {
		return ""
	}
	var private, premiumOnly, subscriberOnly, unlisted, public bool
	for _, item := range badges {
		object, ok := item.Object()
		if !ok {
			continue
		}
		nodes := 0
		err := walkOrderedJSON(value.ObjectValue(object), 0, &nodes, func(key string, badge *value.Object) {
			if !strings.HasSuffix(key, "BadgeRenderer") && key != "metadataBadgeRenderer" {
				return
			}
			label := strings.ToLower(strings.TrimSpace(rendererText(badge.Lookup("label"))))
			style := objectString(badge, "style")
			icon := objectString(badge, "icon", "iconType")
			switch {
			case icon == "PRIVACY_PUBLIC" || label == "public":
				public = true
			case icon == "PRIVACY_PRIVATE" || label == "private" || style == "BADGE_STYLE_TYPE_PRIVATE":
				private = true
			case icon == "PRIVACY_UNLISTED" || label == "unlisted":
				unlisted = true
			case style == "BADGE_STYLE_TYPE_PREMIUM" || label == "premium":
				premiumOnly = true
			case style == "BADGE_STYLE_TYPE_MEMBERS_ONLY" || label == "members only" || label == "members-only":
				subscriberOnly = true
			}
		})
		if err != nil {
			return ""
		}
	}
	return youtubeAvailabilityPrecedence(private, premiumOnly, subscriberOnly, false, unlisted, public)
}

const youtubeMaxCountTextBytes = 64

// youtubeParseCountText parses a single attributable view/video count token.
// It accepts plain integers with canonical thousands commas, or one decimal
// with an exact k/m/b/kk suffix, then at most one allowlisted trailing noun
// (views/videos). It rejects malformed commas, arbitrary trailing words, and
// multiplication/decimal overflow. Generic localized count grammars are not
// claimed beyond this allowlist and the exact "no views"/"no videos" phrases.
func youtubeParseCountText(raw string) (int64, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" || len(raw) > youtubeMaxCountTextBytes || strings.ContainsRune(raw, 0) {
		return 0, false
	}
	lower := strings.ToLower(raw)
	if lower == "no views" || lower == "no videos" {
		return 0, true
	}
	runes := []rune(lower)
	start := 0
	for start < len(runes) && (runes[start] < '0' || runes[start] > '9') {
		start++
	}
	if start == len(runes) {
		return 0, false
	}
	// yt-dlp parse_count strips a leading non-digit run only when followed by
	// whitespace before the first digit (`^[^\d]+\s`).
	if start > 0 {
		if runes[start-1] != ' ' && runes[start-1] != '\t' && runes[start-1] != '\u00a0' {
			return 0, false
		}
	}
	i := start
	var groups []string
	current := make([]rune, 0, 3)
	sawComma := false
	sawDot := false
	var fracDigits []rune
	flushGroup := func() bool {
		if len(current) == 0 {
			return false
		}
		groups = append(groups, string(current))
		current = current[:0]
		return true
	}
	for i < len(runes) {
		r := runes[i]
		switch {
		case r >= '0' && r <= '9':
			if sawDot {
				if len(fracDigits) >= 3 {
					return 0, false
				}
				fracDigits = append(fracDigits, r)
			} else {
				if len(current) >= 18 {
					return 0, false
				}
				current = append(current, r)
			}
			i++
		case r == ',':
			if sawDot || len(current) == 0 {
				return 0, false
			}
			sawComma = true
			if !flushGroup() {
				return 0, false
			}
			i++
		case r == '.':
			if sawDot || sawComma || len(current) == 0 {
				return 0, false
			}
			sawDot = true
			if !flushGroup() {
				return 0, false
			}
			i++
		default:
			goto afterNumber
		}
	}
afterNumber:
	if sawDot {
		if len(groups) != 1 || len(fracDigits) == 0 {
			return 0, false
		}
	} else {
		if len(current) == 0 {
			return 0, false
		}
		if !flushGroup() {
			return 0, false
		}
		if sawComma {
			if len(groups) < 2 || len(groups[0]) == 0 || len(groups[0]) > 3 {
				return 0, false
			}
			for _, group := range groups[1:] {
				if len(group) != 3 {
					return 0, false
				}
			}
		} else if len(groups) != 1 {
			return 0, false
		}
	}
	digitCount := 0
	for _, group := range groups {
		digitCount += len(group)
	}
	if digitCount == 0 || digitCount > 18 {
		return 0, false
	}
	sepBeforeToken := 0
	for i < len(runes) && (runes[i] == ' ' || runes[i] == '\t' || runes[i] == '\u00a0') {
		sepBeforeToken++
		i++
	}
	multiplier := int64(1)
	suffixFound := false
	if i < len(runes) {
		switch {
		case i+1 < len(runes) && runes[i] == 'k' && runes[i+1] == 'k':
			multiplier = 1_000_000
			i += 2
			suffixFound = true
		case runes[i] == 'k':
			multiplier = 1_000
			i++
			suffixFound = true
		case runes[i] == 'm':
			multiplier = 1_000_000
			i++
			suffixFound = true
		case runes[i] == 'b':
			multiplier = 1_000_000_000
			i++
			suffixFound = true
		default:
			if sawDot {
				return 0, false
			}
		}
		if suffixFound {
			if i < len(runes) {
				r := runes[i]
				if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
					return 0, false
				}
			}
		}
	} else if sawDot {
		return 0, false
	}
	nounSep := 0
	if suffixFound {
		for i < len(runes) && (runes[i] == ' ' || runes[i] == '\t' || runes[i] == '\u00a0') {
			nounSep++
			i++
		}
	} else {
		nounSep = sepBeforeToken
	}
	if i < len(runes) {
		// Trailing nouns require an actual supported whitespace separator
		// (ASCII space/tab or NBSP). Attached forms like "42videos" fail closed.
		if nounSep == 0 {
			return 0, false
		}
		nounStart := i
		for i < len(runes) && runes[i] >= 'a' && runes[i] <= 'z' {
			i++
		}
		if i != len(runes) {
			return 0, false
		}
		noun := string(runes[nounStart:i])
		if !youtubeCountTrailingNounAllowed(noun) {
			return 0, false
		}
	}
	intDigits := strings.Join(groups, "")
	whole, err := strconv.ParseInt(intDigits, 10, 64)
	if err != nil || whole < 0 {
		return 0, false
	}
	if sawDot {
		frac := string(fracDigits)
		for len(frac) < 3 {
			frac += "0"
		}
		fraction, err := strconv.ParseInt(frac, 10, 64)
		if err != nil {
			return 0, false
		}
		if whole > (math.MaxInt64-fraction)/1000 {
			return 0, false
		}
		base := whole*1000 + fraction
		scale := multiplier / 1000
		if scale <= 0 {
			return 0, false
		}
		if base > math.MaxInt64/scale {
			return 0, false
		}
		return base * scale, true
	}
	if multiplier != 1 {
		if whole > math.MaxInt64/multiplier {
			return 0, false
		}
	}
	return whole * multiplier, true
}

func youtubeCountTrailingNounAllowed(noun string) bool {
	switch noun {
	case "view", "views", "video", "videos":
		return true
	default:
		return false
	}
}

func youtubeRendererVideoEntry(renderer *value.Object) (Entry, bool) {
	videoID := objectString(renderer, "videoId")
	if !youtubeIDPattern.MatchString(videoID) {
		return Entry{}, false
	}
	title := rendererText(renderer.Lookup("title"))
	if title == "" {
		title = rendererText(renderer.Lookup("headline"))
	}
	path := "/watch?v="
	navigation := objectString(renderer, "navigationEndpoint", "commandMetadata", "webCommandMetadata", "url")
	if objectString(renderer, "navigationEndpoint", "reelWatchEndpoint", "videoId") == videoID ||
		objectString(renderer, "videoType") == "SHORT" ||
		strings.Contains(navigation, "/shorts/") ||
		youtubeOverlayStyle(renderer) == "SHORTS" {
		path = "/shorts/"
	}
	if len(title) > youtubeMaxTabEntryTitleBytes || strings.ContainsRune(title, 0) {
		title = ""
	}
	return Entry{
		URL: "https://www.youtube.com" + path + videoID, ExtractorKey: "youtube",
		ID: videoID, Title: title, Availability: youtubeRendererAvailability(renderer),
	}, true
}

func youtubeOverlayStyle(renderer *value.Object) string {
	overlays, ok := renderer.Lookup("thumbnailOverlays").ListValue()
	if !ok {
		return ""
	}
	for _, item := range overlays {
		overlay, ok := item.Object()
		if !ok {
			continue
		}
		if style := objectString(overlay, "thumbnailOverlayTimeStatusRenderer", "style"); style != "" {
			return style
		}
	}
	return ""
}

func youtubeShortsLockupEntry(viewModel *value.Object) (Entry, bool) {
	videoID := objectString(viewModel, "onTap", "innertubeCommand", "reelWatchEndpoint", "videoId")
	if !youtubeIDPattern.MatchString(videoID) {
		return Entry{}, false
	}
	title := objectString(viewModel, "overlayMetadata", "primaryText", "content")
	if len(title) > youtubeMaxTabEntryTitleBytes || strings.ContainsRune(title, 0) {
		title = ""
	}
	return Entry{
		URL: "https://www.youtube.com/shorts/" + videoID, ExtractorKey: "youtube",
		ID: videoID, Title: title,
	}, true
}

func youtubeRendererChannelEntry(renderer *value.Object) (Entry, bool) {
	channelID := objectString(renderer, "channelId")
	if !youtubeChannelIDPattern.MatchString(channelID) {
		return Entry{}, false
	}
	title := rendererText(renderer.Lookup("title"))
	if len(title) > youtubeMaxTabEntryTitleBytes || strings.ContainsRune(title, 0) {
		title = ""
	}
	return Entry{
		URL: "https://www.youtube.com/channel/" + channelID, ExtractorKey: "youtube_channel_tab",
		ID: channelID, Title: title,
	}, true
}

func youtubeRendererLockupEntry(viewModel *value.Object, policy youtubeRendererPolicy) (Entry, bool) {
	contentType := objectString(viewModel, "contentType")
	contentID := objectString(viewModel, "contentId")
	title := objectString(viewModel, "metadata", "lockupMetadataViewModel", "title", "content")
	if len(title) > youtubeMaxTabEntryTitleBytes || strings.ContainsRune(title, 0) {
		title = ""
	}
	switch contentType {
	case "LOCKUP_CONTENT_TYPE_VIDEO":
		if !policy.allows(youtubeRendererVideo) || !youtubeIDPattern.MatchString(contentID) {
			return Entry{}, false
		}
		return Entry{
			URL: "https://www.youtube.com/watch?v=" + contentID, ExtractorKey: "youtube",
			ID: contentID, Title: title,
		}, true
	case "LOCKUP_CONTENT_TYPE_PLAYLIST", "LOCKUP_CONTENT_TYPE_PODCAST":
		if !policy.allows(youtubeRendererPlaylist) || !youtubePlaylistIDPattern.MatchString(contentID) {
			return Entry{}, false
		}
		return youtubeTabPlaylistResult(contentID, title), true
	default:
		return Entry{}, false
	}
}

func youtubeHashtagTileEntry(renderer *value.Object) (Entry, bool) {
	raw := objectString(renderer, "onTapCommand", "commandMetadata", "webCommandMetadata", "url")
	if raw == "" {
		return Entry{}, false
	}
	canonical, ok := youtubeSafeHashtagURL(raw)
	if !ok {
		return Entry{}, false
	}
	title := rendererText(renderer.Lookup("hashtag"))
	if len(title) > youtubeMaxTabEntryTitleBytes || strings.ContainsRune(title, 0) {
		title = ""
	}
	tag := strings.TrimPrefix(canonical.Path, "/hashtag/")
	return Entry{
		URL: canonical.String(), ExtractorKey: "youtube_hashtag",
		ID: tag, Title: title,
	}, true
}

func youtubeSafeHashtagURL(raw string) (*url.URL, bool) {
	reference, err := url.Parse(raw)
	if err != nil {
		return nil, false
	}
	resolved := (&url.URL{Scheme: "https", Host: "www.youtube.com"}).ResolveReference(reference)
	if err := youtubeAssertExactWebHost(resolved); err != nil {
		return nil, false
	}
	if resolved.RawPath != "" || resolved.Fragment != "" || resolved.RawQuery != "" {
		return nil, false
	}
	parts := strings.Split(resolved.Path, "/")
	if len(parts) != 3 || parts[1] != "hashtag" || parts[2] == "" ||
		strings.ContainsAny(parts[2], `/\.%`) || len(parts[2]) > 100 {
		return nil, false
	}
	return &url.URL{Scheme: "https", Host: "www.youtube.com", Path: "/hashtag/" + parts[2]}, true
}

func youtubeShelfEntry(renderer *value.Object) (Entry, bool) {
	raw := objectString(renderer, "endpoint", "commandMetadata", "webCommandMetadata", "url")
	if raw == "" {
		return Entry{}, false
	}
	reference, err := url.Parse(raw)
	if err != nil {
		return Entry{}, false
	}
	resolved := (&url.URL{Scheme: "https", Host: "www.youtube.com"}).ResolveReference(reference)
	if err := youtubeAssertExactWebHost(resolved); err != nil {
		return Entry{}, false
	}
	if resolved.User != nil || resolved.Fragment != "" || resolved.RawPath != "" {
		return Entry{}, false
	}
	lowQuery := strings.ToLower(resolved.RawQuery)
	if strings.Contains(lowQuery, "%2f") || strings.Contains(lowQuery, "%5c") || strings.Contains(lowQuery, "%00") {
		return Entry{}, false
	}
	// Skip channel-switcher shelves; they are not attributable media results.
	if strings.Contains(resolved.Path, "/channels") {
		return Entry{}, false
	}
	title := rendererText(renderer.Lookup("title"))
	if len(title) > youtubeMaxTabEntryTitleBytes || strings.ContainsRune(title, 0) {
		title = ""
	}
	canonical := &url.URL{Scheme: "https", Host: "www.youtube.com", Path: resolved.Path, RawQuery: resolved.RawQuery}
	return Entry{URL: canonical.String(), Title: title}, true
}

func youtubeMusicRendererEntry(renderer *value.Object, policy youtubeRendererPolicy) (Entry, bool) {
	if videoID := objectString(renderer, "playlistItemData", "videoId"); youtubeIDPattern.MatchString(videoID) {
		if !policy.allows(youtubeRendererVideo) {
			return Entry{}, false
		}
		title := musicFlexTitle(renderer)
		if title == "" {
			title = rendererText(renderer.Lookup("title"))
		}
		if len(title) > youtubeMaxTabEntryTitleBytes || strings.ContainsRune(title, 0) {
			title = ""
		}
		return Entry{
			URL: "https://www.youtube.com/watch?v=" + videoID, ExtractorKey: "youtube",
			ID: videoID, Title: title,
		}, true
	}
	if videoID := objectString(renderer, "navigationEndpoint", "watchEndpoint", "videoId"); youtubeIDPattern.MatchString(videoID) {
		if playlistID := objectString(renderer, "navigationEndpoint", "watchEndpoint", "playlistId"); youtubePlaylistIDPattern.MatchString(playlistID) {
			if policy.allows(youtubeRendererPlaylist) {
				title := rendererText(renderer.Lookup("title"))
				if title == "" {
					title = musicFlexTitle(renderer)
				}
				return youtubeTabPlaylistResult(playlistID, title), true
			}
		}
		if policy.allows(youtubeRendererVideo) {
			title := rendererText(renderer.Lookup("title"))
			if title == "" {
				title = musicFlexTitle(renderer)
			}
			if len(title) > youtubeMaxTabEntryTitleBytes || strings.ContainsRune(title, 0) {
				title = ""
			}
			return Entry{
				URL: "https://www.youtube.com/watch?v=" + videoID, ExtractorKey: "youtube",
				ID: videoID, Title: title,
			}, true
		}
	}
	if playlistID := objectString(renderer, "navigationEndpoint", "watchEndpoint", "playlistId"); youtubePlaylistIDPattern.MatchString(playlistID) {
		if policy.allows(youtubeRendererPlaylist) {
			title := rendererText(renderer.Lookup("title"))
			if title == "" {
				title = musicFlexTitle(renderer)
			}
			return youtubeTabPlaylistResult(playlistID, title), true
		}
	}
	browseID := objectString(renderer, "navigationEndpoint", "browseEndpoint", "browseId")
	if browseID == "" {
		return Entry{}, false
	}
	title := rendererText(renderer.Lookup("title"))
	if title == "" {
		title = musicFlexTitle(renderer)
	}
	if len(title) > youtubeMaxTabEntryTitleBytes || strings.ContainsRune(title, 0) {
		title = ""
	}
	if youtubeChannelIDPattern.MatchString(browseID) {
		if !policy.allows(youtubeRendererChannel) {
			return Entry{}, false
		}
		return Entry{
			URL: "https://www.youtube.com/channel/" + browseID, ExtractorKey: "youtube_channel_tab",
			ID: browseID, Title: title,
		}, true
	}
	if strings.HasPrefix(browseID, "VL") && youtubePlaylistIDPattern.MatchString(browseID[2:]) {
		if !policy.allows(youtubeRendererPlaylist) {
			return Entry{}, false
		}
		return youtubeTabPlaylistResult(browseID[2:], title), true
	}
	// Emit only registered Music browse families that youtube_music_browse
	// consumes. Unregistered prefixes stay omitted so default playlist
	// expansion cannot select the generic YouTube extractor.
	if policy.allows(youtubeRendererMusicBrowse) {
		if _, ok := youtubeMusicBrowseFamily(browseID); ok {
			return youtubeMusicBrowseResult(browseID, title), true
		}
	}
	return Entry{}, false
}

func musicFlexTitle(o *value.Object) string {
	columns, _ := o.Lookup("flexColumns").ListValue()
	if len(columns) == 0 {
		return ""
	}
	c, ok := columns[0].Object()
	if !ok {
		return ""
	}
	flex, ok := c.Lookup("musicResponsiveListItemFlexColumnRenderer").Object()
	if !ok {
		return ""
	}
	return rendererText(flex.Lookup("text"))
}

func validYouTubeMusicBrowseID(id string) bool {
	if id == "" || len(id) > 128 || strings.ContainsAny(id, "\x00\r\n/\\?#") {
		return false
	}
	for _, r := range id {
		if (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '_' || r == '-' {
			continue
		}
		return false
	}
	return true
}

func youtubeAssertExactWebHost(parsed *url.URL) error {
	if parsed == nil || parsed.User != nil || parsed.Scheme != "https" {
		return fmt.Errorf("%w: hostile YouTube URL", ErrInvalidMetadata)
	}
	host := strings.ToLower(strings.TrimSuffix(parsed.Hostname(), "."))
	if host != "www.youtube.com" && host != "youtube.com" {
		return fmt.Errorf("%w: hostile YouTube host", ErrInvalidMetadata)
	}
	if parsed.Port() != "" {
		return fmt.Errorf("%w: hostile YouTube port", ErrInvalidMetadata)
	}
	return nil
}
