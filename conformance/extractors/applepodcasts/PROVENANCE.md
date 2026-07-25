# Apple Podcasts extractor fixture provenance

Behavior was derived by direct inspection of the read-only yt-dlp checkout at
commit `aefce1eea4d0b6bab1ec2bd3beff09bff91a39c8`, principally:

- `yt_dlp/extractor/applepodcasts.py` (`ApplePodcastsIE`, `_VALID_URL`, and
  `_real_extract`);
- `yt_dlp/utils/_utils.py` (`clean_podcast_url`, `clean_html`, `parse_iso8601`,
  `int_or_none`); and
- the module's `_TESTS` URL shapes (locale + show id, no-locale dual segment,
  single slug segment, and id-only segment forms).

The HTML/JSON fixtures under this directory and
`internal/extractor/testdata/applepodcasts/` are synthetic, repository-authored
pages. They preserve only attributable response shapes: the
`<script id="serialized-server-data">` envelope, `headerButtonItems` share /
`EpisodeLockup` selection, OpenGraph thumbnail meta attribute order variants,
HTML summaries, optional field absence, and fail-closed malformed / missing /
unsafe stream cases. They contain no captured accounts, tokens, cookies, or
production media URLs.

## Covered expectations

- Shared Suitable/Extract URL parser for the pinned episode forms with numeric
  `i` query IDs.
- Rejection of non-HTTP(S), userinfo, explicit ports, encoded separators/NULs,
  lookalike hosts, missing/duplicate/conflicting/non-numeric/oversized `i`,
  fragments, and show-only URLs without `i`.
- Bounded webpage fetch via `Transport.ReadPage`, context cancellation, and
  secret-safe categorized errors (401/403 auth, 404/410 unavailable; generic
  transport and unhandled HTTP statuses remain network-class without
  `ErrInvalidMetadata`).
- First matching `$kind=share` + `modelType=EpisodeLockup` model extraction with
  required title (reject oversized rather than truncate) and cleaned absolute
  HTTP(S) stream URL.
- Audio-only format metadata (`vcodec=none`) with protocol derived from the
  validated stream scheme and safe extension inference (no invented `acodec`).
- The pinned tracking-prefix/double-scheme cleanup is implemented once in the
  shared podcast URL helper and reused by Apple Podcasts, Acast, Simplecast,
  and the other feeder-backed podcast extractors.
- Explicit JSON nesting-depth validation before decoding root and model
  payloads; `<script>` open tags require a tag-name boundary (reject
  `scripture`/`scriptx` prefixes).

## Deliberate boundaries

- Public episode pages only; show catalog URLs without `i` remain unsupported.
- No browser impersonation, credentials, or new redirect transport capability.
- Primary-owned registry wiring, parity manifest entries, and supported-sites
  documentation are intentionally left for a follow-up integration change.
