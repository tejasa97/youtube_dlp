# libsyn provenance

Pinned reference: `yt-dlp/yt-dlp@aefce1eea4d0b6bab1ec2bd3beff09bff91a39c8`

| Field | Value |
|-------|-------|
| Reference file | `yt_dlp/extractor/libsyn.py` |
| Reference class | `LibsynIE` |
| Reference function(s) | `_real_extract` |
| Go entry | `internal/extractor` constructors `New*` for key `libsyn` |

## Derived facts (copied from reference behavior)
- html5-player.libsyn.com/embed/episode/id/{digits}

## Fixture construction
- Synthetic, license-safe, secret-free: `page.html`
- No copyrighted media bytes committed.

## Go hardening / deliberate deviations
- digit episode id
- HTTPS media
