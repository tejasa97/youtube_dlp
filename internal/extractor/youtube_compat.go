package extractor

import (
	"context"
	"net/url"

	youtubeprovider "github.com/tejasa97/ytdlp-go/internal/providers/youtube"
)

var (
	ErrYouTubeAliasTabNetwork        = youtubeprovider.ErrYouTubeAliasTabNetwork
	ErrYouTubeAliasTabRateLimited    = youtubeprovider.ErrYouTubeAliasTabRateLimited
	ErrYouTubeChannelNetwork         = youtubeprovider.ErrYouTubeChannelNetwork
	ErrYouTubeChannelRateLimited     = youtubeprovider.ErrYouTubeChannelRateLimited
	ErrYouTubeCommentsNetwork        = youtubeprovider.ErrYouTubeCommentsNetwork
	ErrYouTubeCommentsRateLimited    = youtubeprovider.ErrYouTubeCommentsRateLimited
	ErrYouTubeHandleTabNetwork       = youtubeprovider.ErrYouTubeHandleTabNetwork
	ErrYouTubeHandleTabRateLimited   = youtubeprovider.ErrYouTubeHandleTabRateLimited
	ErrYouTubeMusicBrowseNetwork     = youtubeprovider.ErrYouTubeMusicBrowseNetwork
	ErrYouTubeMusicBrowseRateLimited = youtubeprovider.ErrYouTubeMusicBrowseRateLimited
	ErrYouTubeMusicSearchNetwork     = youtubeprovider.ErrYouTubeMusicSearchNetwork
	ErrYouTubeMusicSearchRateLimited = youtubeprovider.ErrYouTubeMusicSearchRateLimited
	ErrYouTubeSearchNetwork          = youtubeprovider.ErrYouTubeSearchNetwork
	ErrYouTubeSearchRateLimited      = youtubeprovider.ErrYouTubeSearchRateLimited
)

type YouTubeReloadRequest = youtubeprovider.YouTubeReloadRequest

func ReloadYouTubePlayer(ctx context.Context, transport Transport, request YouTubeReloadRequest) (Extraction, error) {
	return youtubeprovider.ReloadYouTubePlayer(ctx, transport, request)
}

type youtubeProviderAdapter struct {
	provider youtubeprovider.Provider
}

func (adapter youtubeProviderAdapter) Name() string { return adapter.provider.Name() }
func (adapter youtubeProviderAdapter) Suitable(parsed *url.URL) bool {
	return adapter.provider.Suitable(parsed)
}
func (adapter youtubeProviderAdapter) Extract(ctx context.Context, request Request) (Extraction, error) {
	return adapter.provider.Extract(ctx, request.YouTubeRequest())
}

type YouTube struct{ youtubeProviderAdapter }
type YouTubeMusicSearch struct{ youtubeProviderAdapter }
type YouTubeMusicBrowse struct{ youtubeProviderAdapter }
type YouTubeHashtag struct{ youtubeProviderAdapter }
type YouTubeAliasTab struct{ youtubeProviderAdapter }
type YouTubeHandleTab struct{ youtubeProviderAdapter }
type YouTubeChannelTab struct{ youtubeProviderAdapter }

type YouTubeSearch struct {
	youtubeProviderAdapter
	provider youtubeprovider.YouTubeSearch
}

func NewYouTube() YouTube {
	return YouTube{youtubeProviderAdapter{provider: youtubeprovider.NewYouTube()}}
}

func NewYouTubeMusicSearch() YouTubeMusicSearch {
	return YouTubeMusicSearch{youtubeProviderAdapter{provider: youtubeprovider.NewYouTubeMusicSearch()}}
}

func NewYouTubeMusicBrowse() YouTubeMusicBrowse {
	return YouTubeMusicBrowse{youtubeProviderAdapter{provider: youtubeprovider.NewYouTubeMusicBrowse()}}
}

func NewYouTubeSearch() YouTubeSearch {
	provider := youtubeprovider.NewYouTubeSearch()
	return YouTubeSearch{youtubeProviderAdapter: youtubeProviderAdapter{provider: provider}, provider: provider}
}

func (extractor YouTubeSearch) SupportsSearchPrefix(prefix string) bool {
	return extractor.provider.SupportsSearchPrefix(prefix)
}

func (extractor YouTubeSearch) SearchQueryAllowed(query string) bool {
	return extractor.provider.SearchQueryAllowed(query)
}

func NewYouTubeHashtag() YouTubeHashtag {
	return YouTubeHashtag{youtubeProviderAdapter{provider: youtubeprovider.NewYouTubeHashtag()}}
}

func NewYouTubeAliasTab() YouTubeAliasTab {
	return YouTubeAliasTab{youtubeProviderAdapter{provider: youtubeprovider.NewYouTubeAliasTab()}}
}

func NewYouTubeHandleTab() YouTubeHandleTab {
	return YouTubeHandleTab{youtubeProviderAdapter{provider: youtubeprovider.NewYouTubeHandleTab()}}
}

func NewYouTubeChannelTab() YouTubeChannelTab {
	return YouTubeChannelTab{youtubeProviderAdapter{provider: youtubeprovider.NewYouTubeChannelTab()}}
}

type youtubeTarget struct {
	videoID            string
	startTime, endTime *float64
	startSet, endSet   bool
}

func parseYouTubeTarget(rawURL string) (youtubeTarget, error) {
	target, err := youtubeprovider.ParseTarget(rawURL)
	if err != nil {
		return youtubeTarget{}, err
	}
	return youtubeTarget{
		videoID:   target.VideoID,
		startTime: target.StartTime,
		endTime:   target.EndTime,
		startSet:  target.StartSet,
		endSet:    target.EndSet,
	}, nil
}
