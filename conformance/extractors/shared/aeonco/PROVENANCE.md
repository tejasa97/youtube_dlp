# Aeon.co fixture provenance

Reference baseline: `yt-dlp/yt-dlp@aefce1eea4d0b6bab1ec2bd3beff09bff91a39c8`.

The behavioral reference is `yt_dlp/extractor/aeonco.py` at that pinned commit.
The upstream extractor recognizes Aeon `/videos/{slug}` pages, walks parsed
JSON-LD for the first `VideoObject.embedUrl`, returns a URL handoff, and applies
`https://aeon.co/` as the Vimeo embedding referrer. The Go adapter intentionally
narrows routing to exact HTTPS `aeon.co` and `www.aeon.co` hosts and limits
handoffs to the existing Vimeo and YouTube extractors.

All HTML in this directory is synthetic and authored for this repository. No
upstream page body, copyrighted media, credential, signed URL, or user data is
copied into the fixtures.

| Fixture | Synthetic behavior covered |
| --- | --- |
| `vimeo_page.html` | `VideoObject.embedUrl` handoff to Vimeo with the Aeon referrer |
| `youtube_page.html` | `VideoObject.embedUrl` handoff to YouTube without a referrer override |
| `mixed_jsonld_page.html` | malformed JSON-LD, authoritative `contentUrl`, AudioObject exclusion, non-Aeon provider exclusion, and first-supported-video ordering |
| `no_embed_page.html` | a valid JSON-LD page without a VideoObject embed |
| `hostile_embed_page.html` | JavaScript, userinfo-bearing, and unsupported-host embed rejection without reflecting hostile values |

Runtime and build-time behavior is native Go and does not access or execute the
reference Python checkout.
