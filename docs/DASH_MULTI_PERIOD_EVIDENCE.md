# DASH multi-period composition evidence

## Scope

The native DASH pipeline composes compatible fragmented representations across
ordered static MPD Periods without Python. Period boundaries remain explicit
through download and are finalized through the existing shell-free ffmpeg
supervisor.

Behavior was reviewed against the pinned reference
`yt-dlp/yt-dlp@aefce1eea4d0b6bab1ec2bd3beff09bff91a39c8`, principally
`yt_dlp/extractor/common.py::_parse_mpd_periods` and `_merge_mpd_periods` plus
`yt_dlp/postprocessor/ffmpeg.py::FFmpegFixupDuplicateMoovPP`.

## Closure matrix

| Manifest | Addressing | Periods | Status |
| --- | --- | --- | --- |
| static | SegmentTemplate, including inherited BaseURL/timeline/PTO | single | compatible |
| dynamic | SegmentTemplate/List, bounded polling and append de-duplication | single | compatible |
| static | SegmentList and byte ranges | single | compatible |
| static | SegmentBase/indexRange with bounded SIDX and hierarchy | single | compatible |
| static | Template/List/Base selected as one compatible track | multiple | compatible when timing and codec/container signature are exact |
| dynamic | Template/List selected as one compatible track | multiple | compatible only with stable period and representation identities, exact contiguous timing, and bounded snapshots |
| dynamic | SegmentBase/indexRange SIDX | multiple | fail closed: independent period index/window evolution is not yet proven |

The `conformance/media/dash/multi_period_mixed_addressing.mpd` fixture uses
only `example.test` URLs and exercises static Template/List/SegmentBase
selection. Production does not read the pinned reference checkout.

## Implemented behavior

1. The parser records bounded Period identity, derives omitted starts or
   durations from adjacent Periods and the presentation duration where
   possible, and preserves each representation's period index, ID, and
   fragmented/unfragmented state.
2. Static multi-period selection intersects exact format signatures across
   every period. Content kind, MIME type, codecs, language, frame rate, audio
   sampling rate, bandwidth, width, and height must agree; representation IDs
   may differ.
3. The highest-bandwidth common signature is selected deterministically per
   audio/video kind. A high-quality representation present in only one period
   cannot displace a lower common representation and truncate the result.
4. SegmentTemplate, SegmentList, and static SegmentBase/SIDX plans retain their
   period boundaries. Each period is downloaded atomically through the shared
   fragment engine with normal headers, bounds, retries, and cancellation. The
   configured segment limit applies to the complete selected track, not once
   per Period.
5. Completed period files are passed to the bounded ffmpeg concat demuxer in
   manifest order. Separate video/audio tracks are each concatenated before
   the existing merge operation. Source period files are removed only after
   successful finalization and remain recoverable when postprocessing fails.
6. Product dispatch recognizes the multi-period result and requires the
   supervised media toolchain instead of publishing a raw duplicate-MOOV byte
   stream. When overwrite is disabled, an existing final destination is
   rejected before any Period fragments are transferred.
7. Bounded dynamic multi-period Template/List selection is supported when each
   snapshot preserves the exact ordered Period IDs, timing, selected
   representation IDs, and format signature. New fragments are accumulated per
   Period; a replacement identity fails before any fragments are requested.

## Fail-closed boundaries

- At most 128 periods are accepted, matching the media concat input bound.
- Dynamic multi-period SegmentBase/indexRange SIDX MPDs are rejected because
  safe polling requires independently stable index and live-window identities
  per Period. Dynamic Template/List MPDs are supported only under the stable
  identity contract above.
- Direct/unfragmented multi-period resources are rejected rather than byte
  concatenated.
- Period timing must be fully derivable, start at zero, remain contiguous, and
  agree with a declared presentation duration. Gaps, overlaps, zero-duration
  Periods, and unresolved timing are rejected rather than silently flattened.
- Every emitted track requires one exact compatible signature in every period.
- Missing periods, incompatible codecs/geometry/bitrates, missing media tools,
  malformed manifests, failed fragments, and cancellation never publish a
  partial destination.

These are narrower than the pinned reference's general format dictionary
grouping, but they avoid silently incomplete output and make the claimed subset
mechanically testable.

## Automated evidence

- `internal/protocol/dash.TestParseMultiPeriodFixture`
- `internal/protocol/dash.TestParseRecordsPeriodIdentityAndFragmentedState`
- `internal/protocol/dash.TestParseRejectsPeriodCountBeyondConcatBoundary`
- `internal/protocol/dash.TestSelectMultiPeriodChoosesHighestCommonSignature`
- `internal/protocol/dash.TestSelectMultiPeriodRejectsUnsafeCombinations`
- `internal/protocol/dash.TestSelectMultiPeriodRejectsDiscontinuousOrUnknownTiming`
- `internal/protocol/dash.TestParseDerivesContiguousPeriodTiming`
- `internal/protocol/dash.TestDownloadMultiPeriodConcatenatesFragmentsInManifestOrder`
- `internal/protocol/dash.TestDownloadMultiPeriodEnforcesAggregateSegmentLimit`
- `internal/protocol/dash.TestDownloadMultiPeriodRejectsExistingDestinationBeforeFragments`
- `internal/protocol/dash.TestDownloadMultiPeriodFailureDoesNotPublishTrack`
- `internal/protocol/dash.TestDownloadMultiPeriodCancellationDoesNotPublishTrack`
- `internal/protocol/dash.TestSelectDynamicMultiPeriodAllowsStableFragmentedMixedAddressing`
- `internal/protocol/dash.TestParseMixedAddressingMultiPeriodFixtureSelectsOneSafeTrack`
- `internal/protocol/dash.TestDownloadDynamicMultiPeriodAccumulatesStableSnapshots`
- `internal/protocol/dash.TestDownloadDynamicMultiPeriodRejectsRepresentationReplacement`
- `internal/protocol/dash.TestDownloadDynamicMultiPeriodSIDXFailsClosedBeforeMedia`
- `internal/media/pipeline.TestFinalizeDASHMultiPeriodRemuxesAndRemovesSource`
- `internal/media/pipeline.TestFinalizeDASHMultiPeriodConcatenatesAndMergesTracks`
- `engine.TestClientDASHMultiPeriodDispatchAndFixup`
- `internal/protocol/dash.FuzzParse` includes a deterministic multi-period seed

The product test generates license-safe media with ffmpeg lavfi, downloads two
synthetic periods through the public client, verifies the concatenated duration
contains both periods, and confirms no intermediate period files remain.

## Python-free boundary

Production and tests do not execute Python or load the reference checkout.
ffmpeg/ffprobe remain optional non-Python media tools invoked through explicit
argument vectors with cancellation, bounded diagnostics, and atomic outputs.
