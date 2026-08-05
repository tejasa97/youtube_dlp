# YouTube Clip fixture

This corpus is a synthetic, offline fixture for `/clip/<id>` transparent
re-entry (YouTube Clips). It contains only the structural data needed to
resolve a clip page to its source video and loop-section timing:

- `clip.html` — a synthetic `ytInitialData` payload carrying
  `currentVideoEndpoint.watchEndpoint.videoId` and the deep
  `engagementPanels → … → loopCommand → startTimeMs/endTimeMs` chain;
- `expected.json` — the overlaid clip identity (clip id wins over the source
  video id; `media_type: clip`; `section_start`/`section_end` in seconds;
  https-priority `_format_sort_fields`).

The shapes and semantics follow the pinned reference
`yt-dlp/yt-dlp@aefce1eea4d0b6bab1ec2bd3beff09bff91a39c8`:

- clip URL and clip-id grammar: `yt_dlp/extractor/youtube/_clip.py:1-11`;
- source video id and loop timing traversal:
  `yt_dlp/extractor/youtube/_clip.py:28-45`;
- url_transparent semantics, clip-id precedence, section fields, and the
  https-priority format sort: `yt_dlp/extractor/youtube/_clip.py:47-54` and
  `yt_dlp/YoutubeDL.py:1978-1991`.

All identifiers, metadata, and URLs are artificial; no production response,
cookie, token, signed URL, or account data is retained. The `missing-video-id`
case asserts the pinned “Unable to find video ID” fail-closed behavior.
