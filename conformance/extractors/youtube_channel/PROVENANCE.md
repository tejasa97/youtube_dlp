# Synthetic public channel-tab corpus

These offline fixtures model the public `YoutubeTabIE` browse-tab renderer and
continuation shapes from pinned `yt-dlp/yt-dlp@aefce1eea4d0b6bab1ec2bd3beff09bff91a39c8`, specifically `_entries` and `_extract_continuation` in `yt_dlp/extractor/youtube/_tab.py`.

All IDs, titles, API keys, visitor data, and continuation values are inert
synthetic values. The corpus asserts explicit `/channel/<UCID>/videos` routing,
ordered lazy continuation entries, and no live access.
