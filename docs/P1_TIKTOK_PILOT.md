# TikTok impersonation evidence

## Scope and outcome

This pilot implements the pinned yt-dlp public-video webpage path for TikTok.
It proves that a platform extractor can require the explicit `chrome-133`
profile, consume embedded hydration metadata, normalize multiple protected
media URL shapes, and fail without silently falling back to the native
transport.

Supported URLs are public `www.tiktok.com/@user/video/id` and
`www.tiktok.com/embed/id` pages (the bare `tiktok.com` host is accepted too),
plus short links on `vm.tiktok.com`, `vt.tiktok.com`, and `tiktok.com/t/*`
(including a valid trailing slash) that resolve to the canonical video page
before hydration extraction. Intermediate `m.tiktok.com` hops are accepted when
they remain short tokens or resolve to a canonical `/@user/video/id` path.
Input query parameters are not forwarded into the canonical webpage request,
which avoids carrying caller tokens into diagnostics or fixture behavior.

## Flow

1. For short links, issue bounded `HEAD` requests through
   `DoWithoutCredentialsNoRedirect` (not `DoWithoutCookies`, which follows
   redirects, and not `DoNoRedirect`, which may still attach jar cookies) with
   `User-Agent: facebookexternalhit/1.1`, validate each `Location` hop against
   an explicit TikTok host allowlist, and return a lazy URL result to the
   canonical `https://www.tiktok.com/@user/video/id` page.
2. Parse the numeric video ID and optional public username.
3. Canonicalize the URL as `https://www.tiktok.com/@user/video/id`.
4. Request the page through `ReadPageWithProfile` using exactly
   `chrome-133`. If the transport cannot provide that profile,
   `ErrTransportProfile` is returned and there is no native retry.
5. Extract a bounded `__UNIVERSAL_DATA_FOR_REHYDRATION__` JSON script.
6. Read `webapp.video-detail`, validate the requested ID, map TikTok status
   codes, and normalize bitrate, direct-play, watermarked-download, or
   slideshow-audio formats.
7. Return author, music, stats, thumbnail, referrer, and timestamp metadata in
   the shared ordered value model.

Hydration JSON is capped at the shared 16 MiB extractor JSON bound. JSON input,
cookies, signed URLs, and page bodies are never included in errors. Caller
cancellation is checked before transport, propagated through the profiled
request, and checked again before parsing.

## Categorized failures

- `10216` and `10222`, classified-content-without-video, login pages, and
  expired-session pages: `ErrAuthentication`.
- `10204` and other explicit unavailable status codes: `ErrUnavailable`.
- TikTok wait/WAF challenge pages: `ErrChallengeSolver`.
- Missing, malformed, trailing, mismatched-ID, or formatless hydration:
  `ErrInvalidMetadata`.
- Missing explicit browser-profile support: `ErrTransportProfile`.
- Missing credential-isolated no-redirect transport for short-link resolution:
  `ErrTransportIsolation`.
- Oversized hydration: `ErrJSONResponseTooLarge`.
- Cancellation: the original context error.

## Registry integration

The primary integrator must register `extractor.NewTikTok()` in the common
product registry before the generic extractor. No other shared integration is
required. Capability status should cite the deterministic tests and remain
scoped to the public hydration pilot corpus.

## Evidence

Fixtures and attribution are in `conformance/extractors/tiktok`. Automated
tests cover the protected success path, explicit profile/no-fallback behavior,
canonical embed handling, bitrate/direct/download formats, normalized metadata,
private/blocked/expired-session/challenge/malformed/formatless failures,
short-link redirect hop validation, loop/open-redirect rejection,
secret-safe errors, cancellation, the hydration bound, and fuzz parsing.

The pilot is offline and deterministic. It does not contact TikTok and does not
invoke Python or the reference checkout.

## Known deviations

- TikTok's Android application API fallback, app-info rotation, device IDs,
  request signing, and `odin_tt`/`sid_tt` cookie forwarding are not implemented.
- The pinned reference's transient WAF proof-of-work cookie solver is not
  implemented. Challenge pages return a stable capability error.
- SIGI-state fallback, image slideshows, availability labels, and full
  thumbnail collections are outside the pilot. Bounded public-webpage captions
  from hydration `video.subtitleInfos` are in scope (see
  `docs/TIKTOK_CAPTIONS_EVIDENCE.md`).
- TikTok's proprietary `bytevc2` codec is not specially deprioritized.
- No live canary is part of deterministic CI; current interoperability must be
  assessed separately under the repository's controlled-canary policy.
