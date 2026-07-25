# simplecast_podcast provenance

Pinned reference: `yt-dlp/yt-dlp@aefce1eea4d0b6bab1ec2bd3beff09bff91a39c8`

| Field | Value |
|-------|-------|
| Reference file | `yt_dlp/extractor/simplecast.py` |
| Reference class | `SimplecastPodcastIE` |
| Reference function(s) | `_real_extract` |
| Go entry | `internal/extractor` constructors `New*` for key `simplecast_podcast` |

## Derived facts (copied from reference behavior)
- POST podcasts/search then GET /podcasts/{id}/episodes

## Fixture construction
- Synthetic, license-safe, secret-free: `search.json + episodes.json`
- No copyrighted media bytes committed.

## Go hardening / deliberate deviations
- LazyFirstPageEntries defers both network calls
- podcastMaxEpisodes
