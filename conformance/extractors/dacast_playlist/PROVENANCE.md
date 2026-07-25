# dacast_playlist provenance

Pinned reference: `yt-dlp/yt-dlp@aefce1eea4d0b6bab1ec2bd3beff09bff91a39c8`

| Field | Value |
|-------|-------|
| Reference file | `yt_dlp/extractor/dacast.py` |
| Reference class | `DacastPlaylistIE` |
| Reference function(s) | `_real_extract` |
| Go entry | `internal/extractor` constructors `New*` for key `dacast_playlist` |

## Derived facts (copied from reference behavior)
- iframe.dacast.com/playlist/{user}/{id}
- contentInfo.features.playlist.contents → dacast VOD URLResults

## Fixture construction
- Synthetic, license-safe, secret-free: `info.json`
- No copyrighted media bytes committed.

## Go hardening / deliberate deviations
- LazyFirstPageEntries
- contentId user-vod-id parse
- duplicate id skip
