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

`request.text_tracks` models the public manual-caption list handled by
`VimeoBaseIE._parse_config` in the pinned reference (lines 325-328). Relative
and protocol-relative tracks intentionally resolve to `player.vimeo.com`; all
track data and query tokens are synthetic.

The config fixture's `player.vimeo.com` endpoint is likewise deliberate: the
extractor requests only that HTTPS origin and retains its synthetic query token
while using a canonical token-free Vimeo Referer.
