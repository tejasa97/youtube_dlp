# Vimeo subtitles evidence

The bounded implementation merges manual subtitles from three extraction
sources, in priority order:

1. **Player config** — the public `request.text_tracks` array in the Vimeo
   player config (`yt_dlp/extractor/vimeo.py` at pinned commit
   `aefce1eea4d0b6bab1ec2bd3beff09bff91a39c8`, lines 325-328). Relative
   references resolve only against `https://player.vimeo.com/`; absolute,
   protocol-relative, and relative inputs must end there over HTTPS without
   credentials, ports, fragments, encoded path separators, encoded dot paths,
   repeated encodings, or NUL. Path validation is strict while query tokens are
   retained in result URLs and never included in errors.

2. **Viewer-JWT texttracks API** — when the transport exposes both
   credential-isolated no-redirect and scoped-authorization capabilities, the
   extractor obtains an anonymous viewer application JWT through the same
   `_next/viewer` mechanism used for album access and requests
   `https://api.vimeo.com/videos/<id>/texttracks` with only the scoped JWT
   Authorization header. HTTP 401/403/404 responses are non-fatal; extraction
   continues with player-config and manifest fallbacks.

3. **HLS/DASH manifest fallbacks** — when the transport is credential-isolated
   no-redirect capable, bounded manifest reads discover subtitle renditions from
   HLS master playlists (`TYPE=SUBTITLES`) and DASH text representations.
   Manifest fetches never forward ambient cookies, Authorization, Proxy-
   Authorization, or Referer.

The player-config fetch has the same trusted boundary: only an HTTPS, clean-path
`player.vimeo.com` config URL may be requested. Config query tokens are
preserved for the request, while the Referer is always the canonical
`https://vimeo.com/<id>` URL and never contains caller tokens.

## Product download boundary

Extraction emits a normalized `subtitles` object. Manifest- and API-derived
tracks additionally set `_credential_isolated: true` on each subtitle entry.
The product subtitle downloader honors that flag per track:

- **Credential-isolated tracks** (`_credential_isolated`) — HLS child `.m3u8`
  playlists are assembled into WebVTT and direct/DASH subtitle URLs are fetched
  through `DoWithoutCredentialsNoRedirect`, which strips ambient and explicit
  cookies, Authorization, Proxy-Authorization, and Referer. Redirects are not
  followed and the cookie jar is unused. Assembled payloads are written through
  the shared downloader's bounded atomic write path.

- **Player-config tracks** — HLS, direct, and DASH subtitle downloads use the
  ambient operation transport, preserving default Referer, cookie-jar cookies,
  Authorization, Proxy-Authorization, per-track `http_headers`, and redirects.

Password submission, DRM, live archives, and arbitrary Vimeo API hosts remain
out of scope. Public channel, explicit/bare user, and group playlist breadth is
documented separately in `docs/VIMEO_CHANNEL_PLAYLISTS_EVIDENCE.md`. Invalid
individual tracks are ignored; a too-large track list is invalid metadata.

Primary integration checklist: retain this extractor's existing Vimeo route;
preserve the normalized `subtitles` object and per-track `_credential_isolated`
flag through product selection and download; do not log track URLs with tokens.

## Acceptance evidence

| Requirement | Evidence |
| --- | --- |
| Fixture-backed public config parsing, formats and duplicates | `config.json`, `expected.json`, `TestVimeoExtractsProgressiveHLSAndDASHWithProfile` |
| Relative/protocol-relative URLs, labels, mixed invalid data and no tracks | `text_tracks_mixed.json`, `text_tracks_empty.json`, `TestVimeoTextTracksAreBoundedAndFailClosed` |
| Request, response bound, network/config failure and profile contract | `TestVimeoExtractsProgressiveHLSAndDASHWithProfile`, `TestVimeoFailuresAreCategorized` |
| Config-origin trust boundary and secret-safe failure | `TestVimeoConfigURLFailsClosedWithoutRequests`, `FuzzNormalizeVimeoConfigURL` |
| Limits, cancellation and hostile URL policy | `TestVimeoTextTracksAreBoundedAndFailClosed`, `TestNormalizeVimeoTextTrackURLRejectsHostileInputs`, `TestValidVimeoRefererRejectsHostileInputs` |
| Manifest subtitle fallback merge and `_credential_isolated` metadata | `TestVimeoSubtitleManifestFallbackMergesHLSAndDASH`, `internal/protocol/hls.TestParseMasterSubtitles`, `internal/protocol/hls.TestAssembleWebVTTConcatenatesSegments`, `internal/protocol/hls.TestAssembleWebVTTRejectsEncryptedSegments`, `internal/protocol/dash.TestParseTextRepresentations`, `internal/downloader.TestWriteFinalizesPayloadAtomically`, `pkg/ytdlp.TestSubtitleDownloaderAssemblesHLSSubtitlePlaylists`, `pkg/ytdlp.TestSubtitleHLSDownloadRejectsEncryptedPlaylists` |
| Viewer-JWT texttracks API and extractor credential isolation | `TestVimeoViewerJWTTexttracksAPIUsesScopedAuthorization`, `TestVimeoTexttracksAPI401And403AreNonfatal`, `TestVimeoSubtitleManifestFetchesWithoutCredentials`, `TestVimeoSubtitleManifestCredentialIsolation` |
| Per-track product isolation and ambient redirect regression | `pkg/ytdlp.TestSubtitleHLSIsolatedTrackSendsNoCredentials`, `pkg/ytdlp.TestSubtitleIsolatedDirectDownloadSendsNoCredentials`, `pkg/ytdlp.TestCredentialIsolatedSubtitleTransportStripsAmbientCredentials`, `pkg/ytdlp.TestSubtitleHLSPreservesAmbientCredentialsAndRedirects`, `pkg/ytdlp.TestSubtitleDirectDownloadPreservesAmbientCredentialsAndRedirects`, `pkg/ytdlp.TestSubtitleDASHDownloadPreservesAmbientCredentials`, `internal/network.TestDoWithoutCredentialsNoRedirectDropsAllCredentialSources` |
| Text-track bounds | `TestVimeoSubtitleLimitsRejectOversizedSourcesAndAggregate` |
| Parser and normalizer semantic fuzz invariants | `FuzzParseVimeoConfig`, `FuzzNormalizeVimeoTextTrackURL`, `FuzzParseMasterSubtitles`, `FuzzParseTextRepresentations` |
