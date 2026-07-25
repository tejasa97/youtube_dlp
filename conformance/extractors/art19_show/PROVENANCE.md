# art19_show provenance

Pinned reference: `yt-dlp/yt-dlp@aefce1eea4d0b6bab1ec2bd3beff09bff91a39c8`

| Field | Value |
|-------|-------|
| Reference file | `yt_dlp/extractor/art19.py` |
| Reference class | `Art19ShowIE` |
| Reference function(s) | `_real_extract` |
| Go entry | `internal/extractor` constructors `New*` for key `art19_show` |

## Derived facts (copied from reference behavior)
- GET art19.com/shows/{slug} Accept:application/json

## Fixture construction
- Synthetic, license-safe, secret-free: `show.json`
- No copyrighted media bytes committed.

## Go hardening / deliberate deviations
- LazyFirstPageEntries
- podcastMaxEpisodes
