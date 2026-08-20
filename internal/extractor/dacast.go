package extractor

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	"github.com/tejasa97/ytdlp-go/internal/value"
)

const dacastMaxPlaylistEntries = 256

var (
	dacastPathID       = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)
	dacastVODPath      = regexp.MustCompile(`(?i)^/vod/([A-Za-z0-9_-]{1,128})/([A-Za-z0-9_-]{1,128})/?$`)
	dacastPlaylistPath = regexp.MustCompile(`(?i)^/playlist/([A-Za-z0-9_-]{1,128})/([A-Za-z0-9_-]{1,128})/?$`)
	dacastContentID    = regexp.MustCompile(`(?i)^([A-Za-z0-9_-]{1,128})-vod-([A-Za-z0-9_-]{1,128})$`)
)

func dacastHostOK(host string) bool {
	return strings.EqualFold(host, "iframe.dacast.com")
}

// Dacast extracts HLS VOD from iframe.dacast.com embeds.
type Dacast struct{}

func NewDacast() Dacast     { return Dacast{} }
func (Dacast) Name() string { return "dacast" }

func (Dacast) Suitable(parsed *url.URL) bool {
	_, _, ok := parseDacastVODURL(parsed)
	return ok
}

func (Dacast) Extract(ctx context.Context, request Request) (Extraction, error) {
	if err := ctx.Err(); err != nil {
		return Extraction{}, err
	}
	parsed, err := url.Parse(request.URL)
	if err != nil || request.Transport == nil {
		return Extraction{}, ErrUnsupported
	}
	userID, videoID, ok := parseDacastVODURL(parsed)
	if !ok {
		return Extraction{}, ErrUnsupported
	}
	contentID := userID + "-vod-" + videoID
	infoURL := "https://playback.dacast.com/content/info?contentId=" + url.QueryEscape(contentID) + "&provider=universe"
	accessURL := "https://playback.dacast.com/content/access?contentId=" + url.QueryEscape(contentID) + "&provider=universe"
	var infoPayload struct {
		ContentInfo struct {
			Title        string  `json:"title"`
			Duration     float64 `json:"duration"`
			ThumbnailURL string  `json:"thumbnailUrl"`
		} `json:"contentInfo"`
	}
	if err := hostedRequestJSON(ctx, request.Transport, http.MethodGet, infoURL, nil, make(http.Header), &infoPayload); err != nil {
		// Info is non-fatal in reference; continue to access.
		infoPayload = struct {
			ContentInfo struct {
				Title        string  `json:"title"`
				Duration     float64 `json:"duration"`
				ThumbnailURL string  `json:"thumbnailUrl"`
			} `json:"contentInfo"`
		}{}
	}
	if err := ctx.Err(); err != nil {
		return Extraction{}, err
	}
	var access struct {
		HLS   string `json:"hls"`
		Error string `json:"error"`
	}
	if err := hostedRequestJSON(ctx, request.Transport, http.MethodGet, accessURL, nil, make(http.Header), &access); err != nil {
		return Extraction{}, err
	}
	if err := ctx.Err(); err != nil {
		return Extraction{}, err
	}
	switch strings.TrimSpace(access.Error) {
	case "":
	case "Broadcaster has been blocked", "Content is offline":
		return Extraction{}, ErrUnavailable
	default:
		if access.Error != "" {
			return Extraction{}, fmt.Errorf("%w: Dacast access denied", ErrAuthentication)
		}
	}
	hls := strings.TrimSpace(access.HLS)
	if hls == "" {
		return Extraction{}, fmt.Errorf("%w: missing Dacast HLS", ErrInvalidMetadata)
	}
	if strings.Contains(hls, "DRM_EXT") {
		return Extraction{}, fmt.Errorf("%w: Dacast DRM", ErrUnsupported)
	}
	format, ok := strictHostedURLFormat("hls", hls)
	if !ok {
		return Extraction{}, fmt.Errorf("%w: invalid Dacast HLS URL", ErrInvalidMetadata)
	}
	title := infoPayload.ContentInfo.Title
	if title == "" {
		title = videoID
	}
	fields := []value.Field{
		{Key: "id", Value: value.String(videoID)},
		{Key: "title", Value: value.String(title)},
		{Key: "uploader_id", Value: value.String(userID)},
		{Key: "webpage_url", Value: value.String("https://iframe.dacast.com/vod/" + userID + "/" + videoID)},
		{Key: "ext", Value: value.String("mp4")},
		{Key: "formats", Value: value.List(value.ObjectValue(format))},
	}
	if thumbURL := strings.TrimSpace(infoPayload.ContentInfo.ThumbnailURL); thumbURL != "" && strictValidHostedHTTPURL(thumbURL) {
		fields = append(fields, value.Field{Key: "thumbnail", Value: value.String(thumbURL)})
	}
	return Media(value.NewInfo(value.NewObject(fields...))), nil
}

func parseDacastVODURL(parsed *url.URL) (userID, videoID string, ok bool) {
	if hostedRejectUnsafeURL(parsed) || !dacastHostOK(parsed.Hostname()) {
		return "", "", false
	}
	match := dacastVODPath.FindStringSubmatch(parsed.EscapedPath())
	if len(match) != 3 || !dacastPathID.MatchString(match[1]) || !dacastPathID.MatchString(match[2]) {
		return "", "", false
	}
	return match[1], match[2], true
}

// DacastPlaylist enumerates VOD entries for a Dacast playlist embed.
type DacastPlaylist struct{}

func NewDacastPlaylist() DacastPlaylist { return DacastPlaylist{} }
func (DacastPlaylist) Name() string     { return "dacast_playlist" }

func (DacastPlaylist) Suitable(parsed *url.URL) bool {
	_, _, ok := parseDacastPlaylistURL(parsed)
	return ok
}

func (DacastPlaylist) Extract(ctx context.Context, request Request) (Extraction, error) {
	if err := ctx.Err(); err != nil {
		return Extraction{}, err
	}
	parsed, err := url.Parse(request.URL)
	if err != nil || request.Transport == nil {
		return Extraction{}, ErrUnsupported
	}
	userID, playlistID, ok := parseDacastPlaylistURL(parsed)
	if !ok {
		return Extraction{}, ErrUnsupported
	}
	contentID := userID + "-playlist-" + playlistID
	infoURL := "https://playback.dacast.com/content/info?contentId=" + url.QueryEscape(contentID) + "&provider=universe"
	canonical := "https://iframe.dacast.com/playlist/" + userID + "/" + playlistID
	info := value.NewInfo(value.NewObject(
		value.Field{Key: "id", Value: value.String(playlistID)},
		value.Field{Key: "title", Value: value.String(playlistID)},
		value.Field{Key: "webpage_url", Value: value.String(canonical)},
	))
	sequence, err := LazyFirstPageEntries(dacastMaxPlaylistEntries, func(ctx context.Context) ([]Entry, error) {
		var payload struct {
			ContentInfo struct {
				Title    string `json:"title"`
				Features struct {
					Playlist struct {
						Contents []struct {
							ID    string `json:"id"`
							Title string `json:"title"`
						} `json:"contents"`
					} `json:"playlist"`
				} `json:"features"`
			} `json:"contentInfo"`
		}
		if err := hostedRequestJSON(ctx, request.Transport, http.MethodGet, infoURL, nil, make(http.Header), &payload); err != nil {
			return nil, err
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		contents := payload.ContentInfo.Features.Playlist.Contents
		if len(contents) > dacastMaxPlaylistEntries {
			return nil, fmt.Errorf("%w: Dacast playlist overflow", ErrInvalidMetadata)
		}
		entries := make([]Entry, 0, len(contents))
		seen := make(map[string]bool, len(contents))
		for _, item := range contents {
			match := dacastContentID.FindStringSubmatch(strings.TrimSpace(item.ID))
			if len(match) != 3 {
				continue
			}
			vodUser, vodID := match[1], match[2]
			if seen[vodID] {
				continue
			}
			seen[vodID] = true
			entries = append(entries, Entry{
				URL:          "https://iframe.dacast.com/vod/" + vodUser + "/" + vodID,
				ExtractorKey: "dacast",
				ID:           vodID,
				Title:        item.Title,
			})
		}
		if len(entries) == 0 {
			return nil, fmt.Errorf("%w: empty Dacast playlist", ErrInvalidMetadata)
		}
		return entries, nil
	})
	if err != nil {
		return Extraction{}, err
	}
	return Playlist(info, sequence)
}

func parseDacastPlaylistURL(parsed *url.URL) (userID, playlistID string, ok bool) {
	if hostedRejectUnsafeURL(parsed) || !dacastHostOK(parsed.Hostname()) {
		return "", "", false
	}
	match := dacastPlaylistPath.FindStringSubmatch(parsed.EscapedPath())
	if len(match) != 3 || !dacastPathID.MatchString(match[1]) || !dacastPathID.MatchString(match[2]) {
		return "", "", false
	}
	return match[1], match[2], true
}
