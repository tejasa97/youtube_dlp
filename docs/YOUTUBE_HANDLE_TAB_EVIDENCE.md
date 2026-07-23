# YouTube handle-tab extractor evidence

Implementation provenance: pinned `yt-dlp/yt-dlp` checkout at
`aefce1eea4d0b6bab1ec2bd3beff09bff91a39c8`, `YoutubeTabIE` in
`yt_dlp/extractor/youtube/_tab.py`. The implementation follows its public
browse-tab model: an initial `ytInitialData` page yields ordered video URL
results and a continuation is sent to `youtubei/v1/browse`.

Implemented: exact `youtube.com`/`www.youtube.com`
`/@handle/{videos,shorts,streams,playlists}` routes, a deliberately bounded ASCII handle
grammar (`@` + 3–30 ASCII letters/digits/dots/underscores/hyphens, containing
at least one alphanumeric), public metadata, `videoRenderer`,
`gridVideoRenderer`, `reelItemRenderer`, and compatible lockup entries; lazy,
reusable bounded continuations; cancellation, cursor-loop protection, request
configuration propagation, and authentication/unavailable/rate-limit/network
classification. Playlist metadata uses a valid extracted UCID when present;
otherwise it uses the stable bounded `handle:@lowercase-handle` ID.

Deviation: Unicode and other full YouTube handle forms are deliberately
rejected rather than partially normalized. This avoids claiming parity with
the reference's broader handle support.

Not implemented: handle home resolution, search/community/releases, arbitrary
renderer parity, authenticated/private success, or live-site compatibility
guarantees. The separately documented legacy alias extractor covers bounded
explicit `/user` and `/c` tabs. Registration places `NewYouTubeHandleTab()`
before `NewYouTube()`.
