package extractor

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strings"

	"github.com/ytdlp-go/ytdlp/internal/value"
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
		ID: videoID, Title: title,
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
	// Hashtag pages are not registered. Still validate the endpoint shape so
	// hostile tiles are rejected, but never emit an entry that default playlist
	// expansion would hand to the generic YouTube extractor.
	raw := objectString(renderer, "onTapCommand", "commandMetadata", "webCommandMetadata", "url")
	if raw == "" {
		return Entry{}, false
	}
	if _, ok := youtubeSafeHashtagURL(raw); !ok {
		return Entry{}, false
	}
	return Entry{}, false
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
