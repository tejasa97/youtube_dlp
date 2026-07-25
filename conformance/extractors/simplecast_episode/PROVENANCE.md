# simplecast_episode provenance

Pinned reference: `yt-dlp/yt-dlp@aefce1eea4d0b6bab1ec2bd3beff09bff91a39c8`

| Field | Value |
|-------|-------|
| Reference file | `yt_dlp/extractor/simplecast.py` |
| Reference class | `SimplecastEpisodeIE` |
| Reference function(s) | `_real_extract` |
| Go entry | `internal/extractor` constructors `New*` for key `simplecast_episode` |

## Derived facts (copied from reference behavior)
- customer subdomain /episodes/{slug} → search API → simplecast URLResult

## Fixture construction
- Synthetic, license-safe, secret-free: `search.json`
- No copyrighted media bytes committed.

## Go hardening / deliberate deviations
- exact customer host suffix .simplecast.com
- transparent URLResult
