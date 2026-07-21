# YouTube extractor pilot fixture

This corpus is a synthetic, offline fixture for the Phase 1 YouTube video
extractor. It contains only structural data needed to exercise player-response
parsing, direct and signature-cipher formats, `n` transformation, signature
deciphering, HLS/DASH manifest exposure, and normalized metadata.

The player program is the pinned synthetic EJS fixture at
`conformance/javascript/ejs-0.8.0/synthetic-player.js`. Its provenance and the
exact upstream `yt-dlp-ejs` version are documented alongside that fixture.

The domains use the reserved `.example` namespace, and the in-memory test
transport rejects every unlisted URL. No live YouTube request is made. The
expected document is intentionally checked in so field presence, ordering, and
challenge-transformed URLs remain reviewable.

The playlist corpus is also synthetic and follows the renderer and continuation
shapes consumed by `YoutubeTabIE` in the pinned reference
`yt-dlp/yt-dlp@aefce1eea4d0b6bab1ec2bd3beff09bff91a39c8`: ordered
`playlistVideoRenderer` URL results, `continuationItemRenderer` cursor lookup,
and `youtubei/v1/browse` continuation requests. The client version is the
pinned reference's web client value. The live fixture follows the pinned
reference's `isLive`/`isLiveContent` to `live_status=is_live` classification and
HLS manifest exposure. All identifiers, metadata, cursors, keys, visitor data,
and domains are artificial; no captured account or production response is
stored.

`sabr-watch.html`, `android-player.json`, and `android-vr-player.json` are synthetic regression fixtures
for URL-less `serverAbrStreamingUrl` webpage responses and native-client format
recovery. Their response fields and client-request expectations are derived
from `YoutubeIE._extract_player_responses`, `_DEFAULT_CLIENTS`, and the Android
client table in the same pinned reference checkout. They also pin propagation
of the webpage visitor identity required to keep multi-client player requests
in one anonymous session. The cookie-isolation and authenticated-page policy is
derived from `_get_requested_clients` in
`yt_dlp/extractor/youtube/_video.py`, which excludes clients without
`SUPPORTS_COOKIES` when authenticated. The synthetic watch page uses the
bounded `ytcfg.set({...})` shape observed by the pinned implementation; player
URLs are accepted only from structured configuration or the player response's
`assets.js`, then constrained to HTTPS YouTube `/s/player/` paths. No production
response, media URL, cookie, visitor identifier, or account data is retained.
