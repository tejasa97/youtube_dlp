# Discovery / DPlay fixture provenance

Reference: `yt-dlp/yt-dlp@aefce1eea4d0b6bab1ec2bd3beff09bff91a39c8`.

`internal/extractor/testdata/dplay/v3-playback.json` is a minimal synthetic response for `DiscoveryPlusBaseIE._download_video_playback_info` in `yt_dlp/extractor/dplay.py`. It preserves only the v3 `streaming` list shape (`type`, `url`); all hosts are synthetic and it contains no tokens, cookies, signed URLs, or account data.

`legacy-playback.json` derives from `DPlayBaseIE._download_video_playback_info`: its keyed `streaming` object is preserved without real endpoints. `master.m3u8` and `master.mpd` derive from the HLS/DASH branches of `DPlayBaseIE._get_disco_api_info`; they are minimal synthetic manifests exercising rendition and text-track discovery only.

`content.json` preserves the bounded `content/videos` data/included shape used
by `DPlayBaseIE._get_disco_api_info`. `error-geo.json` preserves only the
structured geo-error code shape used by the same method.

`india-show.json`, `italy-show.json`, and `show-page.json` preserve the
component/filter/season and paginated video-path fields read by
`DiscoveryPlusIndiaShowIE` and `DiscoveryPlusItalyShowIE`. They contain
synthetic IDs and paths.

`tele5.json` preserves the Aurora block `videoId` field read by `Tele5IE`.
`german.json` preserves the Loma `uid` and genre-taxonomy fields read by
`DiscoveryNetworksDeIE`. Every committed fixture is loaded by
`TestDiscoveryAllCommittedFixturesAreExercised`.
