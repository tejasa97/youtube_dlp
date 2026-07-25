# dacast provenance

Pinned reference: `yt-dlp/yt-dlp@aefce1eea4d0b6bab1ec2bd3beff09bff91a39c8`

| Field | Value |
|-------|-------|
| Reference file | `yt_dlp/extractor/dacast.py` |
| Reference class | `DacastVODIE` |
| Reference function(s) | `_real_extract` |
| Go entry | `internal/extractor` constructors `New*` for key `dacast` |

## Derived facts (copied from reference behavior)
- iframe.dacast.com/vod/{user}/{id}
- playback.dacast.com content info+access
- HLS formats

## Fixture construction
- Synthetic, license-safe, secret-free: `info.json + access.json`
- No copyrighted media bytes committed.

## Go hardening / deliberate deviations
- strict HLS URL
- DRM_EXT rejected
- offline/blocked → ErrUnavailable
- USP AES signing not claimed
