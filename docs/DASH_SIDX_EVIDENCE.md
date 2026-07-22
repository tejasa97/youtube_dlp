# DASH SegmentBase indexRange / SIDX Expansion Evidence

## Scope

This document records the implementation of native, Python-free DASH
SegmentBase `indexRange` support via ISO-BMFF SIDX box parsing and expansion
into bounded byte-range media segments.

Branch: `minimax/dash-sidx`

## Behavior implemented

1. **MPD model**: `SegmentBase@indexRange` is parsed at Representation,
   AdaptationSet, and Period levels (inherited via `firstSegmentBase`).
   Malformed, reversed, negative, and overflowing ranges are rejected with
   `ErrUnsupportedAddressing`.

2. **SIDX parser** (`sidx.go`): Standalone, fuzzable ISO-BMFF SIDX parser
   supporting version 0 and 1, normal and 64-bit extended box sizes,
   non-SIDX box skipping, and all fail-closed conditions (truncation,
   overflow, hierarchical references, zero timescale, zero reference size,
   segment count limits).

3. **Index-range retrieval** (`downloader.go`): Bounded HTTP range request
   with header propagation, cancellation, 206/200 handling, Content-Range
   validation, and size limits. Expanded segments pass through the existing
   fragment downloader for retry, concurrency, atomic publication, and limits.

4. **Dynamic manifests**: Dynamic SegmentBase/SIDX is explicitly rejected
   with `ErrUnsupportedAddressing`. Rationale: stale SIDX data cannot be
   safely applied to a resource that may have changed between polls. This is
   the smaller provably-correct behavior versus re-fetching on each poll.

5. **Multi-period**: Not implemented. Each period's representations remain
   independent; no concatenation is performed.

## Remaining deviations

- Dynamic SegmentBase/SIDX is rejected (not re-fetched per poll).
- Hierarchical SIDX (`reference_type == 1`) is rejected.
- Multi-period concatenation is not implemented.
- The index fetch does not retry on transient failure (single attempt);
  media segment retries use the existing fragment engine machinery.

## Test names for parity manifest integration

The following test names can be added to `conformance/parity_manifest.yaml`
by the primary agent:

### SIDX parser tests
- `TestSIDXVersion0OneReference`
- `TestSIDXVersion0MultipleReferences`
- `TestSIDXVersion1SixtyFourBitFields`
- `TestSIDXNonZeroFirstOffset`
- `TestSIDXExtendedBoxSize`
- `TestSIDXWithinLargerByteRange`
- `TestSIDXPreciseAbsoluteByteRanges`
- `TestSIDXTruncatedHeader`
- `TestSIDXTruncatedReferenceTable`
- `TestSIDXInvalidVersion`
- `TestSIDXZeroTimescale`
- `TestSIDXZeroReferenceSize`
- `TestSIDXHierarchicalReferenceRejection`
- `TestSIDXSizeOverflow`
- `TestSIDXOffsetOverflow`
- `TestSIDXSegmentCountLimit`
- `TestSIDXNoSIDXBox`
- `TestSIDXMalformedExtendedSize`
- `TestSIDXBoxSizeTooSmall`
- `TestSIDXEmptyData`
- `TestSIDXMediaRangesWithFirstOffset`

### MPD parsing tests
- `TestParseSegmentBaseSingleFileAndIndexRange`
- `TestParseSegmentBaseIndexRangeRepresentationLevel`
- `TestParseSegmentBaseIndexRangeInheritedPeriod`
- `TestParseSegmentBaseIndexRangeInheritedAdaptationSet`
- `TestParseSegmentBaseIndexRangeSeparateInitResource`
- `TestParseSegmentBaseIndexRangeSameResourceInit`
- `TestParseSegmentBaseIndexRangeMalformedRanges`
- `TestParseSegmentBaseCoexistsWithTemplateAndList`
- `TestParseMultiPeriodPreservesExplicitBehavior`

### Downloader integration tests
- `TestDownloadSIDXExactRangeHeader`
- `TestDownloadSIDXHeadersPropagated`
- `TestDownloadSIDX206Success`
- `TestDownloadSIDX200Fallback`
- `TestDownloadSIDXInvalidContentRange`
- `TestDownloadSIDXTruncatedResponse`
- `TestDownloadSIDXOversized200Response`
- `TestDownloadSIDXOrderedInitAndMediaAssembly`
- `TestDownloadSIDXRetryTransientIndexFailure`
- `TestDownloadSIDXRetryTransientMediaFailure`
- `TestDownloadSIDXCancellationDuringIndexRetrieval`
- `TestDownloadSIDXCancellationDuringSegmentDownload`
- `TestDownloadSIDXNoOutputOnFailure`
- `TestDownloadSIDXAudioVideoMergeRequired`
- `TestDownloadSIDXAtomicPublication`
- `TestDownloadSIDXDynamicRejected`

### Fuzz targets
- `FuzzSIDX` (bounded, no network/filesystem; seeded with v0, v1, truncated,
  malformed, and non-SIDX inputs)

## Design decision: dynamic SIDX

Dynamic manifests with SegmentBase/SIDX are **explicitly rejected** rather than
re-fetching the index on each poll. Rationale:

1. A dynamic MPD's media resource may grow or be rewritten between polls.
2. SIDX byte offsets are absolute; applying stale offsets to a changed resource
   produces corrupt output silently.
3. Re-fetching and re-expanding on each poll requires tracking which segments
   are new vs. already downloaded, adding complexity without clear correctness
   guarantees for SegmentBase (unlike SegmentTemplate which uses time/number
   addressing).
4. The explicit rejection is provably safe: no stale data is ever applied.

## Files changed

- `internal/protocol/dash/sidx.go` (new)
- `internal/protocol/dash/sidx_test.go` (new)
- `internal/protocol/dash/sidx_downloader_test.go` (new)
- `internal/protocol/dash/mpd.go` (modified)
- `internal/protocol/dash/mpd_test.go` (modified)
- `internal/protocol/dash/downloader.go` (modified)
- `conformance/media/dash/sidx_indexrange.mpd` (new)
- `conformance/media/dash/sidx_indexrange.expected.json` (new)
- `conformance/media/dash/sidx_v0_two_refs.hex` (new)
- `conformance/media/dash/PROVENANCE.md` (modified)
- `docs/DASH_SIDX_EVIDENCE.md` (new)
