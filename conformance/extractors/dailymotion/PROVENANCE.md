# Dailymotion discovery fixture provenance

These deterministic, synthetic, license-safe fixtures model the public
`graphql.api.dailymotion.com` collection shapes used by the pinned yt-dlp
reference at `aefce1eea4d0b6bab1ec2bd3beff09bff91a39c8`, specifically
`DailymotionBaseInfoExtractor`, `DailymotionPlaylistIE`,
`DailymotionSearchIE`, and `DailymotionUserIE`
in `yt_dlp/extractor/dailymotion.py` (lines 26–99 and 541 onward).

`token.json` models the anonymous `client_credentials` OAuth response from
`DailymotionBaseInfoExtractor._get_token`. The native Go slice uses the same
OAuth client credential pair embedded by that upstream class; it is request
material rather than a user secret and never appears in diagnostics.

`playlist_page1.json`, `search_page*.json`, and `user_page*.json` model the
`collection(...).videos(...)`, `SEARCH_QUERY`, and
`channel(...).videos(...)` pagination envelopes. `search_page1_bad_edge.json`
is a full 20-entry page with one malformed node to prove fail-closed
pagination. `media.json` models the public media GraphQL fragment, while
`master.m3u8`, `720.m3u8`, and `en.vtt` are minimal signed-query playback and
subtitle fixtures used by product-level isolation tests. All IDs, text, and
URLs were independently authored. No production code reads this directory.

Deliberate hardening beyond the reference: exact-origin credential-isolated
token acquisition, scoped Authorization GraphQL POSTs with no redirects,
identifier and search-term bounds, reserved-route rejection, hostile node URL
rejection with canonical child emission, aggregate page and entry ceilings,
repeated full-page detection, and bounded `allowExplicit: true` user uploads
without a family-filter request option in this public slice.

The video extractor also preserves the pinned `_yes_playlist` choice on
video-context URLs: `/video/{id}?playlist={playlist}` prefers the playlist
redirect by default and honors `Request.NoPlaylist` for the video branch. A
playlist-only player URL remains a playlist redirect, and the video branch
retains the original query bytes in `webpage_url`; see
`internal/extractor.TestDailymotionNoPlaylistAmbiguousURLChoice` and
`pkg/ytdlp.TestProductDailymotionNoPlaylistChoice`.
