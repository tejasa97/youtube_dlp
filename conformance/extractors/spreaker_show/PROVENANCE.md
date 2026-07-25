# spreaker_show provenance

Pinned reference: `yt-dlp/yt-dlp@aefce1eea4d0b6bab1ec2bd3beff09bff91a39c8`

| Field | Value |
|-------|-------|
| Reference file | `yt_dlp/extractor/spreaker.py` |
| Reference class | `SpreakerShowIE` |
| Reference function(s) | `_real_extract` |
| Go entry | `internal/extractor` constructors `New*` for key `spreaker_show` |

## Derived facts (copied from reference behavior)
- api.spreaker.com/show/{id}/episodes?page=&max_per_page=
- www.spreaker.com/podcast/{slug}--{id}

## Fixture construction
- Synthetic, license-safe, secret-free: `episodes.json`
- No copyrighted media bytes committed.

## Go hardening / deliberate deviations
- OnDemandEntries page iteration
- hostile next_url rejection
- empty first page → ErrInvalidMetadata
