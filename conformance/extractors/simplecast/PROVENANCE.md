# simplecast provenance

Pinned reference: `yt-dlp/yt-dlp@aefce1eea4d0b6bab1ec2bd3beff09bff91a39c8`

| Field | Value |
|-------|-------|
| Reference file | `yt_dlp/extractor/simplecast.py` |
| Reference class | `SimplecastIE` |
| Reference function(s) | `_real_extract / SimplecastBaseIE` |
| Go entry | `internal/extractor` constructors `New*` for key `simplecast` |

## Derived facts (copied from reference behavior)
- api.simplecast.com/episodes/{uuid}
- player.simplecast.com/{uuid}

## Fixture construction
- Synthetic, license-safe, secret-free: `episode.json`
- No copyrighted media bytes committed.

## Go hardening / deliberate deviations
- UUID path validation
- HTTPS media policy
