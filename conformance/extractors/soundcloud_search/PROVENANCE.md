# SoundCloud search fixture provenance

These deterministic, synthetic, license-safe fixtures model the public
`api-v2.soundcloud.com/search/tracks` collection shape used by the pinned
yt-dlp reference at `aefce1eea4d0b6bab1ec2bd3beff09bff91a39c8`, specifically
`SoundcloudSearchIE._get_collection` and `_get_n_results` in
`yt_dlp/extractor/soundcloud.py`.

`home.html` and `client.js` model the bounded first-party client-ID discovery
performed by `SoundcloudBaseIE._update_client_id`. All IDs, text, URLs, and
cursors were independently authored. No production code reads this directory.

The native-Go slice accepts `scsearch:`, `scsearchN:`, and `scsearchall:`.
Unlike upstream's unbounded `all`, `scsearchall` is capped at 200 results and
requires exact HTTPS `api-v2.soundcloud.com/search/tracks` continuations.

`page1.json` contains a deliberately track-shaped `kind:"user"` object with a
canonical-looking permalink. It proves this corpus emits only explicit
`kind:"track"` results rather than inferring a track from its URL.
