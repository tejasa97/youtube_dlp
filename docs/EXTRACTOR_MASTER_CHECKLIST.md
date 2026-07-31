# Extractor master checklist

Reference baseline: `yt-dlp/yt-dlp@aefce1eea4d0b6bab1ec2bd3beff09bff91a39c8`

The authoritative row-level checklist is
[`conformance/extractors/upstream_master_checklist.csv`](../conformance/extractors/upstream_master_checklist.csv).
It contains every concrete extractor class registered by the pinned
`yt_dlp/extractor/_extractors.py`; internal base classes that are not registered
are excluded.

## Baseline result

| Classification | Classes | Meaning |
| --- | ---: | --- |
| `already_supported` | 219 | An exact registered Go extractor mapping is known. Compatibility remains bounded by that extractor's manifest claim. |
| `partially_supported` | 73 | The site family exists in Go, but this upstream class does not have a proven exact mapping. |
| `uses_existing_shared_backend` | 49 | The upstream class visibly hands off to a backend already implemented in Go. |
| `requires_authentication_or_antibot` | 134 | The class contains explicit login, password, OAuth, authorization, or impersonation behavior. |
| `obsolete_or_intentional_deviation` | 136 | The pinned upstream class explicitly declares `_WORKING = False`. |
| `requires_new_backend` | 1,140 | No exact Go mapping or existing-backend handoff was detected; manual family review is required. |
| **Total** | **1,751** | All registered concrete classes in the pinned reference. |

Exact extractor-class coverage is therefore 219/1,751 (12.5%). Including partial
site-family coverage gives 292/1,751 (16.7%), but partial rows must not be
treated as complete. These figures measure extractor-class breadth only, not
the completion of downloaders, post-processing, the CLI, or the overall Go
port.

## How to use the checklist

1. Filter `uses_existing_shared_backend` first. These are the best candidates
   for Composer adapter batches.
2. Review `partially_supported` by existing Go family and split missing URL
   classes into bounded depth PRs.
3. Group `requires_new_backend` by upstream module/base family. Implement a
   shared backend before leaf adapters whenever several classes share one.
4. Handle `requires_authentication_or_antibot` one family at a time after the
   credential, origin, redirect, and fallback policy is designed.
5. Re-check `obsolete_or_intentional_deviation` during every reference refresh;
   `_WORKING` can change upstream.
6. Replace low-confidence generated classifications with reviewed overrides as
   implementation work proceeds.

No row should move to `already_supported` merely because its hostname is
recognized. Promotion requires successful fixture-backed extraction or
adapter-to-backend re-entry, negative routing evidence, categorized failures,
cancellation, bounds, secret safety, provenance, registry integration, and a
passing manifest claim.

## Classification limits

This is a conservative generated baseline, not 1,751 hand-reviewed design
decisions.

- Exact normalized key matches and curated aliases are high confidence.
- Same-module Go coverage is marked partial rather than assumed complete.
- Existing-backend and auth/anti-bot detection is source-token based and must
  be confirmed before implementation.
- The `requires_new_backend` bucket includes standalone public extractors as
  well as true shared families. It is intentionally low confidence.
- `_WORKING = False` is recorded as the pinned upstream state, not a permanent
  decision to exclude the extractor.

## Generator-authoritative inventory (2026-07-30)

The checklist generator is the single source of truth. Previously reviewed
CSV-only promotions are now encoded in Go:

- **`exactAliases`**: route-corpus mappings where the upstream IE name does not
  normalize to the registered Go key. This includes eleven reconciled public
  extractors (Bandcamp album, Brightcove new, Dacast VOD, Imgur album/gallery,
  Kick VOD/clip, Mixcloud user/playlist, Rumble channel/embed), the four
  fixture-backed TED public-family adapters, plus twenty fixture-backed
  Discovery/DPlay adapters and `Tele5IE`.
- **`reviewedInventory`**: preserves fixture-backed rationale text for Discovery,
  NHK, PRX search, and Tele5 without allowing unsupported promotions.

Regenerating the CSV from the pinned reference must be byte-identical to the
checked file (`cmp -s`) after source changes.

| Upstream class | Go key | Evidence |
| --- | --- | --- |
| `BandcampAlbumIE` | `bandcamp` | `bandcamp.go` album `/album/` playlist extraction |
| `BrightcoveNewIE` | `brightcove` | `brightcove.go` `players.brightcove.net` embed API |
| `DacastVODIE` | `dacast` | `dacast.go` `iframe.dacast.com/vod/...` VOD route |
| `ImgurAlbumIE` / `ImgurGalleryIE` | `imgur` | `imgur.go` `/a/` and `/gallery/` collection routes |
| `KickVODIE` / `KickClipIE` | `kick` | `kick.go` `/videos/<uuid>` and `/clips/clip_*` |
| `MixcloudUserIE` / `MixcloudPlaylistIE` | `mixcloud` | `mixcloud.go` user collections and `/playlists/` |
| `RumbleChannelIE` / `RumbleEmbedIE` | `rumble` | `rumble.go` `/c|user/` channel and `/embed/` video |
| `TedTalkIE` / `TedSeriesIE` / `TedPlaylistIE` / `TedEmbedIE` | `ted_talk` / `ted_series` / `ted_playlist` / `ted_embed` | `ted.go` strict public routes, Next metadata, direct/HLS/sidecars, series/playlist children, transparent embeds |
| Discovery/DPlay family (20 IEs) | per-adapter keys | `dplay.go` configuration-driven adapters |
| `Tele5IE` | `tele5` | `dplay.go` Aurora CMS recursion with public identity |

Deliberately left partial: `BrightcoveLegacyIE` (Go rejects legacy `/services`
URLs), `PanoptoListIE` (`List.aspx` folder API vs `panopto_playlist` pid
route), and all other family-only partial rows (bilibili, soundcloud, vimeo
sub-classes, etc.). Twitch's seven reviewed public classes are listed in the
exact mapping table below.

| Upstream class | Go key | Evidence |
| --- | --- | --- |
| `TwitchVodIE` | `twitch_vod` | shared Twitch backend, VOD/Usher/HLS product corpus |
| `TwitchCollectionIE` | `twitch_collection` | shared Twitch backend, direct collection corpus |
| `TwitchVideosIE` | `twitch_videos` | shared Twitch backend, bounded videos/profile corpus |
| `TwitchVideosClipsIE` | `twitch_videos_clips` | shared Twitch backend, bounded clips corpus |
| `TwitchVideosCollectionsIE` | `twitch_videos_collections` | shared Twitch backend, bounded collections corpus |
| `TwitchStreamIE` | `twitch_stream` | shared Twitch backend, live/rerun HLS product corpus |
| `TwitchClipsIE` | `twitch_clips` | shared Twitch backend, direct clip product corpus |
| `DailymotionIE` | `dailymotion` | isolated public metadata, direct/HLS/sidecar product corpus |
| `DailymotionPlaylistIE` | `dailymotion_playlist` | bounded public GraphQL collection pagination and child product corpus |
| `DailymotionSearchIE` | `dailymotion_search` | bounded public GraphQL search pagination and child product corpus |
| `DailymotionUserIE` | `dailymotion_user` | bounded public GraphQL user pagination and child product corpus |
| `ARDMediathekCollectionIE` | `ard_mediathek_collection` | bounded public ARD sendung/serie/sammlung pagination, season variants, and registered child product corpus |

## Refresh

The generator reads the reference checkout as source text; it does not execute
Python and is not part of normal build or test execution:

```sh
go run ./cmd/extractorinventory \
  -reference /absolute/path/to/yt-dlp-reference \
  -repository . \
  -output conformance/extractors/upstream_master_checklist.csv
```

Normal builds and tests depend only on the checked-in CSV. They have no runtime
or build-time dependency on Python or the reference checkout.
