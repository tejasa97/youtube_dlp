# Vimeo public album and showcase evidence

Status: compatible for bounded anonymous public numeric and safe-slug Vimeo
album and showcase roots.

Pinned behavioral reference:
`yt-dlp/yt-dlp@aefce1eea4d0b6bab1ec2bd3beff09bff91a39c8`,
`yt_dlp/extractor/vimeo.py` `VimeoAlbumIE`.

## Supported scope

- `https://vimeo.com/album/{positive-numeric-id}`
- `https://vimeo.com/showcase/{positive-numeric-id}`
- `https://vimeo.com/album/{safe-public-slug}`
- `https://vimeo.com/showcase/{safe-public-slug}`
- `https://vimeo.com/album/{id}/embed` and `/embed2`
- `https://vimeo.com/showcase/{id}/embed` and `/embed2`

Safe slugs are first resolved through Vimeo's exact anonymous showcase
resolver endpoint. Extraction then obtains an anonymous viewer application
JWT, requests public album metadata, and lazily pages the album videos API at
100 rows per page. The resolved positive numeric ID, title, description, and
original slug or numeric webpage identity are preserved. Video entries are
transparent canonical Vimeo URLs; malformed, cross-origin, or
link/URI-disagreeing rows are skipped, and duplicates retain first occurrence
order.

## Credential boundary

The slug resolver and viewer requests require credential-isolated, no-redirect
transport. The resolver is an exact locally constructed HTTPS URL, sends
`X-Requested-With: XMLHttpRequest`, and accepts a positive integer identity
from bounded JSON on the pinned 200/401/403 statuses. API requests use a
dedicated scoped-authorization transport that:

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
paths, encoded identifiers/separators, lookalike hosts, zero/overflow numeric
IDs, unsafe/non-ASCII/oversized slugs, and hostile embed/query forms before I/O.
Supported `/embed` and `/embed2` suffixes are accepted when the base album or
showcase identity is otherwise valid.

## Evidence

| Requirement | Automated evidence |
| --- | --- |
| Scoped application authorization isolation | `TestDoWithScopedAuthorizationNoRedirect*` |
| Exact route and no-I/O hostile rejection | `TestVimeoAlbumRoutesAndUnsafeRejection` |
| Slug resolution and requested-identity preservation | `TestVimeoAlbumSlugResolutionPreservesRequestedIdentity` |
| Resolver statuses, strict identity JSON, bounds, and categorization | `TestVimeoAlbumSlugResolverAcceptedStatuses`, `TestVimeoAlbumSlugResolverFailuresAreCategorized` |
| Metadata, laziness, reuse, row validation | `TestVimeoAlbumPlaylistIsLazyReusableAndFiltersHostileRows` |
| Pagination order, dedupe, short/400 ending | `TestVimeoAlbumMultiPageOrderDedupeAndHTTP400End` |
| Expiry-aware JWT refresh across delayed/reused iteration | `TestVimeoAlbumRefreshesJWTForDelayedReusableIteration` |
| One-shot JWT refresh after authorization rejection | `TestVimeoAlbumRefreshesJWTOnceAfterAuthorizationRejection` |
| Capability, privacy, token, status, cancellation | `TestVimeoAlbumFailuresCapabilityAndCancellation` |
| Embed routes, referrer propagation, hostile referer rejection | `TestVimeoAlbumEmbedRoutesAccepted`, `TestVimeoAlbumEmbedPropagatesRefererToPlayerEntries`, `TestVimeoAlbumEmbedRejectsHostileReferer` |
| Classifier round-trip and dispatch invariants | `FuzzClassifyVimeoAlbumURL` |
| Resolved numeric identity invariants | `FuzzParseVimeoAlbumSlugID` |
| Video row URL/URI identity invariants | `FuzzVimeoAlbumVideoEntry` |

Fixture provenance is recorded in
`conformance/extractors/vimeo/PROVENANCE.md`.

## Deliberate limits

Vimeo slugs outside the deliberately narrow ASCII letter/digit/underscore/
hyphen grammar, password submission, authenticated/private/unlisted albums, and
live-service compatibility claims remain out of scope. Embed albums that require
a validated embedding-page Referer fail closed when none is supplied. The fixture
corpus establishes deterministic conformance; the volatile resolver/viewer/API
flow still requires an opt-in live canary before a live compatibility claim.
