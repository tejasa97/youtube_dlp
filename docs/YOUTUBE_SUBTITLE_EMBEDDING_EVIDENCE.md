# YouTube subtitle embedding evidence

Status: compatible for the bounded native subtitle/container matrix described
below. The implementation is Python-free and invokes ffmpeg directly with an
argument vector.

## User contract

`--embed-subs` selects and downloads compatible subtitles, embeds them after
media download and configured media postprocessors, and replaces existing
subtitle streams in the destination. `--no-embed-subs` clears an inherited
setting. With no explicit manual subtitle write option, temporary sidecars are
removed only after successful embedding. Combining embedding with
`--write-subs` retains selected sidecars; `--write-auto-subs` alone does not,
matching the pinned retention rule. `--skip-download` never attempts embedding
and retains downloaded sidecars.

The Go API exposes the same policy through `SubtitleOptions.Embed` and
`SubtitleOptions.KeepFiles`.

Supported output containers are MP4, MOV, M4A, WebM, MKV, and MKA. Inputs are
bounded to 64 VTT, SRT, ASS, or SSA tracks; WebM accepts VTT only. MP4-family
outputs convert subtitle streams to `mov_text`. Existing subtitle streams are
removed before the selected tracks are mapped in deterministic language order,
with bounded language/name metadata.

Cancellation is checked immediately before atomic publication. Once a media
replacement is committed, terminal completion notifications and sidecar
cleanup warnings are observational rather than vetoes; any sidecar that cannot
be removed remains accurately reported as an artifact.

## Behavioral provenance

The contract was derived from the pinned read-only reference
`yt-dlp/yt-dlp@aefce1eea4d0b6bab1ec2bd3beff09bff91a39c8`:

- CLI enable/disable flags: `yt_dlp/options.py:1683-1690`;
- implicit subtitle selection and sidecar retention:
  `yt_dlp/__init__.py:674-681`; and
- container filtering, multi-input mapping, metadata, replacement, and cleanup:
  `yt_dlp/postprocessor/ffmpeg.py:581-643`.

All automated media is generated from ffmpeg `lavfi`; subtitle bodies,
languages, names, paths, and metadata are artificial.

## Automated evidence

- `internal/media/ffmpeg.TestEmbedSubtitleTracksMP4MapsMetadataAndReplacesExisting`
- `internal/media/ffmpeg.TestEmbedSubtitleTracksMKVAndLegacyEntryPoint`
- `internal/media/ffmpeg.TestEmbedSubtitleTracksRemainingContainerMatrix`
- `internal/media/ffmpeg.TestEmbedSubtitleTracksWebMRequiresAndAcceptsVTT`
- `internal/media/ffmpeg.TestEmbedSubtitleTracksRejectsInvalidInputsBeforeOutput`
- `internal/media/ffmpeg.TestEmbedSubtitleTracksCancellationPreservesInputs`
- `internal/media/ffmpeg.TestSubtitleEmbedArgsDropsDataAndUnknownStreams`
- `internal/media/ffmpeg.TestCompletedEventCannotVetoCommittedOutput`
- `internal/media/ffmpeg.FuzzSubtitleEmbedMetadataValidation`
- `internal/media/postprocess.TestArtifactAndDestinationFailures`
- `internal/media/postprocess.TestSubtitleEmbedFailurePreservesOwnedInputs`
- `pkg/ytdlp.TestProductEmbedsSelectedSubtitleTracksAndAppliesRetention`
- `pkg/ytdlp.TestProductSkipsUnsupportedEmbeddingContainerWithoutDeletingSidecar`
- `pkg/ytdlp.TestProductSkipsUnsupportedWebMTrackWithoutDeletingSidecar`
- `pkg/ytdlp.TestProductConvertsBeforeEmbeddingForWebM`
- `pkg/ytdlp.TestSubtitleCleanupFailureIsNonVetoableAndReported`
- `pkg/ytdlp.TestSubtitleEmbeddingImplicitlySelectsManualTracks`
- `pkg/ytdlp.TestSubtitleKeepFilesRequiresEmbedding`
- `pkg/ytdlp.TestSubtitleEmbeddingTrackLimitFailsBeforeDownload`
- `internal/cli.TestRunEmbedSubtitlesImplicitSelectionAndClear`
- `internal/cli.TestRunEmbedsAutomaticSubtitleAndUsesPinnedRetentionRule`

## Known deviations

Bitmap subtitles, JSON/live-chat captions, inline data subtitles, arbitrary
containers/codecs, and yt-dlp's full ISO-639 normalization database are not
claimed. Unsupported containers or track formats are skipped without deleting
sidecars. Atomic overwrite remains unavailable on Windows, and native Windows
execution is not claimed by cross-build evidence.
