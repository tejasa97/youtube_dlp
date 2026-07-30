# Vimeo authenticated unlisted-video evidence

Status: compatible for direct HTTPS Vimeo unlisted-share URLs of the form
`https://vimeo.com/{numeric-id}/{10-lowercase-hex-hash}` when the operation
cookie jar contains a valid `vimeo` session cookie.

The implementation follows the pinned web-client flow in
`yt-dlp/yt-dlp@aefce1eea4d0b6bab1ec2bd3beff09bff91a39c8` without copying the
reference's embedded non-web OAuth secrets:

1. Fetch an authenticated viewer JWT from the exact, no-redirect
   `https://vimeo.com/_next/viewer` endpoint. Only this request can receive
   the operation jar's `vimeo` cookie.
2. Fetch `api.vimeo.com/videos/{id}:{hash}` metadata with only the scoped JWT.
   A 401 or 403 invalidates the JWT and retries once; a terminal 401/403 is
   `ErrAuthentication`.
3. Require the API `uri` and credential-isolated player-config `video.id` to
   agree with the requested numeric ID before returning any media.
4. Preserve API description, license, created/release timestamps, plays,
   comments, and likes.
5. Follow the pinned logged-in original/source path through scoped
   `fields=privacy` and then `fields=download`, appending a valid `source`
   download as the preferred `quality=1` format.

The config request uses the credential-isolated no-redirect executor on exact
`player.vimeo.com`; config and emitted CDN URLs receive neither the cookie nor
the JWT. Caller query strings and fragments are accepted for upstream URL-shape
compatibility but stripped from the canonical page identity and every request
boundary.

`error_code` 5460 is accepted only as an unquoted JSON integer in one complete,
bounded JSON object. Quoted, floating-point, exponential, malformed,
oversized, and trailing-value forms do not trigger authentication
categorization.

## Evidence

| Contract | Evidence |
| --- | --- |
| Authenticated extraction, metadata, source format, query/fragment stripping, credential origins | `TestVimeoUnlistedExtractionSuccess` |
| Single refresh and terminal 401/403 mapping | `TestVimeoUnlistedAPI401RefreshesAndSucceeds`, `TestVimeoUnlistedPersistentAuthorizationFailureMapsToAuthentication` |
| API URI and player-config ID binding before media | `TestVimeoUnlistedRejectsAPIAndConfigIdentityMismatchBeforeMedia` |
| 5460 and other status taxonomy | `TestVimeoUnlistedAPI404WithErrorCode5460MapsToAuthentication`, `TestVimeoUnlistedAPI404Without5460MapsToUnavailable`, `TestVimeoUnlistedAPI410MapsToUnavailable` |
| Strict bounded error JSON | `TestMatchesVimeoUnlistedErrorCodeRequiresStrictIntegerAndJSONEOF`, `FuzzMatchesVimeoUnlistedErrorCode` |
| Route canonicalization invariants | `TestVimeoUnlistedRouteAcceptsCanonicalForm`, `FuzzClassifyVimeoUnlistedURL` |
| Synthetic fixture attribution | `conformance/extractors/vimeo/PROVENANCE.md` |

## Remaining deviations

Interactive Vimeo login, copied mobile/desktop OAuth client credentials,
non-share private URL discovery, uppercase or
non-10-character share hashes, HTTP input URLs, DRM, and live archives remain
unsupported. Original/source discovery deliberately uses only the scoped web
API `privacy`/`download` path: it does not use the cookie-bearing webpage
`load_download_config` fallback, does not issue a CDN HEAD request when a
source extension is absent, and does not expose upstream's
`original_format_policy=always` override. Source discovery failures other than
cancellation remain nonfatal, matching upstream's best-effort behavior. Live
service compatibility is not asserted from synthetic fixtures alone.

The public/player path additionally recognizes only the pinned fingerprint
contexts: `vimeo.com` page 403 and `player.vimeo.com` page 429 map to the
existing transport-profile category. This fixture-backed behavior is neither a
generic Vimeo rate-limit claim nor live-site coverage; `api.vimeo.com` 429
remains deliberately unresolved.
