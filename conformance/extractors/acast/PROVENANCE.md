# acast provenance

Pinned reference: `yt-dlp/yt-dlp@aefce1eea4d0b6bab1ec2bd3beff09bff91a39c8`

| Field | Value |
|-------|-------|
| Reference file | `yt_dlp/extractor/acast.py` |
| Reference class | `ACastIE` |
| Reference function(s) | `_real_extract / ACastBaseIE._extract_episode / _call_api` |
| Go entry | `internal/extractor` constructors `New*` for key `acast` |

## Derived facts (copied from reference behavior)
- feeder.acast.com/api/v1/shows/{channel}/episodes/{episode}?showInfo=true
- hosts: shows/www/embed/play.acast.com
- media URL from episode.url through the pinned `clean_podcast_url` prefix
  cleanup rules

## Fixture construction
- Synthetic, license-safe, secret-free: `episode.json synthetic feeder payload`
  with a nested Podtrac/Chartable prefix chain
- No copyrighted media bytes committed.

## Go hardening / deliberate deviations
- HTTPS-only feeder
- reject userinfo/unsafe ports
- bound description/title
- secret-safe errors
