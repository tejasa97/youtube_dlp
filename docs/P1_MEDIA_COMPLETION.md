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
  SegmentBase representations. A post-Phase 1 extension also expands bounded
  static `SegmentBase@indexRange` entries through native SIDX v0/v1 parsing.
- Separate deterministic best audio/video selection, including equal
  representation IDs across content types.
- A post-Phase 1 extension intersects compatible fragmented formats across
  ordered, contiguous static periods, applies the segment budget across the
  complete track, and finalizes each track through bounded ffmpeg concat before
  optional audio/video merge.
- Dynamic MPD updates using explicit configuration or the manifest minimum
  update period, ordered URL/range de-duplication, transition to static MPDs,
  and context cancellation before output finalization.
- Fragment transfer, retries, resumable state, bounded concurrency, and atomic
  output continue to use the shared fragment engine.
- The downloader returns track results plus merge/multi-period requirements;
  parsing and downloading do not invoke ffmpeg directly.

Key evidence:

- `internal/protocol/dash.TestParseBaseInheritanceTemplatesAndSegmentList`
- `internal/protocol/dash.TestParseInheritedNegativeRepeatDynamicTimeline`
- `internal/protocol/dash.TestParseSegmentBaseSingleFileAndIndexRange`
- `internal/protocol/dash.TestDownloadSIDX206Success`
- `internal/protocol/dash.TestDownloadSIDXCancellationDuringIndexRetrieval`
- `internal/protocol/dash.FuzzSIDX`
- `internal/protocol/dash.TestDownloadDynamicMPDPollsAndDeduplicates`
- `internal/protocol/dash.TestDownloadDynamicUsesManifestPollIntervalAndCancels`
- `internal/protocol/dash.TestDownloadDynamicDoesNotCollideSameRepresentationID`
- `internal/protocol/dash.TestDownloadMultiPeriodConcatenatesFragmentsInManifestOrder`
- `internal/protocol/dash.TestSelectMultiPeriodRejectsDiscontinuousOrUnknownTiming`
- `internal/protocol/dash.TestDownloadMultiPeriodEnforcesAggregateSegmentLimit`
- `internal/media/pipeline.TestFinalizeDASHMultiPeriodConcatenatesAndMergesTracks`
- `pkg/ytdlp.TestClientDASHMultiPeriodDispatchAndFixup`
- `internal/protocol/dash.FuzzParse`
- `conformance/media/dash/PROVENANCE.md`

Remaining deviations: dynamic SegmentBase/SIDX manifests and hierarchical SIDX
references fail with `ErrUnsupportedAddressing`; initialization/media overlap
is rejected instead of trimmed, and index retrieval is single-attempt. Dynamic,
unfragmented, or format-incompatible multi-period sets fail closed. See
`docs/DASH_SIDX_EVIDENCE.md` and `docs/DASH_MULTI_PERIOD_EVIDENCE.md` for the
later extensions and evidence.

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
subtitle embedding, thumbnail embedding, or metadata rewriting. SIDX parsing
was subsequently added to the DASH layer and does not change the ffmpeg
supervision boundary described here.
The new `Toolset.Remux` and `pipeline.RemuxDownload` APIs are product-facing
integration hooks; shared client dispatch remains owned by the primary agent.

## Portability and Python-free boundary

The lane is pure Go. ffmpeg and ffprobe are optional non-Python external tools.
Direct HTTP/HLS/DASH downloading does not require either executable. The lane
is checked with native tests/race/vet/fuzz, Windows and Linux cross-compilation,
and repository Docker verification by the integrator. No production source
loads fixtures or the pinned reference checkout.
