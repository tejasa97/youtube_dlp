# Phase 1 TikTok impersonation pilot

## Scope and outcome

This pilot implements the pinned yt-dlp public-video webpage path for TikTok.
It proves that a platform extractor can require the explicit `chrome-133`
profile, consume embedded hydration metadata, normalize multiple protected
media URL shapes, and fail without silently falling back to the native
transport.

Supported URLs are public `www.tiktok.com/@user/video/id` and
`www.tiktok.com/embed/id` pages (the bare `tiktok.com` host is accepted too).
Input query parameters are not forwarded into the canonical webpage request,
which avoids carrying caller tokens into diagnostics or fixture behavior.

## Flow

1. Parse the numeric video ID and optional public username.
2. Canonicalize the URL as `https://www.tiktok.com/@user/video/id`.
3. Request the page through `ReadPageWithProfile` using exactly
   `chrome-133`. If the transport cannot provide that profile,
   `ErrTransportProfile` is returned and there is no native retry.
4. Extract a bounded `__UNIVERSAL_DATA_FOR_REHYDRATION__` JSON script.
5. Read `webapp.video-detail`, validate the requested ID, map TikTok status
   codes, and normalize bitrate, direct-play, watermarked-download, or
   slideshow-audio formats.
6. Return author, music, stats, thumbnail, referrer, and timestamp metadata in
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
secret-safe errors, cancellation, the hydration bound, and fuzz parsing.

The pilot is offline and deterministic. It does not contact TikTok and does not
invoke Python or the reference checkout.

## Known deviations

- TikTok's Android application API fallback, app-info rotation, device IDs,
  request signing, and `odin_tt`/`sid_tt` cookie forwarding are not implemented.
- The pinned reference's transient WAF proof-of-work cookie solver is not
  implemented. Challenge pages return a stable capability error.
- SIGI-state fallback, captions, image slideshows, availability labels, and
  full thumbnail collections are outside the pilot.
- TikTok's proprietary `bytevc2` codec is not specially deprioritized.
- No live canary is part of deterministic CI; current interoperability must be
  assessed separately under the repository's controlled-canary policy.
