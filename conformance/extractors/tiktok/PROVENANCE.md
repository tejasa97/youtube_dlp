# TikTok pilot fixture provenance

- Reference checkout: `/Users/tejas/projects/yt-dlp-reference`
- Reference commit: `aefce1eea4d0b6bab1ec2bd3beff09bff91a39c8`
- Reference file: `yt_dlp/extractor/tiktok.py`
- Relevant reference behavior: `TikTokIE._real_extract`,
  `_extract_web_data_and_status`, `_extract_web_formats`, and
  `_parse_aweme_video_web` at the pinned commit. Caption field provenance is
  `TikTokIE._get_subtitles`, specifically public webpage `video.subtitleInfos`.

The fixture was authored and sanitized for this Go port. It is not a captured
TikTok response and contains no real cookie, signature, account identifier,
media, or private URL. Field names and status expectations were derived from
the pinned implementation: public status `0`, private statuses `10216` and
`10222`, IP-block status `10204`, hydration under
`__UNIVERSAL_DATA_FOR_REHYDRATION__`, bitrate/play/download address shapes,
and normalized author/stat/music metadata.

The caption entries are likewise sanitized examples of the public-webpage
shape: `Url`, `LanguageCodeName`, `LanguageName`, and `Format`. They exercise
language normalization, format inference, duplicate removal, and preservation
of an inert signed-query placeholder without containing a real signature.

The `token=redacted` query is inert fixture text used to ensure signed media
URLs survive metadata normalization without placing a real secret in source.
