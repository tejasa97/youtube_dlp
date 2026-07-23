# YouTube playlist-tab evidence

This increment adds a bounded public playlist-tab slice for exact
`/channel/<UCID>/playlists` and ASCII `/@handle/playlists` routes. It is derived
from the read-only pinned reference
`yt-dlp/yt-dlp@aefce1eea4d0b6bab1ec2bd3beff09bff91a39c8`, principally
`YoutubeTabIE._rich_entries`, `_extract_lockup_view_model`, `_extract_entries`,
`_entries`, and the continuation helpers in
`yt_dlp/extractor/youtube/_tab.py`.

## Supported behavior

- Exact public YouTube web routes for a validated 24-character UCID or the
  existing bounded ASCII handle grammar.
- Legacy `playlistRenderer` and `gridPlaylistRenderer` entries.
- Modern `lockupViewModel` entries whose type is exactly playlist or podcast.
- Transparent canonical `https://www.youtube.com/playlist?list=<id>` URL
  results routed to the existing `youtube` extractor.
- Lazy, independently reusable `youtubei/v1/browse` continuations through the
  existing bounded cursor model.
- Stable renderer occurrence order. Repeated playlist occurrences are
  preserved, while repeated continuation tokens terminate iteration.
- Existing authentication, unavailable, rate-limit, network, malformed
  metadata, traversal-limit, and cancellation categories.

Route parsing rejects non-web hosts and schemes, credentials, explicit ports,
fragments, trailing path components, encoded paths, encoded separators or NULs,
oversized URLs, invalid UCIDs, invalid handles, and unsupported tabs. Playlist
IDs use the existing bounded YouTube playlist-ID grammar. Entry titles are
limited to 4096 bytes; an invalid optional title is omitted rather than
weakening the canonical entry identity.

The deterministic fixtures contain only artificial IDs, titles, API keys,
visitor data, and continuation values. They exercise both initial and
continuation response containers, legacy and modern renderers, podcast
lockups, repeated cursors, repeated playlist occurrences, and video-lockup
rejection without contacting YouTube.

## Automated evidence

- `TestYouTubeChannelPlaylistsTabLegacyModernContinuationAndOccurrences`
- `TestYouTubeChannelPlaylistsTabRejectsHostileRenderersAndCategorizesFailures`
- `TestYouTubeHandlePlaylistsTabLegacyModernContinuationAndOccurrences`
- `TestYouTubeHandlePlaylistsTabRejectsHostileRenderersAndCategorizesFailures`
- Existing channel/handle video, Shorts, streams, routing, failure,
  continuation, reuse, and cancellation tests
- `FuzzParseYouTubeChannelTabData`
- `FuzzYouTubeChannelTabTarget`
- `FuzzParseYouTubeHandleTabData`
- `FuzzYouTubeHandleTabTarget`

## Known deviations

- Full Unicode YouTube handle grammar remains outside the bounded handle
  route.
- Channel home, community, releases, podcasts, membership, search, and custom
  `/user` or `/c` playlist tabs are not claimed.
- Only playlist and podcast lockup types are accepted on playlist tabs; other
  entity and shelf variants remain unsupported.
- Rich playlist metadata such as thumbnails, uploader details, video counts,
  badges, and availability is not copied onto transparent URL entries.
- Entry occurrences are intentionally not collapsed. Only repeated
  continuation cursors are de-duplicated.
- Authenticated, private, region-dependent, or Premium success is not claimed;
  failures remain explicit and categorized.
