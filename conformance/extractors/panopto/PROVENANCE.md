# panopto provenance

Pinned reference: `yt-dlp/yt-dlp@aefce1eea4d0b6bab1ec2bd3beff09bff91a39c8`

| Field | Value |
|-------|-------|
| Reference file | `yt_dlp/extractor/panopto.py` |
| Reference class | `PanoptoIE` |
| Reference function(s) | `_real_extract` / `_call_api` / `_extract_streams_formats_and_subtitles` |
| Go entry | `NewPanopto` |

## Derived facts
- Viewer/Embed.aspx with `id=` UUID on `*.panopto.com` / `*.panopto.eu`
- DeliveryInfo JSON streams → HTTPS media URLs

## Fixture construction
- Synthetic `deliveryinfo.json` only

## Go hardening
- Tenant subdomain policy (reject apex / suffix confusable)
- strict HTTPS media URLs
- bounded stream count
