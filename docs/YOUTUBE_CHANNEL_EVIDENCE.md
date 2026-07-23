# YouTube channel-tab extractor evidence

Implementation provenance: pinned `yt-dlp/yt-dlp` checkout at
`aefce1eea4d0b6bab1ec2bd3beff09bff91a39c8`, `YoutubeTabIE` in
`yt_dlp/extractor/youtube/_tab.py`. The implementation follows its public
browse-tab model: an initial `ytInitialData` page yields ordered video URL
results and a continuation is sent to `youtubei/v1/browse`.

Implemented: exact `youtube.com`/`www.youtube.com` explicit
`/channel/<UCID>/{videos,shorts,streams}` routes, public initial metadata,
`videoRenderer`, `gridVideoRenderer`, and compatible lockup entries; lazy,
reusable bounded continuations; cancellation, cursor-loop protection, and
authentication/unavailable/rate-limit/network classification.

Not implemented: channel home resolution, handles, search, playlist/release
tabs, mixes, comments, arbitrary renderer parity, authenticated/private
success, or live-site compatibility guarantees. Registration is intentionally
left to the integration owner; register `NewYouTubeChannelTab()` before
`NewYouTube()`.
