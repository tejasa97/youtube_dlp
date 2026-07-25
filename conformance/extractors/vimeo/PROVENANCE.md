# Vimeo extractor pilot corpus

This synthetic offline corpus follows `_parse_config` and the primary Vimeo
webpage flow in the pinned reference
`yt-dlp/yt-dlp@aefce1eea4d0b6bab1ec2bd3beff09bff91a39c8`: an impersonated webpage
request, HTML-escaped `data-config-url`, progressive files, sorted CDN-backed
HLS/DASH manifests, owner metadata, numeric thumbnails, and live-event status
mapping. The DASH `master.json` to `master.mpd` normalization is also derived
from that implementation.

Every URL uses reserved `.example` data except the synthetic input URL. The
token, identifiers, title, owner, media metadata, and bytes are invented. Tests
use an in-memory profile-aware transport and make no Vimeo or media request.

`request.text_tracks` models the public manual-caption list handled by
`VimeoBaseIE._parse_config` in the pinned reference (lines 325-328). Relative
and protocol-relative tracks intentionally resolve to `player.vimeo.com`; all
track data and query tokens are synthetic.

The config fixture's `player.vimeo.com` endpoint is likewise deliberate: the
extractor requests only that HTTPS origin and retains its synthetic query token
while using a canonical token-free Vimeo Referer.

## Playlist fixtures

Channel, explicit and bare user, and public group playlist pages model
`VimeoChannelIE._title_and_entries` / `_page_url` and `VimeoUserIE` at the same
pinned commit (`yt_dlp/extractor/vimeo.py`, classes starting near lines 1474,
1536, and 1688). Pagination URLs are constructed locally as
`/channels/{slug}/videos/page:{N}/`, `/{user}/videos/page:{N}/`, and
`/groups/{group}/videos/page:{N}/`. The
`rel="next"` marker is only an existence indicator; page-declared next hrefs
are never fetched.

| Fixture | Role |
| --- | --- |
| `channel-page1.html` / `channel-page2.html` | Multi-page channel order, titles, in-page duplicate, and missing next on page 2 |
| `user-videos-page1.html` / `user-videos-page2.html` | Explicit `/{user}/videos` multi-page user list |
| `group-page1.html` / `group-page2.html` | Public group title, multi-page order, and cross-page duplicate suppression |
| `channel-fallback.html` | Conservative `clip_ID` marker fallback when no candidate anchors exist |
| `channel-hostile.html` | Hostile/mismatched hrefs skipped; only agreeing clip retained |
| `channel-all-invalid-anchors.html` | All-invalid anchors do not fall back to bare clip IDs |

All playlist identifiers, titles, and hrefs are invented. No live Vimeo HTML
was captured into this corpus.
