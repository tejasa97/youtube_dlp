# art19 provenance

Pinned reference: `yt-dlp/yt-dlp@aefce1eea4d0b6bab1ec2bd3beff09bff91a39c8`

| Field | Value |
|-------|-------|
| Reference file | `yt_dlp/extractor/art19.py` |
| Reference class | `Art19IE` |
| Reference function(s) | `_real_extract` |
| Go entry | `internal/extractor` constructors `New*` for key `art19` |

## Derived facts (copied from reference behavior)
- rss.art19.com/episodes/{uuid}.mp3 and art19.com show episode paths
- JSON episode metadata

## Fixture construction
- Synthetic, license-safe, secret-free: `episode.json`
- No copyrighted media bytes committed.

## Go hardening / deliberate deviations
- UUID validation
- HTTPS enclosure
