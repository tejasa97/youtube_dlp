# Native YouTube PO-token evidence

The implementation is derived from the typed context and placement behavior in
the pinned read-only reference `yt-dlp/yt-dlp@aefce1eea4d0b6bab1ec2bd3beff09bff91a39c8`:

- `yt_dlp/extractor/youtube/pot/provider.py`
- `yt_dlp/extractor/youtube/pot/_director.py`
- `yt_dlp/extractor/youtube/pot/cache.py`
- `yt_dlp/extractor/youtube/_base.py`
- `yt_dlp/extractor/youtube/_video.py`

The Go product contains no generator copied from or delegated to Python. An
embedding application explicitly supplies one or more native Go providers.
Requests contain bounded binding metadata; responses must be strict base64url
tokens with bounded expiry. Provider count and the process-local LRU cache are
bounded, and cache keys are SHA-256 hashes of the binding fields.

`player` tokens are sent in `serviceIntegrityDimensions.poToken`. `gvs` tokens
are appended as the `pot` media query parameter or the `/pot/TOKEN` manifest
path component. Provider failures and panics are reduced to categorized errors,
and request/response formatting redacts their contents. Context cancellation is
propagated when a provider returns it.

## Explicit deviations

- There is no built-in WebPO generator, implicit network endpoint, executable,
  or upstream external-provider protocol.
- The cache is process-local; persistent provider caches and provider scoring
  are not implemented.
- Providers are trusted in-process Go code and must honor context cancellation;
  Go cannot forcibly terminate a blocked provider goroutine.
- Go strings cannot guarantee zeroization. Tokens are therefore kept out of
  errors and events, but exist in provider responses, request URLs, and cache
  entries as required for playback.
- Direct SABR/UMP, protected subtitles, authenticated Innertube breadth, and
  renderer breadth remain later waves.

Deterministic fixtures use inert synthetic tokens and `.example` media URLs.
No live token, credential, visitor identity, or captured production response is
stored.
