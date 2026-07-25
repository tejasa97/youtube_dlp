# YouTube browse renderer parity evidence

Pinned reference: yt-dlp `aefce1eea4d0b6bab1ec2bd3beff09bff91a39c8`
(`yt_dlp/extractor/youtube/_tab.py`, `_search.py`).

## Shared renderer walker

Deterministic fixtures and unit tests cover video, grid-video, Shorts/reel,
playlist, show/grid-show, channel, lockup, Shorts lockup, shelf, hashtag, and
Music list renderers in initial and continuation payloads, including mixed
ordering and repeated occurrences. Hashtag tiles emit registered
`youtube_hashtag` URL results. Malformed/deep nesting is exercised by
`FuzzParseYouTubeRendererData`.

Video URL results may carry attributable `availability` labels
(`public`/`private`/`premium`/`subscriber_only`/`unlisted`) from badge
style/icon/label evidence with order-independent precedence
(`private > premium > subscriber_only > unlisted > public`). Unknown badges
are ignored; badge walk / parser-limit errors omit availability rather than
emitting a partial positive claim. Playlist and channel/hashtag playlist Info
may carry pre-fetch `playlist_count` / `view_count` when sidebar/header count
text is a single attributable integer or `k`/`m`/`b`/`kk` token (junk-separated
digits, bare decimals, and overflow fail closed).

## Custom tabs and channel search

Custom tabs are accepted only when advertised and bound to the requested
channel/handle/alias identity, including browseId equality against the resolved
UCID when present. Selected custom tabs require an attributable endpoint;
missing browse containers and title/tabIdentifier-only selections fail closed.
Cross-channel advertised endpoints are omitted from `channel_tabs`. Built-in
tabs keep their existing selected-tab checks. Channel-local search routes use
browse continuations with cancellation and lazy reuse. Conditional regional
redirects for `/search` preserve the validated `query`/`q` onto the destination
identity.

## Search / Music / auth

General search emits broader URL-result families under supported `sp` values
(normalized after `url.ParseQuery` decoding), including registered hashtag
results. Music search covers pinned
upstream sections and emits watch/playlist/channel URL results plus registered
Music browse families (`MPRE` albums, `MPSP` podcasts, `VL` playlists, and
`UC`/`MPLA` artists) that `youtube_music_browse` consumes. Unregistered Music
browse prefixes remain omitted so default playlist expansion
cannot fail. WEB_REMIX Music browse/search continuations are cookie-isolated
and never inherit WEB SID state; Music browse requires isolation before any
network call, including the initial page GET. Authenticated WEB browse/search
continuations require the redirect-disabled cookie transport and refuse
anonymous fallback after authenticated state is engaged. Browse continuations
rotate visitor data across pages; general search continuations reuse the
initial page/config visitor without a rotation claim. Authenticated or premium
Music browse/search success is not claimed and fails closed. Albums must
resolve to a canonical Music playlist identity or fail closed.
