# Vimeo public album and showcase evidence

Status: compatible for bounded anonymous public numeric Vimeo album and
showcase roots.

Pinned behavioral reference:
`yt-dlp/yt-dlp@aefce1eea4d0b6bab1ec2bd3beff09bff91a39c8`,
`yt_dlp/extractor/vimeo.py` `VimeoAlbumIE`.

## Supported scope

- `https://vimeo.com/album/{positive-numeric-id}`
- `https://vimeo.com/showcase/{positive-numeric-id}`

Extraction obtains an anonymous viewer application JWT, requests public album
metadata, and lazily pages the album videos API at 100 rows per page. Playlist
ID, title, description, and original album/showcase webpage identity are
preserved. Video entries are transparent canonical Vimeo URLs; malformed,
cross-origin, or link/URI-disagreeing rows are skipped, and duplicates retain
first occurrence order.

## Credential boundary

The viewer request requires credential-isolated, no-redirect transport. API
requests use a dedicated scoped-authorization transport that:

- retains only the extractor-generated JWT Authorization header;
- strips operation/default Authorization, Proxy-Authorization, and Cookie
  sources;
- disables the cookie jar and does not persist response cookies;
- returns the first redirect response without following it;
- redacts request URLs from transport failures.

Both capabilities are required before any request. The JWT is syntax/size and
integer-expiry validated, refreshed with a two-minute safety window and once
after an authorization rejection, held only for the extraction, and never
emitted in metadata, errors, fixtures as a functional credential, or persistent
state.

## Bounds and failure behavior

Pages are capped at 100 rows, 100 pages, and 10,000 unique entries. A short page
ends iteration; pinned HTTP 400 end behavior is accepted only after page one.
Empty/all-invalid playlists, oversized pages, malformed tokens or metadata,
unknown privacy, and exhausted bounds fail explicitly. Password/private/
unlisted albums fail as authentication-required.

Routes reject HTTP, credentials, ports, queries, fragments, trailing or extra
paths, encoded identifiers/separators, lookalike hosts, zero/non-numeric IDs,
and slug/embed forms before I/O.

## Evidence

| Requirement | Automated evidence |
| --- | --- |
| Scoped application authorization isolation | `TestDoWithScopedAuthorizationNoRedirect*` |
| Exact route and no-I/O hostile rejection | `TestVimeoAlbumRoutesAndUnsafeRejection` |
| Metadata, laziness, reuse, row validation | `TestVimeoAlbumPlaylistIsLazyReusableAndFiltersHostileRows` |
| Pagination order, dedupe, short/400 ending | `TestVimeoAlbumMultiPageOrderDedupeAndHTTP400End` |
| Expiry-aware JWT refresh across delayed/reused iteration | `TestVimeoAlbumRefreshesJWTForDelayedReusableIteration` |
| One-shot JWT refresh after authorization rejection | `TestVimeoAlbumRefreshesJWTOnceAfterAuthorizationRejection` |
| Capability, privacy, token, status, cancellation | `TestVimeoAlbumFailuresCapabilityAndCancellation` |
| Classifier round-trip and dispatch invariants | `FuzzClassifyVimeoAlbumURL` |
| Video row URL/URI identity invariants | `FuzzVimeoAlbumVideoEntry` |

Fixture provenance is recorded in
`conformance/extractors/vimeo/PROVENANCE.md`.

## Deliberate limits

Non-numeric showcase slugs, embeds, referrer propagation, password submission,
authenticated/private/unlisted albums, and live-service compatibility claims
remain out of scope. The fixture corpus establishes deterministic conformance;
the volatile viewer/API flow still requires an opt-in live canary before a live
compatibility claim.
