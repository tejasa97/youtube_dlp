# nowness_playlist provenance

Pinned reference: `yt-dlp/yt-dlp@aefce1eea4d0b6bab1ec2bd3beff09bff91a39c8`

| Field | Value |
|-------|-------|
| Reference file | `yt_dlp/extractor/nowness.py` |
| Reference class | `NownessPlaylistIE` |
| Reference function(s) | `_real_extract` |
| Go entry | `internal/extractor` constructors `New*` for key `nowness_playlist` |

## Derived facts (copied from reference behavior)
- api post?PlaylistId=
- entries → nowness story URLResults

## Fixture construction
- Synthetic, license-safe, secret-free: `playlist.json`
- No copyrighted media bytes committed.

## Go hardening / deliberate deviations
- LazyFirstPageEntries
- nownessMaxEntries
- slug dedupe
