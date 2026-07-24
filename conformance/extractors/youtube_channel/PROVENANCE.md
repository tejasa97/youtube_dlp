# Synthetic public channel-tab corpus

These offline fixtures model the public `YoutubeTabIE` browse-tab renderer and
continuation shapes from pinned
`yt-dlp/yt-dlp@aefce1eea4d0b6bab1ec2bd3beff09bff91a39c8`, specifically
`_rich_entries`, `_extract_lockup_view_model`, `_extract_entries`, `_entries`,
and `_extract_continuation` in `yt_dlp/extractor/youtube/_tab.py`.

All IDs, titles, API keys, visitor data, and continuation values are inert
synthetic values. The corpus asserts explicit `/channel/<UCID>/videos` routing,
ordered lazy continuation entries, and no live access.
The videos fixture now includes a decisive synthetic selected-tab identifier
so the shared mismatch guard is exercised without changing its renderer data.

`channel-playlists.html` and `channel-playlists-continuation.json` add the exact
public `/channel/<UCID>/playlists` route. They structurally model legacy
`playlistRenderer`/`gridPlaylistRenderer`, modern playlist and podcast
`lockupViewModel`, both continuation action containers, and a repeated cursor.
A video-type lockup proves that playlist tabs do not relabel videos as
playlists. A repeated playlist occurrence is intentional: the pinned tab
iterator preserves renderer occurrence order, while the shared Go continuation
model de-duplicates cursors to terminate loops. No production page, account
identifier, signed URL, tracking value, or cookie was retained.
