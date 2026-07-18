# Vimeo extractor pilot corpus

This synthetic offline corpus follows `_parse_config` and the primary Vimeo
webpage flow in the pinned reference
`yt-dlp/yt-dlp@aefce1eea4d0b6bab1ec2bd3beff09bff91a39c8`: an impersonated webpage
request, HTML-escaped `data-config-url`, progressive files, sorted CDN-backed
HLS/DASH manifests, owner metadata, numeric thumbnails, and live-event status
mapping. The DASH `master.json` to `master.mpd` normalization is also derived
from that implementation.

Every URL uses reserved `.example` data except the synthetic input URL. The
token, identifiers, title, owner, media metadata, and bytes are invented. Tests
use an in-memory profile-aware transport and make no Vimeo or media request.
