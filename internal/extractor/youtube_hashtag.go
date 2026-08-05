package extractor

// Bounded YouTube hashtag tab extraction. Uses the shared renderer walker and
// accepts only exact /hashtag/{tag} paths previously validated by hashtag tiles.

import (
	"context"
	"fmt"
	"net/url"
	"strings"

	"github.com/tejasa97/youtube_dlp/internal/value"
)

const (
	youtubeHashtagMaxCount     = 100
	youtubeHashtagMaxTagBytes  = 100
	youtubeHashtagExtractorKey = "youtube_hashtag"
)

// YouTubeHashtag extracts public hashtag browse pages as lazy playlists.
type YouTubeHashtag struct{}

func NewYouTubeHashtag() YouTubeHashtag { return YouTubeHashtag{} }
func (YouTubeHashtag) Name() string     { return youtubeHashtagExtractorKey }
func (YouTubeHashtag) Suitable(u *url.URL) bool {
	_, _, ok := youtubeHashtagTarget(u)
	return ok
}

func (YouTubeHashtag) Extract(ctx context.Context, request Request) (Extraction, error) {
	if err := ctx.Err(); err != nil {
		return Extraction{}, err
	}
	if request.Transport == nil {
		return Extraction{}, ErrUnsupported
	}
	u, err := url.Parse(request.URL)
	if err != nil {
		return Extraction{}, fmt.Errorf("%w: invalid YouTube hashtag URL", ErrUnsupported)
	}
	tag, canonical, ok := youtubeHashtagTarget(u)
	if !ok {
		return Extraction{}, fmt.Errorf("%w: unsupported YouTube hashtag", ErrUnsupported)
	}
	page, _, err := request.Transport.ReadPage(ctx, canonical)
	if err != nil {
		return Extraction{}, categorizeYouTubeChannelError(err)
	}
	raw, err := extractJSONObject(page, youtubeInitialDataMarker)
	if err != nil {
		return Extraction{}, fmt.Errorf("%w: YouTube hashtag initial data", ErrInvalidMetadata)
	}
	parsed, err := parseYouTubeRendererData(raw, youtubeRendererPolicy{
		kinds: youtubeRendererVideo | youtubeRendererPlaylist | youtubeRendererChannel,
	})
	if err != nil {
		return Extraction{}, err
	}
	if parsed.alert != "" && len(parsed.entries) == 0 {
		return Extraction{}, youtubeRendererAlertError(parsed.alert, "hashtag")
	}
	title := parsed.title
	if title == "" {
		title = "#" + tag
	}
	config := extractYouTubePlaylistConfig(page)
	visitor := parsed.visitorData
	if visitor == "" {
		visitor = config.VisitorData
	}
	entries, err := youtubeHashtagEntries(parsed.entries, parsed.continuation, visitor, func(ctx context.Context, token, visitorData string) ([]Entry, string, string, error) {
		return fetchYouTubeBrowseContinuation(ctx, request.Transport, token, visitorData, config, youtubeRendererPolicy{
			kinds: youtubeRendererVideo | youtubeRendererPlaylist | youtubeRendererChannel,
		}, "hashtag", categorizeYouTubeChannelError, nil)
	})
	if err != nil {
		return Extraction{}, err
	}
	info := youtubeRendererPlaylistInfoWithCounts(tag, title, canonical, nil, parsed.playlistCount, parsed.hasCount, parsed.viewCount, parsed.hasViewCount)
	info.Set("extractor_key", value.String(youtubeHashtagExtractorKey))
	return Playlist(info, entries)
}

func youtubeHashtagTarget(u *url.URL) (tag, canonical string, ok bool) {
	if u == nil || u.User != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return "", "", false
	}
	if strings.Contains(u.Host, ":") || u.Fragment != "" || u.RawPath != "" || u.RawQuery != "" {
		return "", "", false
	}
	host := strings.ToLower(strings.TrimSuffix(u.Hostname(), "."))
	if host != "youtube.com" && host != "www.youtube.com" {
		return "", "", false
	}
	parts := strings.Split(u.Path, "/")
	if len(parts) != 3 || parts[1] != "hashtag" || parts[2] == "" {
		return "", "", false
	}
	tag = parts[2]
	if len(tag) > youtubeHashtagMaxTagBytes || strings.ContainsAny(tag, `/\.%`) || strings.ContainsRune(tag, 0) {
		return "", "", false
	}
	canonical = "https://www.youtube.com/hashtag/" + tag
	return tag, canonical, true
}

func youtubeHashtagEntries(first []Entry, token, visitor string, fetch StatefulContinuationFetcher) (EntrySequence, error) {
	base, err := StatefulContinuationEntries(first, token, visitor, fetch)
	if err != nil {
		return nil, err
	}
	return limitedEntries{source: base, limit: youtubeHashtagMaxCount}, nil
}
