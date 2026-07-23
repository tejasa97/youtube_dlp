# Synthetic public YouTube handle-tab corpus

These offline fixtures model the public `YoutubeTabIE` browse-tab shapes in
pinned `yt-dlp/yt-dlp@aefce1eea4d0b6bab1ec2bd3beff09bff91a39c8`, especially
`YoutubeTabIE._rich_entries`, `_extract_lockup_view_model`, `_extract_entries`,
`_entries`, and continuation extraction in `yt_dlp/extractor/youtube/_tab.py`.

All IDs, titles, API keys, visitor data, and continuation values are inert
synthetic values. They assert the exact public `/@handle/videos` route,
ordered video/Short URL entries, metadata extraction, and lazy reusable
`youtubei/v1/browse` continuation behavior without live access.

`handle-playlists.html` and `handle-playlists-continuation.json` add the exact
Unicode-aware `/@handle/playlists` route. The route grammar and one-pass URL
decoding derive from `_YT_HANDLE_RE` and `handle_or_none` in
`yt_dlp/extractor/youtube/_base.py:599-615`. They structurally model legacy
`playlistRenderer`/`gridPlaylistRenderer`, modern playlist and podcast
`lockupViewModel`, both continuation action containers, and a repeated cursor.
A video-type lockup proves that playlist tabs do not relabel videos as
playlists. A repeated playlist occurrence is intentional: the pinned tab
iterator preserves renderer occurrence order, while the shared Go continuation
model de-duplicates cursors to terminate loops. No production page, account
identifier, signed URL, tracking value, or cookie was retained.
