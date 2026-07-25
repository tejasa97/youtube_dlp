# YouTube shared renderer fixtures

Synthetic browse/search renderer payloads in this directory (and the inline
fixtures in `internal/extractor/youtube_renderer_test.go`) are minimized
structures derived from the pinned yt-dlp reference commit
`aefce1eea4d0b6bab1ec2bd3beff09bff91a39c8`, specifically
`yt_dlp/extractor/youtube/_tab.py` renderer extraction
(`_extract_video`, `_extract_channel_renderer`, `_extract_lockup_view_model`,
`_hashtag_tile_entry`, `_rich_entries`, `_music_reponsive_list_entry`) and
continuation container shapes. Identifiers, titles, and tokens are invented;
no live response or credential is recorded.
