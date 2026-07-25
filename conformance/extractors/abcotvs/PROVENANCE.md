# abcotvs provenance

Pinned reference: `yt-dlp/yt-dlp@aefce1eea4d0b6bab1ec2bd3beff09bff91a39c8`

| Field | Value |
|-------|-------|
| Reference file | `yt_dlp/extractor/abcotvs.py` |
| Reference class | `ABCOTVSIE` |
| Reference function(s) | `_real_extract` |
| Go entry | `NewABCOTVS` |

## Explicit deviations
- Does not implement reference `publishedKey` media-id fallback or full metadata parity; uses OTV API video/`id` fields and URL id fallback only.
