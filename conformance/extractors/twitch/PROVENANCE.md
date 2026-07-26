# Twitch fixture provenance

These deterministic, synthetic responses model the Twitch stream extraction
contract in the pinned read-only yt-dlp reference checkout:

- repository: `yt-dlp/yt-dlp`
- commit: `aefce1eea4d0b6bab1ec2bd3beff09bff91a39c8`
- source: `yt_dlp/extractor/twitch.py`, `TwitchBaseIE` and
  `TwitchStreamIE._real_extract`
- client ID: `ue6666qo983tsx6so1t0vnawi233wa`
- `StreamMetadata` SHA-256 persisted-query hash:
  `ad022ca32220d5523d03a23cbcb5beaa1e0999889c1f8f78f9f2520dafb5cae6`
- `ComscoreStreamingQuery` SHA-256 persisted-query hash:
  `e1edae8122517d013405f237ffcc124515dc6ded82480a88daef69c83b53ac01`
- `VideoPreviewOverlay` SHA-256 persisted-query hash:
  `9515480dee68a77e667cb19de634739d33f243572b007e98e67184b1a5d8369f`

The metadata fixture follows the reference's three-operation request and its
division of fields: stream identity/timing from `StreamMetadata`, display name
and broadcast title from `ComscoreStreamingQuery`, and preview URL from
`VideoPreviewOverlay`. The token fixture follows the raw
`streamPlaybackAccessToken(channelName: ..., params: ...)` GraphQL query. The
expected fixture records the reference normalization semantics for a live
stream. Values and hosts are invented; no live Twitch response or credential
was copied.

The upstream `p` cache-buster is random in the inclusive range 1,000,000 to
10,000,000. Tests validate that range and compare the semantic query fields,
not the intentionally non-deterministic value.

The offline HLS playlists and segments used by `twitch_test.go` are test-only
synthetic media. They verify that the signed Usher URL produced by extraction
can drive the repository's existing master-playlist selection, live polling,
sequence de-duplication, and ordered fragment assembly without network access.

## Phase 3 VOD and clip breadth fixtures

The VOD and clip fixtures added on 2026-07-19 are attributable to the same
pinned source, specifically:

- `TwitchBaseIE._OPERATION_HASHES` and `_download_access_token`, lines 43–176;
- `TwitchVodIE._download_info`, `_extract_info_gql`, `_extract_chapters`, and
  `_real_extract`, lines 412–608;
- `TwitchClipsIE._real_extract`, lines 1129–1322.

The VOD fixture models the three persisted operations (`VideoMetadata`, chapter
selection, and seek preview), the `videoPlaybackAccessToken` query, signed
Usher `/vod/{id}.m3u8`, archived-live state, start offset, thumbnails, and
chapters. The clip fixture models `ShareClipRenderStatus`, inline playback
tokens, landscape/portrait direct qualities, thumbnails, broadcaster/curator,
and category fields.

All identifiers, counts, timestamps, titles, response bodies, signed tokens,
and `.example.test` asset hosts are synthetic. No Twitch response or user data
was captured. Tests never fetch the declared VOD, clip, thumbnail, or storyboard
assets. Python and the reference checkout are not used at build or runtime.

## Channel videos playlist fixtures

The channel videos playlist fixtures added on 2026-07-24 are attributable to
the same pinned source, specifically:

- `TwitchBaseIE._OPERATION_HASHES['FilterableVideoTower_Videos']`, line 45;
- `TwitchPlaylistBaseIE._entries`, lines 649–697 (`_PAGE_LIMIT = 100`, cursor
  advancement from the last valid emitted edge, empty-edge stop, and
  `user.id == ""` channel-not-found guard);
- `TwitchVideosBaseIE`, lines 700–712 (`FilterableVideoTower_Videos`,
  `VideoEdge`/`Video` typenames, `_make_variables`);
- `TwitchVideosIE`, lines 715–820 (route grammar for `/(videos|profile)` and
  `/videos/all`, filter/sort query mapping, unknown-filter fallback to All
  Videos, title labels, and `_make_video_result` transparent VOD URLs);
- `_make_video_result`, lines 595–609.

Fixtures:

- `videos_page1.json` / `videos_page2.json`: synthetic initial and continuation
  GraphQL pages with mixed valid/invalid edges for deterministic skipping;
- `videos_empty.json`: present channel with an empty edge list;
- `videos_not_found.json`: `user.id` exact empty string with decoy edges;
- `videos_malformed.json`: non-array GraphQL envelope.

All values are synthetic. Clips playlist enumeration is intentionally not
modeled here.

## Direct collection and channel collections fixtures

The collection fixtures added on 2026-07-25 are attributable to the same pinned
source, specifically:

- `TwitchBaseIE._OPERATION_HASHES['CollectionSideBar']`, line 44;
- `TwitchBaseIE._OPERATION_HASHES['ChannelCollectionsContent']`, line 48;
- `TwitchCollectionIE`, lines 612–646 (`CollectionSideBar`, variables
  `collectionID`, `collection.items.edges` transparent VOD entries via
  `_make_video_result`);
- `TwitchPlaylistBaseIE._entries`, lines 649–697 (`_PAGE_LIMIT = 100`, cursor
  advancement from the last valid emitted edge, empty-edge stop, and
  `user.id == ""` channel-not-found guard);
- `TwitchVideosCollectionsIE`, lines 896–949 (`ChannelCollectionsContent`,
  variables `ownerLogin`, `CollectionsItemEdge`/`Collection` typenames,
  transparent `https://www.twitch.tv/collections/{id}` entries, title
  `{channel} - Collections`);
- `_make_video_result`, lines 595–609.

Fixtures:

- `collection_direct.json` / `collection_direct_empty.json` /
  `collection_direct_unavailable.json` / `collection_direct_malformed.json`:
  synthetic direct `CollectionSideBar` responses with mixed valid/invalid edges;
- `collections_page1.json` / `collections_page2.json`: synthetic initial and
  continuation `ChannelCollectionsContent` pages;
- `collections_empty.json`: present channel with an empty edge list;
- `collections_not_found.json`: `user.id` exact empty string with decoy edges;
- `collections_malformed.json`: non-array GraphQL envelope.

All values are synthetic. Clips enumeration, subscriber entitlements, chat, and
arbitrary Twitch routes remain outside this lane.

## Channel clips playlist fixtures

The channel clips playlist fixtures added on 2026-07-26 are attributable to the
same pinned source, specifically:

- `TwitchBaseIE._OPERATION_HASHES['ClipsCards__User']`, line 46
  (`1cd671bfa12cec480499c087319f26d21925e9695d1f80225aae6a4354f23088`);
- `TwitchPlaylistBaseIE._entries`, lines 649–697 (cursor advancement from the
  last valid emitted edge, empty-edge stop, and `user.id == ""`
  channel-not-found guard);
- `TwitchVideosClipsIE`, lines 823–893 (`ClipsCards__User`, `_PAGE_LIMIT = 20`,
  `ClipEdge`/`Clip` typenames, route grammar for `/clips` and
  `/videos?...filter=clips`, range mapping `24hr`→`LAST_DAY`/`Top 24H`,
  `7d`/omitted/unknown→`LAST_WEEK`/`Top 7D`, `30d`→`LAST_MONTH`/`Top 30D`,
  `all`→`ALL_TIME`/`Top All`, variables `login` + `criteria.filter`, and
  transparent clip URL entries via `_extract_entry`).

Fixtures:

- `clips_page1.json` / `clips_page2.json`: synthetic initial and continuation
  GraphQL pages with mixed valid/invalid edges for deterministic skipping;
- `clips_empty.json`: present channel with an empty edge list;
- `clips_not_found.json`: `user.id` exact empty string with decoy edges;
- `clips_malformed.json`: non-array GraphQL envelope.

All identifiers, titles, counts, timestamps, languages, and `.example.test`
thumbnail hosts are synthetic. No Twitch response or user data was captured.
Python and the reference checkout are not used at build or runtime.

## Clip historical archive IDs and preference scores

The clip fixture revision of 2026-07-26 models the remaining clip metadata
behavior of the same pinned source (repository `yt-dlp/yt-dlp`, commit
`aefce1eea4d0b6bab1ec2bd3beff09bff91a39c8`), specifically
`TwitchClipsIE._real_extract`, lines 1129–1322:

- the historical download-archive rule
  `self._search_regex(r'%7C(\d+)(?:-\d+)?.mp4', formats[-1]['url'], ...)`:
  only the final emitted format URL is inspected, the encoded `%7C` marker is
  matched without percent-decoding, the optional `-<digits>` suffix is
  excluded, and only the first numeric component is captured;
- `make_archive_id(TwitchClipsIE, old_id)` lowercases the extractor key so a
  match emits exactly `_old_archive_ids: ["twitchclips <id>"]`; when nothing
  matches, the field is omitted entirely;
- portrait asset formats carry `quality: -2` while landscape formats carry no
  quality penalty;
- thumbnail preferences are `0` for the default asset thumbnail, `-1` for the
  portrait asset thumbnail, and `-2` for the distinct clip-level `small`
  thumbnail, which is suppressed when it duplicates the default asset URL.

The portrait fixture source URL `portrait-%7C246810-12.mp4` and every derived
value are synthetic and attributable to that rule alone. No live Twitch
response, signed URL, token, or credential was captured.
