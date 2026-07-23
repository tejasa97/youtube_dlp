# YouTube handle-tab extractor evidence

Implementation provenance: pinned `yt-dlp/yt-dlp` checkout at
`aefce1eea4d0b6bab1ec2bd3beff09bff91a39c8`, `YoutubeTabIE` in
`yt_dlp/extractor/youtube/_tab.py`. The implementation follows its public
browse-tab model: an initial `ytInitialData` page yields ordered video URL
results and a continuation is sent to `youtubei/v1/browse`.

Implemented: exact `youtube.com`/`www.youtube.com`
`/@handle/{videos,shorts,streams,playlists,home,featured,community,releases,podcasts}`
routes and the pinned reference's
Unicode-aware `@[\w.-]{3,30}` handle grammar. Raw and percent-encoded handles
are decoded once, validated by Unicode code point, and canonicalized without
case folding. Public metadata, mixed home/featured feeds, community media
attachments and inline links, release/podcast playlists, `videoRenderer`,
`gridVideoRenderer`, `reelItemRenderer`, and compatible lockup entries; lazy,
reusable bounded continuations; cancellation, cursor-loop protection, request
configuration propagation, and authentication/unavailable/rate-limit/network
classification. Playlist metadata uses a valid extracted UCID when present;
otherwise it uses the stable bounded `handle:@handle` ID.

Encoded separators, backslashes, NULs, invalid UTF-8, emoji, combining marks,
and non-HTTP URL forms remain rejected. This matches the pinned Python `\w`
grammar rather than broadening it to arbitrary Unicode grapheme clusters.

Not implemented: bare-handle resolution, channel search, membership or
arbitrary custom tabs, arbitrary renderer parity, authenticated/private
success, or live-site compatibility guarantees. The separately documented
legacy alias extractor covers bounded explicit `/user` and `/c` tabs.
