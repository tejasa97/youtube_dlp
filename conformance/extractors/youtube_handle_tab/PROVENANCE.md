# Synthetic public YouTube handle-tab corpus

These offline fixtures model the public `YoutubeTabIE` browse-tab shapes in
pinned `yt-dlp/yt-dlp@aefce1eea4d0b6bab1ec2bd3beff09bff91a39c8`, especially
`YoutubeTabIE._entries` and its continuation extraction in
`yt_dlp/extractor/youtube/_tab.py`.

All IDs, titles, API keys, visitor data, and continuation values are inert
synthetic values. They assert the exact public `/@handle/videos` route,
ordered video/Short URL entries, metadata extraction, and lazy reusable
`youtubei/v1/browse` continuation behavior without live access.
