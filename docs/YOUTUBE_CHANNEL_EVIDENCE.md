# YouTube channel-tab extractor evidence

Implementation provenance: pinned `yt-dlp/yt-dlp` checkout at
`aefce1eea4d0b6bab1ec2bd3beff09bff91a39c8`, `YoutubeTabIE` in
`yt_dlp/extractor/youtube/_tab.py`. The implementation follows its public
browse-tab model: an initial `ytInitialData` page yields ordered video URL
results and a continuation is sent to `youtubei/v1/browse`.

Implemented: exact `youtube.com`/`www.youtube.com` explicit
`/channel/<UCID>/{videos,shorts,streams,playlists,home,featured,community,releases,podcasts}`
routes, public initial metadata, mixed video/playlist home renderers,
community media attachments and inline video links, release/podcast
playlists, and compatible lockup entries; selected-tab validation; lazy,
reusable bounded continuations; cancellation, cursor-loop protection, and
authentication/unavailable/rate-limit/network classification.
The exact bare `/channel/<UCID>` route lazily aggregates advertised videos,
streams, and Shorts without admitting home shelves. When none are advertised,
it tries the equivalent `UU<channel-suffix>` uploads playlist and returns an
empty channel playlist if that derived playlist is unavailable.

Not implemented: channel search, membership or arbitrary custom tabs, mixes,
arbitrary renderer parity, authenticated/private success, or live-site
compatibility guarantees. Handle and legacy alias URL families are implemented
by their dedicated extractors.
