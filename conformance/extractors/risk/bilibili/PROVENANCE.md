# Bilibili public ecosystem provenance

Reference: yt-dlp commit `aefce1eea4d0b6bab1ec2bd3beff09bff91a39c8`, reviewed
class-by-class against `yt_dlp/extractor/bilibili.py`.

## Promoted matrix

| Upstream class | Go key | Public route | Proof |
|---|---|---|---|
| BiliBiliIE | `bilibili` | domestic `/video/{BV|av}` and `/festival/*?bvid=` hydration, anthology, DASH and durl | `TestBilibiliHydrationAndAnthology`, `TestProductBilibiliDomesticDASHExactBytes` |
| BiliBiliPlayerIE | `bilibili_player` | `player.bilibili.com/player.html?aid=` | `TestBilibiliPlayerDynamicIntlRoutes`, `TestProductBilibiliTransparentPlayerAndPlaylistReentry` |
| BiliBiliDynamicIE | `bilibili_dynamic` | `t.bilibili.com/{id}`, `bilibili.com/opus/{id}` | `TestBilibiliPlayerDynamicIntlRoutes` |
| BiliBiliBangumiIE | `bilibili_bangumi` | `/bangumi/play/ep{id}` | `TestBilibiliBangumiAndReusablePlaylists`, `TestProductBilibiliBangumiExactBytesAndScopedReferer` |
| BiliBiliBangumiMediaIE / SeasonIE | `bilibili_bangumi_media` / `bilibili_bangumi_season` | `/bangumi/media/md{id}`, `/bangumi/play/ss{id}` | `TestBilibiliBangumiAndReusablePlaylists`, `TestBilibiliRetainedPlaylistSequences`, `TestProductBilibiliRetainedPlaylistFamilies` |
| BilibiliCollectionListIE / SeriesListIE | `bilibili_collection` / `bilibili_series` | bounded `/lists/{id}` and reviewed channel aliases | `TestBilibiliCollectionSeriesCategoryAndAudioRoutes`, `TestBilibiliRetainedPlaylistSequences`, `TestProductBilibiliRetainedPlaylistFamilies` |
| BilibiliCategoryIE | `bilibili_category` | supported pinned `kichiku` category routes | `TestBilibiliCollectionSeriesCategoryAndAudioRoutes`, `TestBilibiliRetainedPlaylistSequences`, `TestProductBilibiliRetainedPlaylistFamilies` |
| BilibiliAudioIE / AudioAlbumIE | `bilibili_audio` / `bilibili_audio_album` | `/audio/au{id}`, `/audio/am{id}` | `TestBilibiliCollectionSeriesCategoryAndAudioRoutes`, `TestBilibiliRetainedPlaylistSequences`, `TestProductBilibiliAudioExactBytes`, `TestProductBilibiliRetainedPlaylistFamilies` |
| BiliIntlIE / BiliIntlSeriesIE | `biliintl` / `biliintl_series` | anonymous `bilibili.tv` and `biliintl.com` video/series routes | `TestBilibiliPlayerDynamicIntlRoutes`, `TestBilibiliRetainedPlaylistSequences`, `TestProductBilibiliInternationalExactBytes`, `TestProductBilibiliRetainedPlaylistFamilies` |

The promotion is bounded to the listed URL shapes, anonymous public API response
shapes, and attributable domestic/international media and thumbnail hosts. All
page and API requests require the credential-isolated no-redirect capability;
the two API families that carry a referer use only the dedicated interface with
the fixed Bilibili referer. Emitted media formats are marked for credential
isolation, preserving signed query strings unchanged. Every retained playlist
family has registered-product exact-child-byte evidence; paged and lazy
sequences are consumed partially and twice deterministically, with cancellation
and repeated-row safeguards covered by extractor tests.

## Deferred matrix

| Upstream class/family | Reason |
|---|---|
| BilibiliCheeseIE / CheeseSeasonIE | paid-course and purchase/playability states are not a proven anonymous public contract; no generated implementation is registered |
| BilibiliSpaceVideoIE / SpaceAudioIE | WBI-dependent APIs; no unsigned anonymous contract |
| BiliBiliSearchIE | WBI-dependent search API; no unsigned anonymous contract |
| BiliLiveIE | HLS/live manifest and segment exact-byte product proof is not retained |
| BilibiliFavoritesListIE / WatchlaterIE / PlaylistIE | authentication/private or playlist-state behavior is explicitly deferred |

Favorites, watchlater, generic medialists, paid/DRM, authenticated/private,
geo-only, and live/HLS behavior are not claimed by the Go registry. No
fixture-only hostname, mutable test switch, subtitle implementation, or
remote live state is part of the promotion.

For domestic multi-page anthology URLs without an explicit `p` parameter, the
registered `bilibili` extractor follows the pinned `_yes_playlist(video_id,
video_id)` choice: the default returns a lazy playlist, while `Request.NoPlaylist`
selects the first video page. Explicit `?p=` URLs and separate collection/list
routes remain video or playlist routes as registered. Choice and product
re-entry evidence is in `TestBilibiliNoPlaylistAnthologyChoice` and
`TestProductBilibiliNoPlaylistChoice`.
