# acast_channel provenance

Pinned reference: `yt-dlp/yt-dlp@aefce1eea4d0b6bab1ec2bd3beff09bff91a39c8`

| Field | Value |
|-------|-------|
| Reference file | `yt_dlp/extractor/acast.py` |
| Reference class | `ACastChannelIE` |
| Reference function(s) | `_real_extract` |
| Go entry | `internal/extractor` constructors `New*` for key `acast_channel` |

## Derived facts (copied from reference behavior)
- feeder show endpoint lists episodes
- channel hosts exclude episode paths

## Fixture construction
- Synthetic, license-safe, secret-free: `show.json synthetic episode list`
- No copyrighted media bytes committed.

## Go hardening / deliberate deviations
- LazyFirstPageEntries (no eager StaticEntries)
- podcastMaxEpisodes bound
- duplicate slug skip
