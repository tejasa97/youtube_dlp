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
