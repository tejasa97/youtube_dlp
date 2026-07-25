# SVT Play series playlist provenance

The deterministic series fixtures model the bounded public playlist behavior in
the pinned read-only yt-dlp checkout:

- repository: `yt-dlp/yt-dlp`
- commit: `aefce1eea4d0b6bab1ec2bd3beff09bff91a39c8`
- source: `yt_dlp/extractor/svt.py`
- reference class: `SVTSeriesIE`
- reference method: `_real_extract`
- public API shape: `https://api.svt.se/contento/graphql` with a
  `listablesBySlug` query keyed by the series slug

`series.json` preserves the reference response fields for series id/name,
long/short descriptions, associated season id/name, and ordered episode
`videoSvtId` values. Duplicate and malformed episode rows are synthetic test
inputs; the Go extractor filters them deterministically.

## Go hardening and deliberate deviations

- Series roots are accepted only on `https://www.svtplay.se/{slug}` and
  `https://svtplay.se/{slug}` with an optional single `tab` query value. HTTP is
  retained for legacy video paths only. Playlist `webpage_url` values are
  reconstructed canonically from the validated slug and tab.
- Slugs, season tab ids, season counts, per-season item counts, response
  metadata field lengths, and total playlist entries are bounded with named
  constants. Oversized responses fail closed with `ErrPlaylistLimit` or
  `ErrInvalidMetadata`.
- The GraphQL slug is embedded via `json.Marshal` rather than raw string
  interpolation to prevent query injection.
- Series GraphQL requests require `CredentialIsolatedNoRedirectTransport` and
  execute through `DoWithoutCredentialsNoRedirect`, so caller cookies,
  authorization headers, and redirect following are not used.
- Unknown `tab` season ids return `ErrUnavailable` instead of an empty playlist.
- Unrelated malformed or oversized season containers are skipped during all-series
  extraction; season id/name bounds apply only to the selected `tab` season.
- Opaque GraphQL transport failures map to `ErrSVTSeriesNetwork` without
  forwarding underlying error text.
- Playlist entries use validated `svt:<video-id>` opaque pseudo URLs with
  `ie_key=region_svt`, matching the reference handoff to `SVTPlayIE` while
  keeping re-entry inside this extractor.
- SVT article/page playlists (`SVTPageIE`) remain outside this pilot.
