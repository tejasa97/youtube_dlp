# nowness provenance

Pinned reference: `yt-dlp/yt-dlp@aefce1eea4d0b6bab1ec2bd3beff09bff91a39c8`

| Field | Value |
|-------|-------|
| Reference file | `yt_dlp/extractor/nowness.py` |
| Reference class | `NownessIE` |
| Reference function(s) | `_real_extract / NownessBaseIE._extract_url_result / _api_request` |
| Go entry | `internal/extractor` constructors `New*` for key `nowness` |

## Derived facts (copied from reference behavior)
- api.nowness.com/api/post/getBySlug/{slug}
- iframe Brightcove player extraction
- Vimeo handoff supported; YouTube skipped

## Fixture construction
- Synthetic, license-safe, secret-free: `post.json + iframe.html`
- No copyrighted media bytes committed.

## Go hardening / deliberate deviations
- HTTPS API (reference used http)
- YouTube sources skipped deliberately
- iframe size bound
