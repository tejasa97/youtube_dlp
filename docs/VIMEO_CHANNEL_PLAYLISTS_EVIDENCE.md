# Vimeo public HTML playlist evidence

Status: compatible for bounded anonymous public Vimeo channel roots, explicit
and bare user playlists, and public group roots.

Pinned behavioral reference: `yt-dlp/yt-dlp@aefce1eea4d0b6bab1ec2bd3beff09bff91a39c8`,
`yt_dlp/extractor/vimeo.py` classes `VimeoChannelIE`, `VimeoUserIE`, and
`VimeoGroupsIE`
(`_page_url`, `_title_and_entries`, `_MORE_PAGES_INDICATOR`, channel
`_TITLE_RE`, user `_TITLE_RE`).

## Scope

Accepted routes:

- `https://vimeo.com/channels/{safe-slug}` with optional trailing slash
- `https://vimeo.com/{safe-user}/videos` with optional trailing slash
- `https://vimeo.com/{safe-user}` with optional trailing slash
- `https://vimeo.com/groups/{safe-group}` with optional trailing slash

Existing numeric video and `player.vimeo.com/video/{id}` routes are unchanged.
Caller query and fragment are rejected by playlist suitability. Playlist page
fetches use `ReadPageWithProfileWithoutCredentialsNoRedirect` with the existing
`chrome-133` profile and only the locally constructed HTTPS pagination URL.
Transports that only implement `ProfileTransport` fail closed with
`ErrTransportIsolation` before network access.

Entries are lazy URL results pointing at canonical `https://vimeo.com/{id}`
targets accepted by the existing video route. Child videos are never hydrated
during playlist extraction. Page order is preserved; duplicate clip IDs are
suppressed by first occurrence across pages. Declared hrefs are evidence for
ID/title agreement only.

## Anonymous credential boundary

Playlist pages require
`CredentialIsolatedProfilePageTransport.ReadPageProfileWithoutCredentialsNoRedirect`.
The production `network.Client` implementation:

- strips explicit and default `Authorization`, `Proxy-Authorization`, and
  `Cookie` headers;
- uses a dedicated cached impersonation client with no cookie jar and
  `DisableRedirect`;
- does not persist `Set-Cookie` into the operation jar;
- returns the first 3xx response as a bounded status error instead of following
  same-origin or cross-origin redirects;
- preserves the selected browser profile identity;
- redacts URLs in transport errors and never falls back to native transport.

Opaque playlist transport failures surface as `ErrVimeoPlaylistNetwork` without
echoing the original error string. `ErrTransportProfile`,
`ErrTransportIsolation`, context cancellation/deadline, HTTP auth/unavailable,
and page/entry bound categories are preserved.

## Go hardening and deliberate deviations

- No showcases/albums, likes, watch-later, password submission, or
  authenticated/private media.
- Nested group video and arbitrary group subpaths remain owned by video or
  unsupported routes rather than being claimed as playlists.
- Page-declared next URLs are never followed; only a bounded `rel=next`
  presence indicator advances a locally constructed page number.
- Hostile, cross-origin, mismatched, credentialed, ported, fragmented, or
  encoded-separator hrefs are skipped without being echoed in errors.
- Bare `clip_ID` marker fallback runs only when the page contains no candidate
  clip anchors. Pages whose anchors are all invalid emit no entries.
- Named bounds: slug 64 bytes, titles 512 runes, page 4 MiB, 100 pages, 128
  clips/page, 10_000 total entries.
- Reserved and purely numeric user slugs fail closed.

## Fixtures and tests

Corpus and provenance: `conformance/extractors/vimeo/` (`PROVENANCE.md`,
`channel-page*.html`, `user-videos-page*.html`, `group-page*.html`,
`channel-fallback.html`,
`channel-hostile.html`, `channel-all-invalid-anchors.html`).

| Requirement | Evidence |
| --- | --- |
| Channel multi-page order/title, duplicate suppression, lazy URL results | `TestVimeoChannelPlaylistIsLazyOrderedAndTitled` |
| Explicit user videos | `TestVimeoUserVideosPlaylist` |
| Bare user equivalence | `TestVimeoBareUserPlaylistMatchesExplicitVideos` |
| Group title/order/laziness/reuse/dedupe | `TestVimeoGroupPlaylistIsLazyOrderedReusableAndTitled` |
| Marker-only fallback | `TestVimeoPlaylistFallbackClipMarkers` |
| All-invalid anchors do not fallback | `TestVimeoPlaylistAllInvalidAnchorsDoNotFallback` |
| Isolated profile capability required | `TestVimeoPlaylistRequiresCredentialIsolatedProfileCapability` |
| Exact request/profile, no child hydration | playlist transport assertions in the above |
| Hostile/mismatched hrefs | `TestVimeoPlaylistSkipsHostileAndMismatchedHrefs` |
| Suitability negatives | `TestVimeoPlaylistSuitabilityRejectsHostileInputs` |
| Page/entry bounds, missing next, cancellation, secret-safe errors | `TestVimeoPlaylistBoundsCancellationAndSecretSafeErrors` |
| Network isolated profile page contract | `TestReadPageProfileWithoutCredentialsNoRedirect*` |
| Impersonation no-redirect config | `TestDisableRedirectReturnsFirstResponse` |
| Numeric video non-regression | existing `TestVimeo*` video/config/subtitle tests |
| Parser fuzz URL/ID/bound/no-fallback invariants | `FuzzParseVimeoPlaylistPage` |
| Route fuzz canonicalization and kind invariants | `FuzzClassifyVimeoPlaylistURL` |

## Primary integration checklist

Retain the registered `vimeo` extractor. Playlist results reuse the existing
Extraction playlist and URL-result primitives. Do not register a second
extractor key for this slice. User-facing site docs and parity manifests are
owned separately from this bounded increment.
