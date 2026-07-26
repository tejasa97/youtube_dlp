# simplecast_episode provenance

Pinned reference: `yt-dlp/yt-dlp@aefce1eea4d0b6bab1ec2bd3beff09bff91a39c8`

| Field | Value |
|-------|-------|
| Reference file | `yt_dlp/extractor/simplecast.py` |
| Reference class | `SimplecastEpisodeIE` |
| Reference function(s) | `_real_extract` |
| Go entry | `internal/extractor` constructors `New*` for key `simplecast_episode` |

## Derived facts (copied from reference behavior)
- customer subdomain `/episodes/{slug}` → `episodes/search`
- the search response is a complete episode object parsed inline, without a
  second UUID lookup

## Fixture construction
- Synthetic, license-safe, secret-free: `search.json`
- No copyrighted media bytes committed.

## Go hardening / deliberate deviations
- exact customer host suffix .simplecast.com
- the response episode URL may only replace the request URL when it preserves
  the exact requested tenant and slug identity
- media and metadata URLs remain subject to the shared strict public-host policy
