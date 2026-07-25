# megaphone provenance

Pinned reference: `yt-dlp/yt-dlp@aefce1eea4d0b6bab1ec2bd3beff09bff91a39c8`

| Field | Value |
|-------|-------|
| Reference file | `yt_dlp/extractor/megaphone.py` |
| Reference class | `MegaphoneIE` |
| Reference function(s) | `_real_extract` |
| Go entry | `internal/extractor` constructors `New*` for key `megaphone` |

## Derived facts (copied from reference behavior)
- player.megaphone.fm/{id} HTML media URL scrape

## Fixture construction
- Synthetic, license-safe, secret-free: `page.html`
- No copyrighted media bytes committed.

## Go hardening / deliberate deviations
- strict media URL
- bounded page size
