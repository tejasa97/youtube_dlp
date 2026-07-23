# YouTube bare-channel upload fixture provenance

These deterministic fixtures were authored for the Go port from the
read-only yt-dlp reference checkout at commit
`aefce1eea4d0b6bab1ec2bd3beff09bff91a39c8`.

Bare-channel URL rewriting and videos/streams/Shorts aggregation derive from
`YoutubeTabIE._real_extract` in
`yt_dlp/extractor/youtube/_tab.py:2267-2350`, especially its bare-channel
redirect to `/videos`, advertised-tab discovery, fixed streams-then-Shorts
extension order, and no-videos fallback. Renderer and continuation structures
derive from `_rich_entries`, `_extract_entries`, and `_entries` at lines
400-640.

All channel IDs, video IDs, titles, API keys, visitor identities, cursors, and
URLs are synthetic. No live page, account data, cookie, credential, signed
media URL, or copyrighted media is included. The corpus proves:

- videos, then streams, then Shorts upload ordering;
- lazy reusable continuation and tab loading;
- visitor-data propagation through the existing continuation path;
- exclusion of home-page shelf entries;
- channels with streams/Shorts but no videos tab;
- empty channels;
- exact channel, Unicode-handle, and legacy-alias root integration.

The pinned checkout is evidence only and is not used by production, build, or
test execution.
