# Phase 1 media lane completion evidence

This document records the scoped P1-03 and P1-04 implementation completed by
the media lane. It does not change the shared parity manifest; the primary
integrator owns those status decisions.

## P1-03: DASH protocol core

Implemented and covered by automated tests:

- Static and dynamic MPD parsing with MPD, Period, AdaptationSet, and
  Representation `BaseURL` inheritance.
- Inherited SegmentTemplate attributes, `$RepresentationID$`, `$Bandwidth$`,
  `$Number$`, `$Time$`, zero-padded number templates, initialization templates,
  initialization elements, duration templates, and SegmentTimeline templates.
- ISO/IEC 23009-1 `r="-1"` expansion to the next explicit `S@t`, period
  duration, media-presentation duration, or deterministic dynamic
  availability/publish boundary.
- SegmentList URLs and byte ranges, initialization ranges, and single-file
  SegmentBase representations.
- Separate deterministic best audio/video selection, including equal
  representation IDs across content types.
- Dynamic MPD updates using explicit configuration or the manifest minimum
  update period, ordered URL/range de-duplication, transition to static MPDs,
  and context cancellation before output finalization.
- Fragment transfer, retries, resumable state, bounded concurrency, and atomic
  output continue to use the shared fragment engine.
- The downloader returns track results and a merge requirement; parsing and
  downloading do not invoke ffmpeg.

Key evidence:

- `internal/protocol/dash.TestParseBaseInheritanceTemplatesAndSegmentList`
- `internal/protocol/dash.TestParseInheritedNegativeRepeatDynamicTimeline`
- `internal/protocol/dash.TestParseSegmentBaseSingleFileAndRejectsSIDX`
- `internal/protocol/dash.TestDownloadDynamicMPDPollsAndDeduplicates`
- `internal/protocol/dash.TestDownloadDynamicUsesManifestPollIntervalAndCancels`
- `internal/protocol/dash.TestDownloadDynamicDoesNotCollideSameRepresentationID`
- `internal/protocol/dash.FuzzParse`
- `conformance/media/dash/PROVENANCE.md`

Known deviation: SegmentBase `indexRange` requires parsing an ISO BMFF SIDX box
and range-fetching the referenced subsegments. This mode fails with
`ErrUnsupportedAddressing`; it is never silently treated as a complete media
range. Multiple DASH periods are parsed, but the Phase 1 selector still chooses
one best representation per media type rather than concatenating periods.

## P1-04: ffmpeg and ffprobe supervision

Implemented and covered by automated tests:

- Explicit/PATH discovery, version reporting, and ffprobe JSON decoding.
- Shell-free argument-vector execution with a minimal deterministic locale,
  bounded stdout/stderr, redacted token/signature diagnostics, and progress
  events.
- Context-bound process termination (Unix process group; direct process on
  Windows), merge of separate audio/video tracks, container remux, and probe.
- Atomic same-directory temporary outputs, destination conflict handling,
  cleanup after cancellation/failure, and retention of downloaded source
  tracks until successful postprocessing.
- Categorized sentinel errors for unavailable tools, media failures,
  destination conflicts, missing merge tracks, and missing toolsets.
- License-safe deterministic media is generated from ffmpeg lavfi sources in
  tests; no checked-in copyrighted media and no Python process or package is
  used.

Key evidence:

- `internal/media/ffmpeg.TestDiscoverVersionsProbeAndMerge`
- `internal/media/ffmpeg.TestCommandCancellation`
- `internal/media/ffmpeg.TestAtomicPostprocessCancellationRemovesTemporaryOutput`
- `internal/media/ffmpeg.TestRemuxCategorizesFailureAndDestinationConflict`
- `internal/media/ffmpeg.TestDiagnosticRedaction`
- `internal/media/ffmpeg.FuzzRedactDiagnostic`
- `internal/media/pipeline.TestDASHDownloadAndFFmpegMergeEndToEnd`
- `internal/media/pipeline.TestRemuxDownloadFinalizesThenRemovesSource`

Known deviation: Phase 1 does not implement codec-specific transcoding,
subtitle embedding, thumbnail embedding, metadata rewriting, or SIDX parsing.
The new `Toolset.Remux` and `pipeline.RemuxDownload` APIs are product-facing
integration hooks; shared client dispatch remains owned by the primary agent.

## Portability and Python-free boundary

The lane is pure Go. ffmpeg and ffprobe are optional non-Python external tools.
Direct HTTP/HLS/DASH downloading does not require either executable. The lane
is checked with native tests/race/vet/fuzz, Windows and Linux cross-compilation,
and repository Docker verification by the integrator. No production source
loads fixtures or the pinned reference checkout.
