# Vimeo channel and user-videos playlist evidence

Status: compatible for bounded anonymous public Vimeo channel roots and
explicit `/{user}/videos` playlists only.

Pinned behavioral reference: `yt-dlp/yt-dlp@aefce1eea4d0b6bab1ec2bd3beff09bff91a39c8`,
`yt_dlp/extractor/vimeo.py` classes `VimeoChannelIE` and `VimeoUserIE`
(`_page_url`, `_title_and_entries`, `_MORE_PAGES_INDICATOR`, channel
`_TITLE_RE`, user `_TITLE_RE`).

## Scope

Accepted routes:

- `https://vimeo.com/channels/{safe-slug}` with optional trailing slash
- `https://vimeo.com/{safe-user}/videos` with optional trailing slash

Existing numeric video and `player.vimeo.com/video/{id}` routes are unchanged.
Caller query and fragment are stripped for playlist fetches and are rejected
by playlist suitability. Requests use `ReadPageWithProfile` with the existing
`chrome-133` profile and only the locally constructed HTTPS pagination URL.

Entries are lazy URL results pointing at canonical `https://vimeo.com/{id}`
targets accepted by the existing video route. Child videos are never hydrated
during playlist extraction. Page order is preserved; duplicate clip IDs are
suppressed by first occurrence across pages. Declared hrefs are evidence for
ID/title agreement only.

## Go hardening and deliberate deviations

- Public channel plus explicit `/videos` only. Bare user roots are rejected
  (upstream `VimeoUserIE` also matches bare `/{user}/`).
- No showcases/albums, groups, likes, watch-later, password submission, or
  authenticated/private media.
- Page-declared next URLs are never followed; only a bounded `rel=next`
  presence indicator advances a locally constructed page number.
- Hostile, cross-origin, mismatched, credentialed, ported, fragmented, or
  encoded-separator hrefs are skipped without being echoed in errors.
- Named bounds: slug 64 bytes, titles 512 runes, page 4 MiB, 100 pages, 128
  clips/page, 10_000 total entries.
- Reserved and purely numeric user slugs fail closed.

## Fixtures and tests

Corpus and provenance: `conformance/extractors/vimeo/` (`PROVENANCE.md`,
`channel-page*.html`, `user-videos-page*.html`, `channel-fallback.html`,
`channel-hostile.html`).

| Requirement | Evidence |
| --- | --- |
| Channel multi-page order/title, duplicate suppression, lazy URL results | `TestVimeoChannelPlaylistIsLazyOrderedAndTitled` |
| Explicit user videos | `TestVimeoUserVideosPlaylist` |
| Fallback marker path | `TestVimeoPlaylistFallbackClipMarkers` |
| Exact request/profile, no child hydration | playlist transport assertions in the above |
| Hostile/mismatched hrefs | `TestVimeoPlaylistSkipsHostileAndMismatchedHrefs` |
| Suitability negatives | `TestVimeoPlaylistSuitabilityRejectsHostileInputs` |
| Page/entry bounds, missing next, cancellation, secret-safe errors | `TestVimeoPlaylistBoundsCancellationAndSecretSafeErrors` |
| Numeric video non-regression | existing `TestVimeo*` video/config/subtitle tests |
| Parser fuzz URL/ID/bound invariants | `FuzzParseVimeoPlaylistPage` |

## Primary integration checklist

Retain the registered `vimeo` extractor. Playlist results reuse the existing
Extraction playlist and URL-result primitives. Do not register a second
extractor key for this slice. User-facing site docs and parity manifests are
owned separately from this bounded increment.
