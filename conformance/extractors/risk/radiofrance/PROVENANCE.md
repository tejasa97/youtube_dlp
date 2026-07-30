# Radio France public family fixtures

Synthetic fixtures authored on 2026-07-30 from `yt_dlp/extractor/radiofrance.py`
at upstream commit `aefce1eea4d0b6bab1ec2bd3beff09bff91a39c8`. Page HTML, API JSON,
stream URLs, thumbnails, and cursor tokens are invented. No live credentials,
signed production URLs, or response bodies appear in extractor diagnostics.

## Bounded media and thumbnail host policy

Production code accepts only attributable Radio France infrastructure hosts:

- `radiofrance.fr` and `*.radiofrance.fr` (for example `www.radiofrance.fr`,
  `icecast.radiofrance.fr`, `audio-mp3.radiofrance.fr`, `static.radiofrance.fr`)
- `maison.radiofrance.fr` and `*.maison.radiofrance.fr` for legacy pages

Upstream yt-dlp does not enforce this allowlist; URLs are taken from API and page
responses. The Go implementation adds defense-in-depth attribution so fixture-only
or third-party hosts cannot pass validation. Fixtures use production-shaped hostnames
that tests intercept via a registered `RoundTripper`; they are not live endpoints.

## Known deviations

- Live HLS emission uses the generic native `m3u8_native` product path without
  extractor-time manifest parsing; live HLS subtitle tracks are outside this
  fixture-backed scope unless separately declared by API direct sources.
- Parity status is **partial**: product evidence covers credential-isolated direct
  audio, deterministic HLS manifest plus segment download, schedule `series_id` /
  `series` overlay, and bounded schedule query validation; it does not claim
  unbounded upstream equivalence.
