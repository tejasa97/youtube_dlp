# YouTube browse renderer parity evidence

Pinned reference: yt-dlp `aefce1eea4d0b6bab1ec2bd3beff09bff91a39c8`
(`yt_dlp/extractor/youtube/_tab.py`, `_search.py`).

## Shared renderer walker

Deterministic fixtures and unit tests cover video, grid-video, Shorts/reel,
playlist, channel, lockup, Shorts lockup, hashtag, shelf, and Music list
renderers in initial and continuation payloads, including mixed ordering and
repeated occurrences. Malformed/deep nesting is exercised by
`FuzzParseYouTubeRendererData`.

## Custom tabs and channel search

Custom tabs are accepted only when advertised and bound to the requested
channel/handle/alias identity. Cross-host, identity-swap, encoded-separator,
and conflicting selected-tab attacks fail closed. Channel-local search routes
use browse continuations with cancellation and lazy reuse.

## Search / Music / auth

General search emits broader URL-result families under supported `sp` values.
Music search covers pinned upstream sections; WEB_REMIX continuations never
inherit WEB SID state. Authenticated WEB browse/search continuations require
the redirect-disabled cookie transport and refuse anonymous fallback after
authenticated state is engaged.
EOF