# nowness_series provenance

Pinned reference: `yt-dlp/yt-dlp@aefce1eea4d0b6bab1ec2bd3beff09bff91a39c8`

| Field | Value |
|-------|-------|
| Reference file | `yt_dlp/extractor/nowness.py` |
| Reference class | `NownessSeriesIE` |
| Reference function(s) | `_real_extract` |
| Go entry | `internal/extractor` constructors `New*` for key `nowness_series` |

## Derived facts (copied from reference behavior)
- api series/getBySlug/{slug}

## Fixture construction
- Synthetic, license-safe, secret-free: `series.json`
- No copyrighted media bytes committed.

## Go hardening / deliberate deviations
- LazyFirstPageEntries
- does not steal /series/x/y story paths
