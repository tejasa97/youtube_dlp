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

Not implemented: bare channel resolution, channel search, membership or
arbitrary custom tabs, mixes, arbitrary renderer parity, authenticated/private
success, or live-site compatibility guarantees. Handle and legacy alias URL
families are implemented by their dedicated registered extractors.
