# Vimeo subtitles evidence

The bounded implementation supports only the public `request.text_tracks`
array in the Vimeo player config. This is the exact config branch used by
`yt_dlp/extractor/vimeo.py` at pinned commit
`aefce1eea4d0b6bab1ec2bd3beff09bff91a39c8`, lines 325-328.

Tracks expose manual `subtitles` with a language key, a VTT format, and an
optional label/name. Relative references resolve only against
`https://player.vimeo.com/`; absolute, protocol-relative, and relative inputs
must end there over HTTPS without credentials, ports, fragments, encoded path
separators, encoded dot paths, repeated encodings, or NUL. Path validation is
strict while query tokens are retained in result URLs and never included in
errors.

The pre-existing player-config fetch now has the same trusted boundary: only
an HTTPS, clean-path `player.vimeo.com` config URL may be requested.
Config query tokens are preserved for the request, while the Referer is always
the canonical `https://vimeo.com/<id>` URL and never contains caller tokens.

Deliberate deviations: manifest subtitle fallbacks use credential-isolated,
no-redirect manifest reads and never forward Vimeo JWTs, cookies, Authorization,
or Referer to subtitle/CDN origins. HLS subtitle renditions remain child `.m3u8`
playlists at extraction time and are assembled into WebVTT by the product
subtitle downloader through `DoWithoutCredentialsNoRedirect`, then written with
the shared downloader's bounded atomic write path rather than being exposed as
direct VTT downloads.
The anonymous viewer-JWT texttracks API path
is attempted through the same `_next/viewer` mechanism used for album access;
when Vimeo rejects that call, extraction continues with player-config and
manifest fallbacks only. Password submission, DRM, live archives, and arbitrary
Vimeo API hosts remain out of scope. Public channel, explicit/bare user, and
group playlist breadth is documented separately in
`docs/VIMEO_CHANNEL_PLAYLISTS_EVIDENCE.md`. Invalid individual tracks are
ignored; a too-large track list is invalid metadata.

Primary integration checklist: retain this extractor's existing Vimeo route;
do not add subtitle-specific network requests; ensure consumers preserve the
existing normalized `subtitles` object and do not log track URLs with tokens.

## Acceptance evidence

| Requirement | Evidence |
| --- | --- |
| Fixture-backed public config parsing, formats and duplicates | `config.json`, `expected.json`, `TestVimeoExtractsProgressiveHLSAndDASHWithProfile` |
| Relative/protocol-relative URLs, labels, mixed invalid data and no tracks | `text_tracks_mixed.json`, `text_tracks_empty.json`, `TestVimeoTextTracksAreBoundedAndFailClosed` |
| Request, response bound, network/config failure and profile contract | `TestVimeoExtractsProgressiveHLSAndDASHWithProfile`, `TestVimeoFailuresAreCategorized` |
| Config-origin trust boundary and secret-safe failure | `TestVimeoConfigURLFailsClosedWithoutRequests`, `FuzzNormalizeVimeoConfigURL` |
| Limits, cancellation and hostile URL policy | `TestVimeoTextTracksAreBoundedAndFailClosed`, `TestNormalizeVimeoTextTrackURLRejectsHostileInputs`, `TestValidVimeoRefererRejectsHostileInputs` |
| Manifest subtitle fallback merge | `TestVimeoSubtitleManifestFallbackMergesHLSAndDASH`, `internal/protocol/hls.TestParseMasterSubtitles`, `internal/protocol/hls.TestAssembleWebVTTConcatenatesSegments`, `internal/protocol/hls.TestAssembleWebVTTRejectsEncryptedSegments`, `internal/protocol/dash.TestParseTextRepresentations`, `internal/downloader.TestWriteFinalizesPayloadAtomically`, `pkg/ytdlp.TestSubtitleDownloaderAssemblesHLSSubtitlePlaylists`, `pkg/ytdlp.TestSubtitleHLSDownloadRejectsEncryptedPlaylists` |
| Viewer-JWT texttracks API and credential isolation | `TestVimeoViewerJWTTexttracksAPIUsesScopedAuthorization`, `TestVimeoTexttracksAPI401And403AreNonfatal`, `TestVimeoSubtitleManifestFetchesWithoutCredentials` |
| Text-track bounds | `TestVimeoSubtitleLimitsRejectOversizedSourcesAndAggregate` |
| Parser and normalizer semantic fuzz invariants | `FuzzParseVimeoConfig`, `FuzzNormalizeVimeoTextTrackURL`, `FuzzParseMasterSubtitles`, `FuzzParseTextRepresentations` |
