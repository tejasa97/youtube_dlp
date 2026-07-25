# laracasts provenance

Pinned reference: `yt-dlp/yt-dlp@aefce1eea4d0b6bab1ec2bd3beff09bff91a39c8`

| Field | Value |
|-------|-------|
| Reference file | `yt_dlp/extractor/laracasts.py` |
| Reference class | `LaracastsIE` |
| Reference function(s) | `_real_extract` / `_parse_episode` |
| Go entry | `NewLaracasts` |

## Explicit deviations
- Does not smuggle a `laracasts.com` Referer onto Vimeo player URLs; re-entry uses the registered Vimeo extractor's standard `vimeo.com/{id}` fetch.
