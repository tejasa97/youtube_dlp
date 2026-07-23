package extractor

import (
	"net/url"
	"strings"

	"github.com/ytdlp-go/ytdlp/internal/value"
)

// youtubePublicTabKind defines the entry kinds admitted by each explicitly
// supported public channel tab. Keeping this policy shared prevents route and
// renderer handling from drifting across channel, handle, and legacy aliases.
type youtubePublicTabKind uint8

const (
	youtubeTabUnsupported youtubePublicTabKind = iota
	youtubeTabVideos
	youtubeTabPlaylists
	youtubeTabMixed
)

func youtubePublicTabType(tab string) youtubePublicTabKind {
	switch tab {
	case "videos", "shorts", "streams":
		return youtubeTabVideos
	case "playlists", "releases", "podcasts":
		return youtubeTabPlaylists
	case "home", "featured", "community":
		return youtubeTabMixed
	default:
		return youtubeTabUnsupported
	}
}

func youtubeTabAllowsVideos(tab string) bool {
	kind := youtubePublicTabType(tab)
	return kind == youtubeTabVideos || kind == youtubeTabMixed
}

func youtubeTabAllowsPlaylists(tab string) bool {
	kind := youtubePublicTabType(tab)
	return kind == youtubeTabPlaylists || kind == youtubeTabMixed
}

// youtubeCommunityPostEntries mirrors the pinned reference's bounded
// backstage-post behavior: attachment video first, attachment playlist
// second, then distinct inline YouTube video links from contentText runs.
func youtubeCommunityPostEntries(post *value.Object) []Entry {
	if post == nil {
		return nil
	}
	entries := youtubeCommunityAttachmentEntries(post)
	seenVideos := make(map[string]struct{})
	for _, entry := range entries {
		if youtubeTabEntryKind(entry) == "video" {
			seenVideos[entry.ID] = struct{}{}
		}
	}

	content, ok := post.Lookup("contentText").Object()
	if !ok {
		return entries
	}
	runs, ok := content.Lookup("runs").ListValue()
	if !ok {
		return entries
	}
	for _, runValue := range runs {
		run, ok := runValue.Object()
		if !ok {
			continue
		}
		rawURL := objectString(run, "navigationEndpoint", "urlEndpoint", "url")
		if rawURL == "" {
			continue
		}
		reference, err := url.Parse(rawURL)
		if err != nil {
			continue
		}
		resolved := (&url.URL{Scheme: "https", Host: "www.youtube.com"}).ResolveReference(reference)
		target, err := parseYouTubeTarget(resolved.String())
		if err != nil {
			continue
		}
		if _, duplicate := seenVideos[target.videoID]; duplicate {
			continue
		}
		seenVideos[target.videoID] = struct{}{}
		canonical := "https://www.youtube.com/watch?v=" + target.videoID
		if strings.HasPrefix(resolved.Path, "/shorts/") {
			canonical = "https://www.youtube.com/shorts/" + target.videoID
		}
		entries = append(entries, Entry{
			URL:          canonical,
			ExtractorKey: "youtube",
			ID:           target.videoID,
		})
	}
	return entries
}

func youtubeCommunityAttachmentEntries(post *value.Object) []Entry {
	if post == nil {
		return nil
	}
	attachment, ok := post.Lookup("backstageAttachment").Object()
	if !ok {
		return nil
	}
	var entries []Entry
	if renderer, ok := attachment.Lookup("videoRenderer").Object(); ok {
		if entry, ok := youtubeHandleTabVideoEntry(renderer); ok {
			entries = append(entries, entry)
		}
	}
	if renderer, ok := attachment.Lookup("playlistRenderer").Object(); ok {
		if entry, ok := youtubeTabPlaylistEntry(renderer); ok {
			entries = append(entries, entry)
		}
	}
	return entries
}

func youtubeTabEntryKind(entry Entry) string {
	if strings.HasPrefix(entry.URL, "https://www.youtube.com/playlist?list=") {
		return "playlist"
	}
	return "video"
}

func youtubeTabEntryKey(entry Entry) string {
	return youtubeTabEntryKind(entry) + "\x00" + entry.ID
}

func appendYouTubeTabEntry(entries *[]Entry, entry Entry, ok bool) {
	if !ok {
		return
	}
	*entries = append(*entries, entry)
}

func normalizedYouTubeTabIdentity(identity string) string {
	identity = strings.ToLower(strings.TrimSpace(identity))
	switch identity {
	case "live", "felive":
		return "streams"
	case "tab_id_sponsorships":
		return "membership"
	}
	if strings.HasPrefix(identity, "fe") {
		candidate := strings.TrimPrefix(identity, "fe")
		if youtubePublicTabType(candidate) != youtubeTabUnsupported {
			return candidate
		}
	}
	if youtubePublicTabType(identity) != youtubeTabUnsupported {
		return identity
	}
	return ""
}
