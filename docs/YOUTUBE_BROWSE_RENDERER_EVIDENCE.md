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
channel/handle/alias identity, including browseId equality against the resolved
UCID when present. Selected custom tabs require an attributable endpoint;
missing browse containers and title/tabIdentifier-only selections fail closed.
Cross-channel advertised endpoints are omitted from `channel_tabs`. Built-in
tabs keep their existing selected-tab checks. Channel-local search routes use
browse continuations with cancellation and lazy reuse.

## Search / Music / auth

General search emits broader URL-result families under supported `sp` values.
Music search covers pinned upstream sections; WEB_REMIX continuations never
inherit WEB SID state. Authenticated WEB browse/search continuations require
the redirect-disabled cookie transport and refuse anonymous fallback after
authenticated state is engaged. Browse continuations rotate visitor data across
pages; general search continuations reuse the initial page/config visitor
without a rotation claim.
