# Shared hosting extractor fixtures

These deterministic, synthetic protocol fixtures were authored for the native
Go extractor tests on 2026-07-18. They model public JSON/embed payload shapes
documented by Brightcove Player/Playback, Kaltura multirequest, JW Player v2,
Wistia Embed, and SproutVideo Embed APIs. They contain no production media,
cookies, account IDs, policy keys, or valid signatures.

Known deviations from pinned yt-dlp `aefce1eea4d0b6bab1ec2bd3beff09bff91a39c8`:

- Brightcove legacy Flash `/services` routes and policy-key JavaScript fallback
  are not implemented; current Player `config.json` is required.
- Kaltura accepts its opaque `kaltura:` URLs for direct extraction, but the
  current product registry only selects HTTP(S) roots. Embed HTTP(S) routes are
  fully supported; opaque lazy entries require registry opaque-scheme support.
- Wistia password submission/channel HTML fallback and SproutVideo password
  submission/Vids.io wrappers are intentionally omitted because this extractor
  contract has no video-password option or cookie UI flow.
- HLS/DASH manifests are exposed as usable native format URLs; variant parsing
  remains owned by the shared media pipeline rather than duplicated here.
