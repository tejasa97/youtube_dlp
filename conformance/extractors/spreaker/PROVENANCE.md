# spreaker provenance

Pinned reference: `yt-dlp/yt-dlp@aefce1eea4d0b6bab1ec2bd3beff09bff91a39c8`

| Field | Value |
|-------|-------|
| Reference file | `yt_dlp/extractor/spreaker.py` |
| Reference class | `SpreakerIE` |
| Reference function(s) | `_real_extract / _extract_episode` |
| Go entry | `internal/extractor` constructors `New*` for key `spreaker` |

## Derived facts (copied from reference behavior)
- api.spreaker.com/episode|v2/episodes/{id}
- www.spreaker.com/episode/{slug-}{id}

## Fixture construction
- Synthetic, license-safe, secret-free: `episode.json`
- No copyrighted media bytes committed.

## Go hardening / deliberate deviations
- digit id
- download_url HTTPS policy
