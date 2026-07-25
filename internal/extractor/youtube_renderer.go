package extractor

// Shared YouTube renderer walker for browse and search responses. Route
// extractors supply a bounded policy; this package never hydrates nested
// playlist or channel children.

import (
	"encoding/json"
	"fmt"

	"github.com/ytdlp-go/ytdlp/internal/value"
)

// youtubeRendererKind selects which URL-result families a walk may emit.
type youtubeRendererKind uint32

const (
	youtubeRendererVideo youtubeRendererKind = 1 << iota
	youtubeRendererPlaylist
	youtubeRendererChannel
	youtubeRendererHashtag
	youtubeRendererShelf
	youtubeRendererMusicBrowse
	youtubeRendererCommunity
)

const (
	youtubeRendererTabVideos    = youtubeRendererVideo | youtubeRendererCommunity
	youtubeRendererTabPlaylists = youtubeRendererPlaylist
	youtubeRendererTabMixed     = youtubeRendererVideo | youtubeRendererPlaylist | youtubeRendererCommunity
	youtubeRendererSearchAll    = youtubeRendererVideo | youtubeRendererPlaylist | youtubeRendererChannel |
		youtubeRendererHashtag | youtubeRendererShelf
	youtubeRendererMusicAll = youtubeRendererVideo | youtubeRendererPlaylist | youtubeRendererChannel |
		youtubeRendererMusicBrowse
)

// youtubeRendererPolicy bounds which renderers become entries for a walk.
type youtubeRendererPolicy struct {
	kinds youtubeRendererKind
	// tab, when non-empty, applies the built-in public-tab video/playlist gate
	// before kinds. Custom tabs pass kinds directly and leave tab empty.
	tab string
}

func youtubeRendererPolicyForTab(tab string) youtubeRendererPolicy {
	switch youtubePublicTabType(tab) {
	case youtubeTabVideos:
		return youtubeRendererPolicy{kinds: youtubeRendererTabVideos, tab: tab}
	case youtubeTabPlaylists:
		return youtubeRendererPolicy{kinds: youtubeRendererTabPlaylists, tab: tab}
	case youtubeTabMixed:
		return youtubeRendererPolicy{kinds: youtubeRendererTabMixed, tab: tab}
	default:
		// Custom / search tabs accept the broad playable and navigable set that
		// channel browse responses advertise, excluding arbitrary shelves.
		return youtubeRendererPolicy{kinds: youtubeRendererVideo | youtubeRendererPlaylist | youtubeRendererChannel}
	}
}

func (policy youtubeRendererPolicy) allows(kind youtubeRendererKind) bool {
	if policy.tab != "" {
		switch kind {
		case youtubeRendererVideo:
			return youtubeTabAllowsVideos(policy.tab) && policy.kinds&kind != 0
		case youtubeRendererPlaylist:
			return youtubeTabAllowsPlaylists(policy.tab) && policy.kinds&kind != 0
		case youtubeRendererCommunity:
			return policy.tab == "community" && policy.kinds&kind != 0
		default:
			return false
		}
	}
	return policy.kinds&kind != 0
}

// youtubeRendererPage is the normalized product of one browse/search payload.
type youtubeRendererPage struct {
	entries       []Entry
	continuation  string
	title         string
	channelID     string
	alert         string
	visitorData   string
	tabs          []youtubeAdvertisedTab
	playlistCount int64
	viewCount     int64
	hasCount      bool
	hasViewCount  bool
}

// parseYouTubeRendererData walks a browse or search JSON payload under the
// given policy. Content scoping matches the selected tab / continuation
// containers already used by playlist extraction.
func parseYouTubeRendererData(data []byte, policy youtubeRendererPolicy) (youtubeRendererPage, error) {
	var root value.Value
	if err := json.Unmarshal(data, &root); err != nil {
		return youtubeRendererPage{}, fmt.Errorf("%w: decode YouTube renderer data", ErrInvalidMetadata)
	}
	rootObject, ok := root.Object()
	if !ok {
		return youtubeRendererPage{}, fmt.Errorf("%w: YouTube renderer root", ErrInvalidMetadata)
	}
	var page youtubeRendererPage
	var suppressed map[string]int
	if policy.tab == "community" {
		suppressed = make(map[string]int)
	}
	appendEntry := func(entry Entry, ok bool) {
		if !ok {
			return
		}
		key := youtubeTabEntryKey(entry)
		if suppressed != nil && suppressed[key] > 0 {
			suppressed[key]--
			return
		}
		page.entries = append(page.entries, entry)
	}

	nodes := 0
	err := walkOrderedJSON(youtubePlaylistContentScope(rootObject), 0, &nodes, func(key string, object *value.Object) {
		switch key {
		case "videoRenderer", "gridVideoRenderer", "reelItemRenderer", "playlistVideoRenderer", "playlistPanelVideoRenderer":
			if policy.allows(youtubeRendererVideo) {
				appendEntry(youtubeRendererVideoEntry(object))
			}
		case "shortsLockupViewModel":
			if policy.allows(youtubeRendererVideo) {
				appendEntry(youtubeShortsLockupEntry(object))
			}
		case "playlistRenderer", "gridPlaylistRenderer", "showRenderer", "gridShowRenderer":
			if policy.allows(youtubeRendererPlaylist) {
				appendEntry(youtubeTabPlaylistEntry(object))
			}
		case "channelRenderer", "gridChannelRenderer":
			if policy.allows(youtubeRendererChannel) {
				appendEntry(youtubeRendererChannelEntry(object))
			}
		case "lockupViewModel":
			if entry, ok := youtubeRendererLockupEntry(object, policy); ok {
				appendEntry(entry, true)
			}
		case "hashtagTileRenderer":
			if policy.allows(youtubeRendererHashtag) {
				appendEntry(youtubeHashtagTileEntry(object))
			}
		case "shelfRenderer":
			if policy.allows(youtubeRendererShelf) {
				appendEntry(youtubeShelfEntry(object))
			}
		case "musicResponsiveListItemRenderer", "musicTwoRowItemRenderer":
			if entry, ok := youtubeMusicRendererEntry(object, policy); ok {
				appendEntry(entry, true)
			}
		case "backstagePostRenderer":
			if policy.allows(youtubeRendererCommunity) && policy.tab == "community" {
				for _, entry := range youtubeCommunityPostEntries(object) {
					page.entries = append(page.entries, entry)
				}
				for _, entry := range youtubeCommunityAttachmentEntries(object) {
					suppressed[youtubeTabEntryKey(entry)]++
				}
			}
		}
		if token := youtubeContinuationToken(key, object); token != "" {
			page.continuation = token
		}
	})
	if err != nil {
		return youtubeRendererPage{}, err
	}

	continuationNodes := 0
	if err := walkOrderedJSON(rootObject.Lookup("continuationContents"), 0, &continuationNodes, func(key string, object *value.Object) {
		if token := youtubeContinuationToken(key, object); token != "" {
			page.continuation = token
		}
	}); err != nil {
		return youtubeRendererPage{}, err
	}

	if err := youtubeRendererFillMetadata(rootObject, &page); err != nil {
		return youtubeRendererPage{}, err
	}
	page.tabs = youtubeDiscoverAdvertisedTabs(rootObject, youtubeChannelIdentity{})
	return page, nil
}

// youtubeBindAdvertisedTabs re-discovers channel_tabs under a resolved identity
// so cross-channel advertised endpoints are omitted from playlist metadata.
func youtubeBindAdvertisedTabs(data []byte, identity youtubeChannelIdentity) ([]youtubeAdvertisedTab, error) {
	var root value.Value
	if err := json.Unmarshal(data, &root); err != nil {
		return nil, fmt.Errorf("%w: decode advertised tabs", ErrInvalidMetadata)
	}
	rootObject, ok := root.Object()
	if !ok {
		return nil, fmt.Errorf("%w: advertised tabs root", ErrInvalidMetadata)
	}
	return youtubeDiscoverAdvertisedTabs(rootObject, identity), nil
}
