package extractor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/ytdlp-go/ytdlp/internal/value"
)

const youtubeMaxBareChannelTabs = 128

type youtubeBareChannelSpec struct {
	canonical, videosURL, fallbackID, subject string
	categorize                                func(error) error
	extractTab                                func(context.Context, Transport, string) (Extraction, error)
}

// extractYouTubeBareChannelUploads follows the pinned YoutubeTabIE bare-channel
// policy: use the videos page to discover upload tabs, then expose videos,
// streams, and Shorts as one lazy ordered playlist. Home-page shelves are
// never treated as the channel's upload corpus.
func extractYouTubeBareChannelUploads(ctx context.Context, transport Transport, spec youtubeBareChannelSpec) (Extraction, error) {
	if err := ctx.Err(); err != nil {
		return Extraction{}, err
	}
	if transport == nil || spec.canonical == "" || spec.videosURL == "" ||
		spec.fallbackID == "" || spec.categorize == nil || spec.extractTab == nil {
		return Extraction{}, fmt.Errorf("%w: invalid YouTube bare channel configuration", ErrUnsupported)
	}

	pageURL := spec.videosURL
	page, headers, err := transport.ReadPage(ctx, pageURL)
	if err != nil && youtubeBareChannelShouldTryRoot(err) {
		pageURL = spec.canonical
		page, headers, err = transport.ReadPage(ctx, pageURL)
	}
	if err != nil {
		return Extraction{}, spec.categorize(err)
	}
	raw, err := extractJSONObject(page, youtubeInitialDataMarker)
	if err != nil {
		return Extraction{}, fmt.Errorf("%w: YouTube %s root initial data", ErrInvalidMetadata, spec.subject)
	}
	metadata, err := parseYouTubeHandleTabData(raw, "videos")
	if err != nil {
		return Extraction{}, err
	}
	if metadata.alert != "" && len(metadata.entries) == 0 {
		return Extraction{}, youtubeBareChannelAlertError(spec.subject, metadata.alert)
	}
	if metadata.title == "" {
		return Extraction{}, fmt.Errorf("%w: missing YouTube %s root metadata", ErrInvalidMetadata, spec.subject)
	}
	available, selected, decisive, err := youtubeBareUploadTabs(raw)
	if err != nil {
		return Extraction{}, err
	}
	if !decisive && pageURL == spec.videosURL {
		available["videos"] = true
		selected = "videos"
	}

	tabs := make([]string, 0, 3)
	for _, tab := range []string{"videos", "streams", "shorts"} {
		if available[tab] {
			tabs = append(tabs, tab)
		}
	}
	// Topic channels may advertise no upload-bearing tab even though their
	// equivalent UU playlist exists. The pinned extractor treats an
	// unavailable derived playlist as an empty channel, but cancellation must
	// remain terminal.
	if len(tabs) == 0 && youtubeChannelIDPattern.MatchString(metadata.channelID) {
		uploadsID := "UU" + metadata.channelID[2:]
		uploadsURL := "https://www.youtube.com/playlist?list=" + uploadsID
		uploads, playlistErr := extractYouTubePlaylist(ctx, Request{
			URL: uploadsURL, Transport: transport,
		}, uploadsID)
		if playlistErr == nil {
			return uploads, nil
		}
		if errors.Is(playlistErr, context.Canceled) || errors.Is(playlistErr, context.DeadlineExceeded) {
			return Extraction{}, playlistErr
		}
	}
	preloadVideos := pageURL == spec.videosURL && selected == "videos" && available["videos"]
	entries := youtubeBareChannelEntries{
		tabs: tabs,
		load: func(ctx context.Context, tab string) (EntrySequence, error) {
			tabTransport := transport
			if tab == "videos" && preloadVideos {
				tabTransport = &youtubePreloadedPageTransport{
					Transport: transport,
					rawURL:    spec.videosURL,
					page:      page,
					headers:   headers,
				}
			}
			extracted, err := spec.extractTab(ctx, tabTransport, tab)
			if err != nil {
				return nil, err
			}
			if !extracted.IsPlaylist() {
				return nil, fmt.Errorf("%w: YouTube %s upload tab is not a playlist", ErrInvalidPlaylist, spec.subject)
			}
			return extracted.Entries, nil
		},
	}

	id := spec.fallbackID
	if youtubeChannelIDPattern.MatchString(metadata.channelID) {
		id = metadata.channelID
	}
	return Playlist(value.NewInfo(value.NewObject(
		value.Field{Key: "id", Value: value.String(id)},
		value.Field{Key: "title", Value: value.String(metadata.title)},
		value.Field{Key: "webpage_url", Value: value.String(spec.canonical)},
	)), entries)
}

func youtubeBareChannelShouldTryRoot(err error) bool {
	var status *HTTPStatusError
	return errors.As(err, &status) && (status.Code == http.StatusNotFound || status.Code == http.StatusGone)
}

func youtubeBareChannelAlertError(subject, alert string) error {
	lower := strings.ToLower(alert)
	if strings.Contains(lower, "private") || strings.Contains(lower, "sign in") || strings.Contains(lower, "login") {
		return fmt.Errorf("%w: %s root access denied", ErrAuthentication, subject)
	}
	return fmt.Errorf("%w: %s root unavailable", ErrUnavailable, subject)
}

// youtubeBareUploadTabs returns only the upload-bearing tabs advertised by a
// decisive two-column tabs array. Unknown/localized tabs are ignored, while
// contradictory selected-tab identities fail closed.
func youtubeBareUploadTabs(data []byte) (available map[string]bool, selected string, decisive bool, err error) {
	available = make(map[string]bool)
	var root value.Value
	if decodeErr := json.Unmarshal(data, &root); decodeErr != nil {
		return nil, "", false, fmt.Errorf("%w: decode YouTube bare channel tabs", ErrInvalidMetadata)
	}
	rootObject, ok := root.Object()
	if !ok {
		return nil, "", false, fmt.Errorf("%w: YouTube bare channel root", ErrInvalidMetadata)
	}
	contents, ok := rootObject.Lookup("contents").Object()
	if !ok {
		return available, "", false, nil
	}
	browse, ok := contents.Lookup("twoColumnBrowseResultsRenderer").Object()
	if !ok {
		return available, "", false, nil
	}
	tabs, ok := browse.Lookup("tabs").ListValue()
	if !ok || len(tabs) == 0 {
		return available, "", false, nil
	}
	if len(tabs) > youtubeMaxBareChannelTabs {
		return nil, "", false, fmt.Errorf("%w: too many YouTube bare channel tabs", ErrInvalidMetadata)
	}
	decisive = true
	selectedCount := 0
	for _, tabValue := range tabs {
		tabObject, ok := tabValue.Object()
		if !ok {
			continue
		}
		for _, rendererName := range []string{"tabRenderer", "expandableTabRenderer"} {
			renderer, ok := tabObject.Lookup(rendererName).Object()
			if !ok {
				continue
			}
			identities := youtubeSelectedTabIdentities(renderer)
			if len(identities) > 1 {
				first := identities[0]
				for _, identity := range identities[1:] {
					if identity != first {
						return nil, "", false, fmt.Errorf("%w: conflicting YouTube bare channel tab identity", ErrInvalidMetadata)
					}
				}
			}
			identity := ""
			if len(identities) != 0 {
				identity = identities[0]
			}
			switch identity {
			case "videos", "streams", "shorts":
				available[identity] = true
			}
			isSelected, _ := renderer.Lookup("selected").Bool()
			if isSelected {
				selectedCount++
				if selectedCount > 1 {
					return nil, "", false, fmt.Errorf("%w: multiple selected YouTube bare channel tabs", ErrInvalidMetadata)
				}
				selected = identity
			}
		}
	}
	return available, selected, decisive, nil
}

type youtubePreloadedPageTransport struct {
	Transport
	rawURL  string
	page    []byte
	headers http.Header
}

func (transport *youtubePreloadedPageTransport) ReadPage(ctx context.Context, rawURL string) ([]byte, http.Header, error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	if rawURL != transport.rawURL {
		return transport.Transport.ReadPage(ctx, rawURL)
	}
	return append([]byte(nil), transport.page...), transport.headers.Clone(), nil
}

type youtubeBareChannelEntries struct {
	tabs []string
	load func(context.Context, string) (EntrySequence, error)
}

func (entries youtubeBareChannelEntries) Iterator() EntryIterator {
	return &youtubeBareChannelIterator{
		tabs: append([]string(nil), entries.tabs...),
		load: entries.load,
	}
}

type youtubeBareChannelIterator struct {
	tabs    []string
	load    func(context.Context, string) (EntrySequence, error)
	index   int
	entries int
	current EntryIterator
	done    bool
}

func (iterator *youtubeBareChannelIterator) Next(ctx context.Context) (Entry, bool, error) {
	if err := contextError(ctx); err != nil {
		iterator.done = true
		return Entry{}, false, err
	}
	if iterator.done {
		return Entry{}, false, nil
	}
	for {
		if iterator.current != nil {
			entry, ok, err := iterator.current.Next(ctx)
			if err != nil {
				iterator.done = true
				return Entry{}, false, err
			}
			if ok {
				iterator.entries++
				if iterator.entries > defaultMaxPlaylistEntries {
					iterator.done = true
					return Entry{}, false, ErrPlaylistLimit
				}
				return entry, true, nil
			}
			iterator.current = nil
		}
		if iterator.index >= len(iterator.tabs) {
			iterator.done = true
			return Entry{}, false, nil
		}
		tab := iterator.tabs[iterator.index]
		iterator.index++
		sequence, err := iterator.load(ctx, tab)
		if err != nil {
			iterator.done = true
			return Entry{}, false, err
		}
		if sequence == nil {
			iterator.done = true
			return Entry{}, false, fmt.Errorf("%w: missing YouTube bare channel tab entries", ErrInvalidPlaylist)
		}
		iterator.current = sequence.Iterator()
	}
}
