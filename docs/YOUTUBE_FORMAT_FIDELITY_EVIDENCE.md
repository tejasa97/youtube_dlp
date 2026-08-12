# YouTube format fidelity evidence

Status: compatible for the bounded single-video format normalization corpus
described below. This wave populates the format fields already consumed by the
format sorter and the CLI tables without changing downloader protocols.

## User-visible behavior

Every YouTube format now carries, when attributable and valid:

- `asr` and `audio_channels`;
- `container` (`<ext>_dash`) for single-stream formats;
- `dynamic_range` (`DV`/`HDR10`) from the codec profile via the pinned
  `parse_codecs` classification, which also governs `vcodec`/`acodec` with
  `none` defaults and a raw two-unknown fallback;
- `format_note` (audio display name with `(default)`, quality label or
  prefix-stripped audio quality, `DRC`, `AI-upscaled`, projection and spatial
  audio markers, `DAMAGED`);
- `quality` (pinned ladder rank, minus 0.5 for DRC formats);
- `source_preference` (`-5` for itag 22, `+100` for Premium labels, else `-1`);
- `has_drm` from `drmFamilies` presence;
- `tbr` with `averageBitrate` precedence and `filesize_approx` from the
  approximate duration;
- `language` and `language_preference` from the audio track identity
  (original 10, default 5, descriptive `-desc` -10, plain -1);
- `format_id` suffixes `-drc` and `-sr` (super-resolution via the `xtags=sr=1`
  marker);
- `fps` only when greater than 1;
- `preference` `-10` for damaged formats (approximate duration below half the
  video duration) and `-2` for the 3gp format (17).

Same-itag audio tracks with different track identities survive with colliding
format IDs, distinguished by language — matching the pinned stream identity
`(itag, audioTrack.id, isDrc)`. Format ordering is stable. The extractor does
not emit `resolution`; the product derives it (`formatResolution`), mirroring
`YoutubeDL.py:1283-1284`. Best-format selection is unchanged by the
enrichment (proven by stripping the new fields and re-selecting).

## Behavioral provenance

The contract is derived from the pinned read-only reference
`yt-dlp/yt-dlp@aefce1eea4d0b6bab1ec2bd3beff09bff91a39c8`, specifically
`YoutubeIE._extract_formats_and_subtitles` and `parse_codecs`; exact line
references are recorded in
`conformance/extractors/youtube_format_fidelity/PROVENANCE.md`. All fixture
data is synthetic. No test or production binary imports or executes Python or
reads the reference checkout.

## Known deviations from the pinned reference

- `language_preference` is emitted only together with `language`; the pinned
  reference emits a bare `-1` preference even without a track identity.
- `audio_channels` is emitted only when positive; the pinned reference emits
  raw zeros.
- `abr`/`vbr` are not emitted (the pinned YouTube extractor never sets them;
  the sorter's `abr` field already falls back to `tbr`).
- `scodec` (subtitle codecs) from `parse_codecs` is not emitted.
- `MISSING POT`/verbose client-name format notes and `available_at`/chunked
  downloader options are not emitted (PO-token gating and downloader chunking
  are handled elsewhere in the product).

## Outside the current claim

Storyboard `mhtml` formats, DRM playback, new downloader protocols, SABR
expansion, and Music-description metadata.

## Automated evidence

- `internal/providers/youtube.TestYouTubeFormatFidelityPinnedExtraction`
- `internal/providers/youtube.TestYouTubeFormatFidelityCodecs`
- `internal/providers/youtube.TestYouTubeFormatFidelityLanguagePreferences`
- `internal/providers/youtube.TestYouTubeFormatFidelityQualityAndNotes`
- `internal/providers/youtube.TestYouTubeFormatFidelitySourcePreference`
- `internal/providers/youtube.TestYouTubeFormatFidelityDRCAndSuperResolution`
- `internal/providers/youtube.TestYouTubeFormatFidelityDynamicRangeAndContainer`
- `internal/providers/youtube.TestYouTubeFormatFidelityDamagedAndPreference`
- `internal/providers/youtube.TestYouTubeFormatFidelityFilesizeApprox`
- `internal/providers/youtube.TestYouTubeFormatFidelityIDCollisionsAndOrdering`
- `internal/providers/youtube.TestYouTubeFormatFidelitySelectionUnchanged`
- `internal/providers/youtube.FuzzYouTubeFormatFidelity`
- `engine.TestProductYouTubeFormatFidelityListFormats`

The pinned corpus lives at `conformance/extractors/youtube_format_fidelity/`
(`watch.html`, `expected.json`, `PROVENANCE.md`). The pilot and
player-metadata expected documents were updated only with the always-on
fields (`has_drm`, `source_preference`, `container`); no existing field
changed.
