# buzzfeed provenance

Pinned reference: `yt-dlp/yt-dlp@aefce1eea4d0b6bab1ec2bd3beff09bff91a39c8`

| Field | Value |
|-------|-------|
| Reference file | `yt_dlp/extractor/buzzfeed.py` |
| Reference class | `BuzzFeedIE` |
| Reference function(s) | `_real_extract` |
| Go entry | `NewBuzzFeed` |

## Explicit deviations
- Facebook bucket URLs are emitted as bare playlist entries (empty ExtractorKey). No Facebook extractor is registered; an explicit `facebook` key is never set. YouTube buckets use `ExtractorKey=youtube` with verified re-entry.
