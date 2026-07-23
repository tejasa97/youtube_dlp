# YouTube expanded public-tab fixture provenance

These deterministic fixtures were authored for the Go port from the
read-only yt-dlp reference checkout at commit
`aefce1eea4d0b6bab1ec2bd3beff09bff91a39c8`.

The route and selected-tab expectations derive from
`yt_dlp/extractor/youtube/_tab.py:2207-2238` and
`yt_dlp/extractor/youtube/_tab.py:2267-2348`. Mixed rich-grid video and
playlist behavior derives from `_rich_entries` at lines 400-444. Community
attachment ordering, duplicate suppression, inline video links, and
continuation shapes derive from `_post_thread_entries`,
`_post_thread_continuation_entries`, `_extract_entries`, and `_entries` at
lines 448-640. Releases and podcasts use the playlist and lockup renderer
behavior represented by the pinned tests around lines 2050-2100.

All service identifiers, titles, tokens, API keys, visitor identities, URLs,
and payloads are synthetic. No live response, account data, credential,
cookie, token, or copyrighted media is included. The fixtures exercise:

- mixed home-tab video and playlist renderers;
- community video/playlist attachments and inline YouTube video links;
- attachment/inline duplicate suppression, cross-post occurrence retention,
  and hostile-host rejection;
- release/podcast playlist renderers with video lockups ignored;
- selected-tab identity validation;
- action, endpoint, and continuation-container pagination shapes;
- visitor-data rotation and bounded continuation behavior.

The reference checkout is evidence only and is never used at build time or
runtime.
