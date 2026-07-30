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

## Contextual video routes

The exact channel, group, album, and showcase child-video shapes model the
context-bearing alternatives in `VimeoIE._VALID_URL` at the pinned reference
(`yt_dlp/extractor/vimeo.py`, lines 558-588), including its group, album, and
showcase cases near lines 842-848 and channel case near line 923:

- `/channels/{safe-slug}/{numeric-id}`
- `/groups/{safe-slug}/videos/{numeric-id}`
- `/album/{safe-id}/video/{numeric-id}`
- `/showcase/{safe-id}/video/{numeric-id}`

The existing synthetic `page.html` and `config.json` fixtures are reused. Tests
assert that the exact canonical contextual page is fetched and retained as the
token-free config Referer; no live Vimeo response or private identifier is
copied into the corpus.

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

## Album and showcase API fixtures

`album-slug-auth.json`, `album-viewer.json`, `album-metadata.json`, and
`album-videos-page1.json` model the anonymous public album subset of pinned
`VimeoAlbumIE` (`yt_dlp/extractor/vimeo.py`, lines 1554-1687):

- `GET https://vimeo.com/showcase/{slug}/auth` with
  `X-Requested-With: XMLHttpRequest` resolves a safe public slug through
  `metadata.id`; pinned 200/401/403 JSON behavior is represented;
- `GET https://vimeo.com/_next/viewer` supplies a short-lived application JWT;
- `GET https://api.vimeo.com/albums/{id}` supplies name, description, and
  public privacy metadata;
- bounded `/albums/{id}/videos` pages supply `link`/`uri` identity pairs.

The slug `synthetic-showcase` and its resolver response are invented. The
JWT-like value is synthetic, nonfunctional, and used only to verify scoped
header placement. Album ID 7, all video IDs, metadata, links, and response
bytes are invented. The hostile rows deliberately cover cross-origin links
and link/URI identity disagreement.

## Authenticated private/unlisted video fixtures

`video-viewer.json`, `video-api-private.json`, `video-api-5460.json`,
`video-config-private.json`, `video-source-privacy.json`, and
`video-source-download.json` are attributable synthetic fixtures modeled from
the pinned `VimeoIE._fetch_viewer_info`, `_call_videos_api`,
`_extract_from_api`, and `_extract_original_format` request/response fields at
`yt-dlp/yt-dlp@aefce1eea4d0b6bab1ec2bd3beff09bff91a39c8`.

Every identifier, JWT segment, session marker, config token, timestamp, count,
title, description, media URL, and response byte is invented and
nonfunctional. The authenticated viewer fixture has an expiry of 2100-01-01
and a literal synthetic signature. Tests use an in-memory no-redirect
transport and make no request to Vimeo or a CDN.

The corpus proves the exact credential boundaries: the synthetic `vimeo`
cookie is available only to `https://vimeo.com/_next/viewer`, the resulting
JWT is available only to exact `api.vimeo.com` video endpoints, and the player
config plus emitted media/source URLs carry neither credential. It also proves
API URI and player-config ID agreement, pinned authenticated metadata
preservation, the logged-in `privacy`/`download` source-format path, and strict
numeric `error_code` 5460 categorization.

## Vimeo fingerprint-block status fixtures

`antibot-403.json` and `antibot-429.json` are invented, body-safe fixtures for
the narrowly pinned response contexts in `VimeoIE._real_extract`: a 403 from a
`vimeo.com` page and a 429 from a `player.vimeo.com` page. They do not model a
generic rate limit or `Retry-After` behavior. Tests also assert the inverse
host/status pairs remain unclassified and that signed query values are never
included in diagnostics.
