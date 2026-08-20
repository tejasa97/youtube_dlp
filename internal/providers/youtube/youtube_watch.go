package youtube

// Bounded watch-page (ytInitialData) metadata extraction. All parsers here are
// pure and fail closed: over-budget, malformed, or unattributable data omits
// the affected field rather than failing extraction or emitting a partial
// positive claim.

import (
	"encoding/json"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/tejasa97/ytdlp-go/internal/value"
)

const (
	youtubeMaxWatchChapters       = 256
	youtubeMaxWatchHeatmapEntries = 1024
	youtubeMaxWatchTextBytes      = 2048
)

var (
	youtubeSuperTitleEpisodePattern = regexp.MustCompile(`(?i)(.+?)\s*S(\d+)\s*•?\s*E(\d+)`)
	youtubeLikeLabelPattern         = regexp.MustCompile(`(?i)([\d,]+)\s*(dis)?like`)
	youtubeLikeAlongPattern         = regexp.MustCompile(`(?i)(dis)?like this video along with ([\d,]+) other people`)
	youtubeWatchTimePattern         = regexp.MustCompile(`(?i)^(?:premiered\s+|streamed\s+|started\s+)?([A-Z][a-z]{2}\.?\s+\d{1,2},\s+\d{4})`)
)

// youtubeWatchChild returns the child value at key, or Missing.
func youtubeWatchChild(parent value.Value, key string) value.Value {
	if object, ok := parent.Object(); ok {
		return object.Lookup(key)
	}
	return value.Missing()
}

// youtubeWatchMetadata is the normalized product of one watch-page
// ytInitialData parse. Fields stay zero/empty when unattributable.
type youtubeWatchMetadata struct {
	chapters                []value.Value
	heatmap                 []value.Value
	likeCount               int64
	dislikeCount            int64
	hasLikeCount            bool
	hasDislikeCount         bool
	commentCount            int64
	hasCommentCount         bool
	channelFollowerCount    int64
	hasChannelFollowerCount bool
	channelIsVerified       bool
	hasChannelIsVerified    bool
	concurrentViewCount     int64
	hasConcurrentViewCount  bool
	series                  string
	seasonNumber            int64
	episodeNumber           int64
	location                string
	uploadDateFallback      string
	availability            string
	hasAvailability         bool
}

// extractYouTubeWatchMetadata parses the watch page's ytInitialData once
// through a bounded root walk before delegating to parseRoot. The byte
// cap and structural limits mirror the pinned yt-dlp reference
// (yt_dlp/extractor/youtube/_video.py:2293-2308) and the existing
// walkOrderedJSON bounds used for badge subtrees; exceeding any of them
// causes the entire watch-page metadata to be omitted rather than
// producing a partial claim.
func extractYouTubeWatchMetadata(page []byte, duration int64, hasDuration bool) (youtubeWatchMetadata, error) {
	raw, err := extractJSONObject(page, youtubeInitialDataMarker)
	if err != nil {
		return youtubeWatchMetadata{}, nil // watch metadata is optional
	}
	if len(raw) > youtubeMaxJSONBytes {
		return youtubeWatchMetadata{}, nil
	}
	var root value.Value
	if err := json.Unmarshal(raw, &root); err != nil {
		return youtubeWatchMetadata{}, nil
	}
	if !youtubeRootWithinBounds(root) {
		return youtubeWatchMetadata{}, nil
	}
	rootObject, ok := root.Object()
	if !ok {
		return youtubeWatchMetadata{}, nil
	}
	var metadata youtubeWatchMetadata
	metadata.parseRoot(rootObject, duration, hasDuration)
	return metadata, nil
}

// youtubeRootWithinBounds enforces the advertised structural limits on
// the entire ytInitialData payload. The walker counts every visited
// node, tracks depth, and aborts on the first violation. Returns true
// only when the entire tree stays within budget.
func youtubeRootWithinBounds(root value.Value) bool {
	nodes := 0
	var walk func(item value.Value, depth int) bool
	walk = func(item value.Value, depth int) bool {
		nodes++
		if depth > youtubeMaxJSONDepth || nodes > youtubeMaxJSONNodes {
			return false
		}
		if object, ok := item.Object(); ok {
			for _, field := range object.Fields() {
				if !walk(field.Value, depth+1) {
					return false
				}
			}
			return true
		}
		if list, ok := item.ListValue(); ok {
			for _, child := range list {
				if !walk(child, depth+1) {
					return false
				}
			}
		}
		return true
	}
	return walk(root, 0)
}

func (metadata *youtubeWatchMetadata) parseRoot(root *value.Object, duration int64, hasDuration bool) {
	twoColumn := youtubeWatchChild(root.Lookup("contents"), "twoColumnWatchNextResults")
	results := youtubeWatchChild(youtubeWatchChild(youtubeWatchChild(twoColumn, "results"), "results"), "contents")
	contents, _ := results.ListValue()
	var vpir, vsir *value.Object
	var commentsEntry *value.Object
	for _, item := range contents {
		object, ok := item.Object()
		if !ok {
			continue
		}
		if candidate, ok := object.Lookup("videoPrimaryInfoRenderer").Object(); ok && vpir == nil {
			vpir = candidate
		}
		if candidate, ok := object.Lookup("videoSecondaryInfoRenderer").Object(); ok && vsir == nil {
			vsir = candidate
		}
		if section, ok := object.Lookup("itemSectionRenderer").Object(); ok && commentsEntry == nil {
			if entry := youtubeCommentsEntryPoint(section); entry != nil {
				commentsEntry = entry
			}
		}
	}
	if vpir != nil {
		metadata.parsePrimaryInfo(vpir)
	}
	if vsir != nil {
		metadata.parseSecondaryInfo(vsir)
	}
	if commentsEntry != nil {
		metadata.parseCommentsEntry(commentsEntry)
	}
	metadata.parsePlayerOverlays(root.Lookup("playerOverlays"), duration, hasDuration)
	metadata.parseEngagementPanels(root.Lookup("engagementPanels"), duration, hasDuration)
	metadata.parseFrameworkUpdates(root.Lookup("frameworkUpdates"))
}

func youtubeCommentsEntryPoint(section *value.Object) *value.Object {
	items, ok := section.Lookup("contents").ListValue()
	if !ok {
		return nil
	}
	for _, item := range items {
		object, ok := item.Object()
		if !ok {
			continue
		}
		if entry, ok := object.Lookup("commentsEntryPointHeaderRenderer").Object(); ok {
			return entry
		}
	}
	return nil
}

// youtubeWatchText extracts a bounded simpleText/runs text from a renderer.
func youtubeWatchText(object *value.Object, key string) string {
	candidate := object.Lookup(key)
	if text, ok := candidate.StringValue(); ok && text != "" {
		return youtubeWatchBoundText(text)
	}
	if text, ok := youtubeWatchChild(candidate, "simpleText").StringValue(); ok && text != "" {
		return youtubeWatchBoundText(text)
	}
	if runs, ok := youtubeWatchChild(candidate, "runs").ListValue(); ok {
		var builder strings.Builder
		for _, run := range runs {
			runObject, ok := run.Object()
			if !ok {
				continue
			}
			if text, ok := runObject.Lookup("text").StringValue(); ok {
				builder.WriteString(text)
			}
			if builder.Len() > youtubeMaxWatchTextBytes {
				break
			}
		}
		return youtubeWatchBoundText(builder.String())
	}
	return ""
}

func youtubeWatchBoundText(raw string) string {
	if raw == "" || len(raw) > youtubeMaxWatchTextBytes || youtubeHasControlChars(raw) {
		return ""
	}
	return raw
}

func (metadata *youtubeWatchMetadata) parsePrimaryInfo(vpir *value.Object) {
	if dateText := youtubeWatchText(vpir, "dateText"); dateText != "" {
		metadata.uploadDateFallback = youtubeWatchUploadDate(dateText)
	}
	if superTitle := vpir.Lookup("superTitleLink"); !superTitle.IsMissing() {
		metadata.parseSuperTitle(superTitle)
	}
	metadata.parseLikeButtons(youtubeWatchChild(youtubeWatchChild(vpir.Lookup("videoActions"), "menuRenderer"), "topLevelButtons"))
	if viewCount := youtubeWatchChild(vpir.Lookup("viewCount"), "videoViewCountRenderer"); !viewCount.IsMissing() {
		if isLive, ok := youtubeWatchChild(viewCount, "isLive").Bool(); ok && isLive {
			viewCountObject, _ := viewCount.Object()
			if count, ok := youtubeParseCountText(youtubeWatchText(viewCountObject, "viewCount")); ok {
				metadata.concurrentViewCount = count
				metadata.hasConcurrentViewCount = true
			}
		}
	}
	metadata.parseAvailabilityBadges(vpir.Lookup("badges"))
}

func (metadata *youtubeWatchMetadata) parseSuperTitle(superTitle value.Value) {
	object, ok := superTitle.Object()
	if !ok {
		return
	}
	text := youtubeWatchText(object, "text")
	if text == "" {
		return
	}
	if iconType, ok := youtubeWatchChild(object.Lookup("superTitleIcon"), "iconType").StringValue(); ok && iconType == "LOCATION_PIN" {
		metadata.location = youtubeWatchBoundText(text)
		return
	}
	if match := youtubeSuperTitleEpisodePattern.FindStringSubmatch(text); match != nil {
		metadata.series = youtubeWatchBoundText(match[1])
		if season, err := strconv.ParseInt(match[2], 10, 64); err == nil {
			metadata.seasonNumber = season
		}
		if episode, err := strconv.ParseInt(match[3], 10, 64); err == nil {
			metadata.episodeNumber = episode
		}
	}
}

func (metadata *youtubeWatchMetadata) parseLikeButtons(buttons value.Value) {
	list, ok := buttons.ListValue()
	if !ok {
		return
	}
	for _, button := range list {
		object, ok := button.Object()
		if !ok {
			continue
		}
		if metadata.parseLikeToggle(object.Lookup("toggleButtonRenderer")) {
			continue
		}
		if segmented, ok := object.Lookup("segmentedLikeDislikeButtonRenderer").Object(); ok {
			metadata.parseLikeToggle(youtubeWatchChild(segmented.Lookup("likeButton"), "toggleButtonRenderer"))
			metadata.parseLikeToggle(youtubeWatchChild(segmented.Lookup("dislikeButton"), "toggleButtonRenderer"))
			continue
		}
		if viewModel := object.Lookup("segmentedLikeDislikeButtonViewModel"); !viewModel.IsMissing() {
			like := youtubeWatchChild(viewModel, "likeButtonViewModel")
			like = youtubeWatchChild(like, "likeButtonViewModel")
			like = youtubeWatchChild(like, "toggleButtonViewModel")
			like = youtubeWatchChild(like, "toggleButtonViewModel")
			like = youtubeWatchChild(like, "defaultButtonViewModel")
			like = youtubeWatchChild(like, "buttonViewModel")
			like = youtubeWatchChild(like, "accessibilityText")
			likeObject, _ := like.Object()
			if count, ok := youtubeParseCountText(youtubeWatchText(likeObject, "text")); ok {
				metadata.likeCount = count
				metadata.hasLikeCount = true
			}
		}
	}
}

func (metadata *youtubeWatchMetadata) parseLikeToggle(toggle value.Value) bool {
	object, ok := toggle.Object()
	if !ok {
		return false
	}
	label := ""
	if labelCandidate, ok := youtubeWatchChild(youtubeWatchChild(object.Lookup("defaultText"), "accessibility"), "accessibilityData").Object(); ok {
		label = youtubeWatchText(labelCandidate, "label")
	}
	if label == "" {
		if accessibility, ok := object.Lookup("accessibility").Object(); ok {
			label = youtubeWatchText(accessibility, "label")
		}
	}
	if label == "" {
		return false
	}
	if match := youtubeLikeLabelPattern.FindStringSubmatch(label); match != nil {
		if count, ok := youtubeParseCountText(match[1]); ok {
			if strings.EqualFold(match[2], "dis") {
				metadata.dislikeCount = count
				metadata.hasDislikeCount = true
			} else {
				metadata.likeCount = count
				metadata.hasLikeCount = true
			}
			return true
		}
	}
	if match := youtubeLikeAlongPattern.FindStringSubmatch(label); match != nil {
		if count, ok := youtubeParseCountText(match[2]); ok {
			if strings.EqualFold(match[1], "dis") {
				metadata.dislikeCount = count
				metadata.hasDislikeCount = true
			} else {
				metadata.likeCount = count
				metadata.hasLikeCount = true
			}
			return true
		}
	}
	return false
}

func (metadata *youtubeWatchMetadata) parseSecondaryInfo(vsir *value.Object) {
	owner := youtubeWatchChild(vsir.Lookup("owner"), "videoOwnerRenderer")
	if owner.IsMissing() {
		return
	}
	ownerObject, ok := owner.Object()
	if !ok {
		return
	}
	if subscribers := youtubeWatchText(ownerObject, "subscriberCountText"); subscribers != "" {
		if count, ok := youtubeParseSubscriberCount(subscribers); ok {
			metadata.channelFollowerCount = count
			metadata.hasChannelFollowerCount = true
		}
	}
	if !metadata.hasChannelFollowerCount {
		if count, ok := youtubeFirstCollaboratorFollowerCount(ownerObject); ok {
			metadata.channelFollowerCount = count
			metadata.hasChannelFollowerCount = true
		}
	}
	if badges, ok := ownerObject.Lookup("badges").ListValue(); ok {
		for _, badge := range badges {
			badgeObject, ok := badge.Object()
			if !ok {
				continue
			}
			nodes := 0
			err := walkOrderedJSON(value.ObjectValue(badgeObject), 0, &nodes, func(key string, candidate *value.Object) {
				if !strings.HasSuffix(key, "BadgeRenderer") && key != "metadataBadgeRenderer" {
					return
				}
				if youtubeBadgeType(candidate) == youtubeBadgeVerified {
					metadata.channelIsVerified = true
					metadata.hasChannelIsVerified = true
				}
			})
			if err != nil {
				metadata.hasChannelIsVerified = false
			}
		}
	}
}

// youtubeFirstCollaboratorFollowerCount follows YouTube's bounded collaborator
// dialog path and reads only the first collaborator, matching current yt-dlp.
// The ordinary owner subscriberCountText remains authoritative when present.
func youtubeFirstCollaboratorFollowerCount(owner *value.Object) (int64, bool) {
	runs, ok := youtubeWatchChild(owner.Lookup("attributedTitle"), "commandRuns").ListValue()
	if !ok {
		return 0, false
	}
	for _, run := range runs {
		runObject, ok := run.Object()
		if !ok {
			continue
		}
		items := runObject.Lookup("onTap")
		for _, key := range []string{
			"innertubeCommand", "showDialogCommand", "panelLoadingStrategy", "inlineContent",
			"dialogViewModel", "customContent", "listViewModel", "listItems",
		} {
			items = youtubeWatchChild(items, key)
		}
		list, ok := items.ListValue()
		if !ok {
			continue
		}
		for _, item := range list {
			itemObject, ok := item.Object()
			if !ok {
				continue
			}
			viewModel, ok := itemObject.Lookup("listItemViewModel").Object()
			if !ok {
				continue
			}
			context := youtubeWatchChild(youtubeWatchChild(viewModel.Lookup("rendererContext"), "accessibilityContext"), "label")
			label, ok := context.StringValue()
			if !ok {
				return 0, false
			}
			return youtubeCollaboratorSubscriberLabel(label)
		}
	}
	return 0, false
}

func youtubeCollaboratorSubscriberLabel(label string) (int64, bool) {
	label = youtubeWatchBoundText(strings.TrimSpace(label))
	if label == "" {
		return 0, false
	}
	if count, ok := youtubeParseSubscriberCount(label); ok {
		return count, true
	}
	// yt-dlp's parse_count permits a non-numeric collaborator name followed by
	// whitespace. Strip only that shape; a digit in the name or a missing
	// separator remains ambiguous and fails the subscriber grammar below.
	start := strings.IndexAny(label, "0123456789")
	if start <= 0 {
		return 0, false
	}
	prefix := label[:start]
	if !strings.HasSuffix(prefix, " ") && !strings.HasSuffix(prefix, "\t") && !strings.HasSuffix(prefix, "\u00a0") {
		return 0, false
	}
	return youtubeParseSubscriberCount(label[start:])
}

func (metadata *youtubeWatchMetadata) parseCommentsEntry(entry *value.Object) {
	if count := youtubeWatchText(entry, "commentCount"); count != "" {
		if parsed, ok := youtubeParseCountText(count); ok {
			metadata.commentCount = parsed
			metadata.hasCommentCount = true
		}
	}
}

// youtubeParseSubscriberCount parses a bounded subscriber count text such as
// "12.3K subscribers" or "1,234 subscribers". The count grammar reuses
// youtubeParseCountText after stripping the allowlisted subscriber noun.
var youtubeSubscriberCountPattern = regexp.MustCompile(`(?i)^([\d.,]+[kKmMbB]?)\s*(subscriber)s?$`)

func youtubeParseSubscriberCount(raw string) (int64, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" || len(raw) > 64 || youtubeHasControlChars(raw) {
		return 0, false
	}
	match := youtubeSubscriberCountPattern.FindStringSubmatch(raw)
	if match == nil {
		return 0, false
	}
	return youtubeParseCountText(match[1])
}

// youtubeWatchUploadDate converts a bounded absolute dateText ("Jan 15, 2024")
// into YYYYMMDD. Relative texts ("3 years ago") are not attributable and are
// skipped, matching the Go port's bounded scope.
func youtubeWatchUploadDate(dateText string) string {
	match := youtubeWatchTimePattern.FindStringSubmatch(dateText)
	if match == nil {
		return ""
	}
	return youtubeParseWatchDate(strings.Replace(match[1], ".", "", 1))
}

func (metadata *youtubeWatchMetadata) parseAvailabilityBadges(badges value.Value) {
	var private, premiumOnly, subscriberOnly, unlisted, public bool
	list, ok := badges.ListValue()
	if !ok {
		return
	}
	for _, badge := range list {
		object, ok := badge.Object()
		if !ok {
			continue
		}
		nodes := 0
		err := walkOrderedJSON(value.ObjectValue(object), 0, &nodes, func(key string, candidate *value.Object) {
			if !strings.HasSuffix(key, "BadgeRenderer") && key != "metadataBadgeRenderer" {
				return
			}
			label := strings.ToLower(strings.TrimSpace(rendererText(candidate.Lookup("label"))))
			style := objectString(candidate, "style")
			icon := objectString(candidate, "icon", "iconType")
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
			// Structural limits must not yield a partial positive claim.
			metadata.hasAvailability = false
			return
		}
	}
	metadata.availability = youtubeAvailabilityPrecedence(private, premiumOnly, subscriberOnly, false, unlisted, public)
	metadata.hasAvailability = metadata.availability != ""
}

// youtubeBadgeKind classifies attributable renderer badges.
type youtubeBadgeKind int

const (
	youtubeBadgeUnknown youtubeBadgeKind = iota
	youtubeBadgeVerified
)

// youtubeBadgeType maps a badge renderer onto its attributable kind. Only the
// pinned "Verified" label is claimed.
func youtubeBadgeType(object *value.Object) youtubeBadgeKind {
	label := strings.ToLower(strings.TrimSpace(rendererTextValue(object.Lookup("label"))))
	if label == "verified" {
		return youtubeBadgeVerified
	}
	return youtubeBadgeUnknown
}

// youtubeAvailabilityPrecedence maps attributable availability signals onto the
// pinned yt-dlp availability string with order-independent precedence:
// private > premium_only > subscriber_only > needs_auth > unlisted > public.
//
// Pin: yt_dlp/extractor/common.py:4010-4021. The "public" branch is the
// caller's all-known inference; when any signal is unknown, "public" must
// not be claimed and the function returns "" (treated as None upstream).
func youtubeAvailabilityPrecedence(private, premiumOnly, subscriberOnly, needsAuth, unlisted, public bool) string {
	switch {
	case private:
		return "private"
	case premiumOnly:
		return "premium_only"
	case subscriberOnly:
		return "subscriber_only"
	case needsAuth:
		return "needs_auth"
	case unlisted:
		return "unlisted"
	case public:
		return "public"
	default:
		return ""
	}
}

// parsePlayerOverlays extracts structured chapters from the player overlay
// decorated player bar (chapters source 1).
func (metadata *youtubeWatchMetadata) parsePlayerOverlays(overlays value.Value, duration int64, hasDuration bool) {
	bar := youtubeWatchChild(overlays, "playerOverlayRenderer")
	bar = youtubeWatchChild(bar, "decoratedPlayerBarRenderer")
	bar = youtubeWatchChild(bar, "decoratedPlayerBarRenderer")
	bar = youtubeWatchChild(bar, "playerBar")
	bar = youtubeWatchChild(bar, "chapteredPlayerBarRenderer")
	chapters, ok := youtubeWatchChild(bar, "chapters").ListValue()
	if !ok || len(chapters) == 0 {
		return
	}
	items := make([]youtubeChapterItem, 0, len(chapters))
	for _, chapter := range chapters {
		renderer, ok := youtubeWatchChild(chapter, "chapterRenderer").Object()
		if !ok {
			continue
		}
		start, ok := renderer.Lookup("timeRangeStartMillis").Int()
		if !ok || start < 0 {
			continue
		}
		title := youtubeWatchText(renderer, "title")
		if title == "" {
			continue
		}
		items = append(items, youtubeChapterItem{start: float64(start) / 1000, title: title})
	}
	metadata.chapters = normalizeYouTubeChapters(items, duration, hasDuration)
}

// parseEngagementPanels extracts macro-marker chapters (source 2) and the
// engagement-panel comment count.
func (metadata *youtubeWatchMetadata) parseEngagementPanels(panels value.Value, duration int64, hasDuration bool) {
	list, ok := panels.ListValue()
	if !ok {
		return
	}
	for _, panel := range list {
		section, ok := youtubeWatchChild(panel, "engagementPanelSectionListRenderer").Object()
		if !ok {
			continue
		}
		panelID, _ := section.Lookup("panelIdentifier").StringValue()
		switch {
		case panelID == "comment-item-section" || panelID == "engagement-panel-comments-section":
			if !metadata.hasCommentCount {
				contextual := youtubeWatchChild(youtubeWatchChild(section.Lookup("header"), "engagementPanelTitleHeaderRenderer"), "contextualInfo")
				contextualObject, _ := contextual.Object()
				if count := youtubeWatchText(contextualObject, "text"); count != "" {
					if parsed, ok := youtubeParseCountText(count); ok {
						metadata.commentCount = parsed
						metadata.hasCommentCount = true
					}
				}
			}
		case len(metadata.chapters) == 0:
			content := youtubeWatchChild(youtubeWatchChild(section.Lookup("content"), "macroMarkersListRenderer"), "contents")
			items := metadata.chapterItemsFromMacroMarkers(content)
			if len(items) > 0 {
				metadata.chapters = normalizeYouTubeChapters(items, duration, hasDuration)
			}
		}
	}
}

func (metadata *youtubeWatchMetadata) chapterItemsFromMacroMarkers(content value.Value) []youtubeChapterItem {
	list, ok := content.ListValue()
	if !ok {
		return nil
	}
	items := make([]youtubeChapterItem, 0, len(list))
	for _, item := range list {
		renderer, ok := youtubeWatchChild(item, "macroMarkersListItemRenderer").Object()
		if !ok {
			continue
		}
		title := youtubeWatchText(renderer, "title")
		timeText := youtubeWatchText(renderer, "timeDescription")
		if title == "" || timeText == "" {
			continue
		}
		start, ok := youtubeParseWatchDuration(timeText)
		if !ok {
			continue
		}
		items = append(items, youtubeChapterItem{start: start, title: title})
	}
	return items
}

// youtubeChapterItem is one raw chapter candidate before normalization.
type youtubeChapterItem struct {
	start float64
	title string
}

// youtubeChaptersFromDescription is the bounded description-based chapter
// fallback (source 3). It accepts four pinned orders, tried in priority:
//
//	A. <timestamp> <title>     (e.g. "0:00 Intro")
//	B. <title> <timestamp>     (e.g. "Intro 0:00")
//	C. <title>\n<timestamp>    (e.g. "Intro\n0:00")
//	D. <timestamp>\n<title>    (e.g. "0:00\nIntro")
//
// Pin: yt_dlp/extractor/youtube/_video.py:2350-2353. yt-dlp consumes
// the description line by line and pairs each title with the timestamp
// that immediately follows or precedes it on the same physical line,
// never double-counting a timestamp that has already anchored one
// chapter. This implementation walks the description sequentially,
// consuming each timestamp at most once across the four orders.
//
// Crucially, orders C and D only apply when the timestamp line is bare
// (no same-line title); when the same line carries both a timestamp and
// a title, only orders A and B are considered, so preceding or
// following prose never gets mis-attributed as a chapter title.
var (
	youtubeDescriptionChapterTimestampPattern = regexp.MustCompile(`^\s*((?:\d{1,2}:)?\d{1,2}:\d{2})\s*$`)
)

func youtubeHasDescriptionControlChars(raw string) bool {
	for _, r := range raw {
		if (r < 0x20 && r != '\n' && r != '\r' && r != '\t') || r == 0x7f {
			return true
		}
	}
	return false
}

// youtubeIsTimestampLine reports whether the trimmed line is exactly a
// timestamp (no surrounding text). Used to gate orders C and D so that
// preceding/following prose never anchors a chapter.
func youtubeIsTimestampLine(trimmed string) bool {
	return youtubeDescriptionChapterTimestampPattern.MatchString(trimmed)
}

// youtubeChapterTitleFromSameLine extracts a title candidate from a line
// that already has a timestamp prefix or suffix, returning the title
// text and a boolean indicating whether a non-timestamp portion exists.
// The line is split at the timestamp boundary; either side may carry
// the title, or neither.
func youtubeChapterTitleFromSameLine(line, timestamp string) (string, bool) {
	if timestamp == "" {
		return "", false
	}
	index := strings.Index(line, timestamp)
	if index < 0 {
		return "", false
	}
	before := strings.TrimSpace(line[:index])
	after := strings.TrimSpace(line[index+len(timestamp):])
	if before != "" && youtubeIsLikelyChapterTitle(before) {
		return before, true
	}
	if after != "" && youtubeIsLikelyChapterTitle(after) {
		return after, true
	}
	return "", false
}

// youtubeIsLikelyChapterTitle accepts titles within the bounded length
// window that contain at least one Unicode letter so a pure numeric or
// pure punctuation line (e.g. "1." or "...") is not misclassified as a
// chapter. The letter check uses unicode.IsLetter so valid Japanese,
// Cyrillic, Arabic, CJK, and other non-Latin titles are accepted,
// matching the pinned yt_dlp/extractor/youtube/_video.py:2350-2353
// parser which imposes no script restriction.
func youtubeIsLikelyChapterTitle(candidate string) bool {
	candidate = strings.TrimSpace(candidate)
	if len(candidate) < 2 || len(candidate) > 120 {
		return false
	}
	for _, r := range candidate {
		if unicode.IsLetter(r) {
			return true
		}
	}
	return false
}

func youtubeChaptersFromDescription(description string, duration int64, hasDuration bool) []value.Value {
	if description == "" || len(description) > 1<<20 || youtubeHasDescriptionControlChars(description) {
		return nil
	}
	lines := strings.Split(description, "\n")
	items := make([]youtubeChapterItem, 0, 8)
	consumed := make([]bool, len(lines))
	for index, line := range lines {
		if consumed[index] {
			continue
		}
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		// Find any timestamp in the line. yt-dlp accepts timestamps with
		// optional whitespace inside the line and recognizes either
		// "<timestamp> <title>" or "<title> <timestamp>" orders.
		timestampText, ok := youtubeExtractTimestampFromLine(trimmed)
		if !ok {
			continue
		}
		start, ok := youtubeParseWatchDuration(timestampText)
		if !ok {
			continue
		}
		// Order A/B: same-line timestamp + title (the line is not bare).
		if title, hasTitle := youtubeChapterTitleFromSameLine(trimmed, timestampText); hasTitle {
			items = append(items, youtubeChapterItem{start: start, title: title})
			consumed[index] = true
			continue
		}
		// Orders C/D only apply when the timestamp line is bare, so
		// preceding/following prose never anchors a chapter.
		if !youtubeIsTimestampLine(trimmed) {
			continue
		}
		// Order C: title is on the previous non-empty line.
		for back := index - 1; back >= 0; back-- {
			if consumed[back] {
				break
			}
			candidate := strings.TrimSpace(lines[back])
			if candidate == "" {
				continue
			}
			if !youtubeIsLikelyChapterTitle(candidate) {
				break
			}
			items = append(items, youtubeChapterItem{start: start, title: candidate})
			consumed[back] = true
			consumed[index] = true
			break
		}
		if consumed[index] {
			continue
		}
		// Order D: title is on the next non-empty line.
		for forward := index + 1; forward < len(lines); forward++ {
			if consumed[forward] {
				continue
			}
			candidate := strings.TrimSpace(lines[forward])
			if candidate == "" {
				continue
			}
			if !youtubeIsLikelyChapterTitle(candidate) {
				break
			}
			items = append(items, youtubeChapterItem{start: start, title: candidate})
			consumed[index] = true
			consumed[forward] = true
			break
		}
	}
	return normalizeYouTubeChapters(items, duration, hasDuration)
}

// youtubeExtractTimestampFromLine returns the timestamp substring of a
// trimmed line that contains exactly one timestamp, or false if the
// line has no timestamp, multiple timestamps, or contains a timestamp
// only inside other text. The split whitespace-aware because yt-dlp
// accepts whitespace between the timestamp and the surrounding text.
func youtubeExtractTimestampFromLine(trimmed string) (string, bool) {
	parts := strings.Fields(trimmed)
	if len(parts) == 0 {
		return "", false
	}
	// Order A: <timestamp> ... <title>
	if _, ok := youtubeParseWatchDuration(parts[0]); ok {
		return parts[0], true
	}
	// Order B: <title> ... <timestamp>
	if last := parts[len(parts)-1]; last != parts[0] {
		if _, ok := youtubeParseWatchDuration(last); ok {
			return last, true
		}
	}
	return "", false
}

// normalizeYouTubeChapters sorts candidates by start time, bounds them to the
// video duration, computes end times from the following start (or duration),
// and drops empty or inverted entries. Without a duration, end times stay
// absent. The result is capped at youtubeMaxWatchChapters.
func normalizeYouTubeChapters(items []youtubeChapterItem, duration int64, hasDuration bool) []value.Value {
	if len(items) == 0 {
		return nil
	}
	sort.SliceStable(items, func(i, j int) bool { return items[i].start < items[j].start })
	if len(items) > youtubeMaxWatchChapters {
		items = items[:youtubeMaxWatchChapters]
	}
	chapters := make([]value.Value, 0, len(items))
	for index := range items {
		item := items[index]
		chapterEnd := 0.0
		if hasDuration {
			chapterEnd = float64(duration)
			if item.start < 0 || item.start >= chapterEnd {
				continue
			}
			if index+1 < len(items) {
				next := items[index+1].start
				if next > item.start && next < chapterEnd {
					chapterEnd = next
				}
			}
			if chapterEnd <= item.start {
				continue
			}
		} else if item.start < 0 {
			continue
		}
		fields := []value.Field{
			{Key: "start_time", Value: value.Float(item.start)},
		}
		if hasDuration {
			fields = append(fields, value.Field{Key: "end_time", Value: value.Float(chapterEnd)})
		}
		fields = append(fields, value.Field{Key: "title", Value: value.String(item.title)})
		chapters = append(chapters, value.ObjectValue(value.NewObject(fields...)))
	}
	return chapters
}

// parseFrameworkUpdates extracts the heatmap from entity batch mutations.
// The cap is enforced globally: when the running entry count reaches
// youtubeMaxWatchHeatmapEntries, both the inner marker loop and the outer
// mutation loop are short-circuited so a second mutation cannot append a
// 1025th entry.
func (metadata *youtubeWatchMetadata) parseFrameworkUpdates(updates value.Value) {
	mutations, ok := youtubeWatchChild(youtubeWatchChild(updates, "entityBatchUpdate"), "mutations").ListValue()
	if !ok {
		return
	}
	entries := make([]value.Value, 0, youtubeMaxWatchHeatmapEntries)
	for _, mutation := range mutations {
		payload, ok := youtubeWatchChild(mutation, "payload").Object()
		if !ok {
			continue
		}
		markersList := youtubeWatchChild(
			youtubeWatchChild(value.ObjectValue(payload), "macroMarkersListEntity"),
			"markersList",
		)
		markerType, _ := youtubeWatchChild(markersList, "markerType").StringValue()
		if markerType != "MARKER_TYPE_HEATMAP" {
			continue
		}
		markers, ok := youtubeWatchChild(markersList, "markers").ListValue()
		if !ok {
			continue
		}
		for _, marker := range markers {
			if len(entries) >= youtubeMaxWatchHeatmapEntries {
				metadata.heatmap = entries
				return
			}
			object, ok := marker.Object()
			if !ok {
				continue
			}
			startMS, startOK := object.Lookup("startMillis").Int()
			durationMS, durationOK := object.Lookup("durationMillis").Int()
			score, scoreOK := object.Lookup("intensityScoreNormalized").Float()
			if !startOK || !durationOK || !scoreOK || startMS < 0 || durationMS < 0 {
				continue
			}
			if score < 0 || score > 1 {
				continue
			}
			entries = append(entries, value.ObjectValue(value.NewObject(
				value.Field{Key: "start_time", Value: value.Float(float64(startMS) / 1000)},
				value.Field{Key: "end_time", Value: value.Float(float64(startMS+durationMS) / 1000)},
				value.Field{Key: "value", Value: value.Float(score)},
			)))
		}
	}
	metadata.heatmap = entries
}

// youtubeParseWatchDuration parses a bounded mm:ss or h:mm:ss chapter time.
func youtubeParseWatchDuration(raw string) (float64, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" || len(raw) > 32 || youtubeHasControlChars(raw) {
		return 0, false
	}
	parts := strings.Split(raw, ":")
	if len(parts) < 2 || len(parts) > 3 {
		return 0, false
	}
	var total float64
	for _, part := range parts {
		value, err := strconv.ParseFloat(part, 64)
		if err != nil || value < 0 {
			return 0, false
		}
		total = total*60 + value
	}
	if total < 0 || total > 24*60*60 {
		return 0, false
	}
	return total, true
}

// youtubeParseWatchDate parses a bounded "Mon D, YYYY" watch-page date into
// YYYYMMDD.
func youtubeParseWatchDate(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" || len(raw) > 32 || youtubeHasControlChars(raw) {
		return ""
	}
	parsed, err := time.Parse("Jan 2, 2006", raw)
	if err != nil {
		return ""
	}
	return parsed.Format("20060102")
}
