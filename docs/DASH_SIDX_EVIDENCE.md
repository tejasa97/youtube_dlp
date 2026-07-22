# DASH SegmentBase indexRange / SIDX Expansion Evidence

## Scope

This document records the implementation of native, Python-free DASH
SegmentBase `indexRange` support via ISO-BMFF SIDX box parsing and expansion
into bounded byte-range media segments, including bounded hierarchical
(reference_type=1) recursive expansion.

Branch: `codex/dash-hierarchical-sidx`

## Behavior implemented

1. **MPD model**: `SegmentBase@indexRange` is parsed at Representation,
   AdaptationSet, and Period levels. Fields inherit individually via
   `mergeSegmentBases` (Period → AdaptationSet → Representation), matching
   the existing template/list merge pattern. `Initialization` is treated as
   an overriding element: a more-specific Initialization replaces the parent
   element wholesale (shallow inheritance), matching DASH-IF dash.js
   behavior (SegmentValuesMap.js, objectiron.js). Malformed, reversed,
   negative, and overflowing ranges are rejected with
   `ErrUnsupportedAddressing`.

2. **SIDX parser** (`sidx.go`): Standalone, fuzzable ISO-BMFF SIDX parser
   supporting version 0 and 1, normal and 64-bit extended box sizes,
   non-SIDX box skipping, and all fail-closed conditions (truncation,
   overflow, zero timescale, zero reference size, segment count limits).
   Both reference_type=0 (leaf media) and reference_type=1 (nested index)
   are parsed and preserved via the `IsIndex` field on `SIDXReference`.

3. **Hierarchical SIDX expansion** (`downloader.go`): Bounded recursive
   resolver expands hierarchical SIDX indexes into ordered leaf media ranges.
   Safety limits: max depth 8, max 256 parsed boxes per representation,
   max 16 MiB cumulative index bytes, leaf count bounded by effective
   MaxSegments. Cycle detection via visited-range key (URL + start + length).
   Overlapping final leaf media ranges are rejected. Depth-first reference
   order is preserved. Nested index bytes never enter assembled media output.

4. **Index-range retrieval** (`downloader.go`): Bounded HTTP range request
   with header propagation, cancellation, 206/200 handling, and size limits.
   Content-Range is mandatory for every 206 response and strictly validated:
   START-END must match the request, and the total must be `*` or a decimal
   integer strictly greater than END. The 200 fallback uses subtraction-based
   bounds checks to prevent overflow panics from hostile ranges. Expanded
   segments pass through the existing fragment downloader for retry,
   concurrency, atomic publication, and limits.

4. **Initialization/media overlap**: Any overlap between the initialization
   range and any media range is explicitly rejected with
   `ErrUnsupportedAddressing`. Rationale: partial trimming risks corrupting
   codec configuration; full omission discards required bytes.

5. **Dynamic manifests**: Dynamic SegmentBase/SIDX is explicitly rejected
   with `ErrUnsupportedAddressing`. Rationale: stale SIDX data cannot be
   safely applied to a resource that may have changed between polls. This is
   the smaller provably-correct behavior versus re-fetching on each poll.

6. **Multi-period**: Static compatible SIDX representations retain period
   boundaries and participate in the supervised multi-period concat path.
   Dynamic SegmentBase/SIDX remains rejected.

## Remaining deviations

- Dynamic SegmentBase/SIDX is rejected (not re-fetched per poll).
- Multi-period composition requires compatible fragmented signatures across
  every static period; dynamic and unfragmented multi-period sets are rejected.
- Initialization/media range overlap is rejected (not trimmed).
- The index fetch does not retry on transient failure (single attempt);
  media segment retries use the existing fragment engine machinery.
- Remote or cross-resource nested indexes are not followed; hierarchy
  remains within the trusted SegmentBase media URL.
- Malformed or unverifiable hierarchies fail closed.

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
- `TestParseSegmentBaseFieldLevelInheritanceSplitFields`
- `TestParseSegmentBaseFieldLevelInheritanceOverride`
- `TestParseSegmentBaseFieldLevelInheritanceInitSourceURLFromPeriod`
- `TestParseSegmentBaseIndexRangeSeparateInitResource`
- `TestParseSegmentBaseIndexRangeSameResourceInit`
- `TestParseSegmentBaseIndexRangeMalformedRanges`
- `TestParseSegmentBaseCoexistsWithTemplateAndList`
- `TestParseMultiPeriodPreservesPeriodIdentity`

### Downloader integration tests
- `TestDownloadSIDXExactRangeHeader`
- `TestDownloadSIDXHeadersPropagated`
- `TestDownloadSIDX206Success`
- `TestDownloadSIDX200Fallback`
- `TestDownloadSIDXInvalidContentRange`
- `TestDownloadSIDXMissingContentRange`
- `TestDownloadSIDXMalformedContentRange`
- `TestDownloadSIDXMismatchedContentRange`
- `TestDownloadSIDXContentRangeEmptyTotal`
- `TestDownloadSIDXContentRangeInconsistentTotal`
- `TestDownloadSIDXTruncatedResponse`
- `TestDownloadSIDXOversized200Response`
- `TestDownloadSIDXHostileRangeOverflowNoPanic`
- `TestDownloadSIDXOrderedInitAndMediaAssembly`
- `TestDownloadSIDXRetryTransientIndexFailure`
- `TestDownloadSIDXRetryTransientMediaFailure`
- `TestDownloadSIDXCancellationDuringIndexRetrieval`
- `TestDownloadSIDXCancellationDuringSegmentDownload`
- `TestDownloadSIDXNoOutputOnFailure`
- `TestDownloadSIDXAudioVideoMergeRequired`
- `TestDownloadSIDXAtomicPublication`
- `TestDownloadSIDXDynamicRejected`
- `TestDownloadSIDXInitFullOverlapRejected`
- `TestDownloadSIDXInitPartialOverlapRejected`
- `TestDownloadSIDXInitOverlapWithLaterReferenceRejected`
- `TestDownloadSIDXInitNoOverlapSucceeds`

### Hierarchical SIDX tests
- `TestSIDXHierarchicalReferenceParsed`
- `TestSIDXVersion0NestedReference`
- `TestSIDXVersion1NestedReference`
- `TestSIDXMixedLeafAndNestedReferences`
- `TestSIDXNestedWithNonZeroFirstOffset`
- `TestSIDXNestedAfterNonSIDXBox`
- `TestSIDXLeafOnlyHasNoHierarchicalReferences`
- `TestDownloadHierarchicalSIDXOneLevel`
- `TestDownloadHierarchicalSIDXTwoLevels`
- `TestDownloadHierarchicalSIDXMixedOrdering`
- `TestDownloadHierarchicalSIDXExactNestedRangeHeader`
- `TestDownloadHierarchicalSIDXHeadersPropagated`
- `TestDownloadHierarchicalSIDX200Fallback`
- `TestDownloadHierarchicalSIDXNoSIDXBytesInOutput`
- `TestDownloadHierarchicalSIDXExcessiveDepth`
- `TestDownloadHierarchicalSIDXExcessiveBoxCount`
- `TestDownloadHierarchicalSIDXRepeatedRangeDetection`
- `TestDownloadHierarchicalSIDXTruncatedNested`
- `TestDownloadHierarchicalSIDXLeafCountLimit`
- `TestDownloadHierarchicalSIDXNestedTransportFailure`
- `TestDownloadHierarchicalSIDXCancellationDuringNestedFetch`
- `TestDownloadHierarchicalSIDXNoOutputOnFailure`
- `TestDownloadHierarchicalSIDXMultiPeriod`
- `TestDownloadHierarchicalSIDXAudioVideo`
- `TestDownloadHierarchicalSIDXVersion1Child`
- `TestDownloadHierarchicalSIDXExtendedSizeChild`
- `TestDownloadHierarchicalSIDXCumulativeByteBudget`
- `TestDownloadHierarchicalSIDXTruncatedChildResponse`
- `TestDownloadHierarchicalSIDXOffsetOverflow`
- `TestDownloadHierarchicalSIDXRoundTripV0Hex`

### Content-Range unit tests
- `TestValidContentRangeTotalValidation`

### Fuzz targets
- `FuzzSIDX` (bounded, no network/filesystem; seeded with v0, v1, truncated,
  malformed, and non-SIDX inputs including index references)
- `FuzzSIDXRecursiveExpansion` (bounded, no network/filesystem; seeds a root
  index range over a deterministic in-memory resource and uses
  `downloader.expandOneSIDX` to surface panic, unbounded allocation, or
  invalid segment emission across fuzzed SIDX payloads)

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

## Design decision: initialization/media overlap

Any overlap between the initialization range and media ranges is **explicitly
rejected**. Rationale:

1. Initialization segments contain codec configuration (ftyp, moov) that must
   be delivered complete and unmodified.
2. Partial trimming of duplicated bytes risks corrupting the initialization
   data if the overlap boundary does not align with box boundaries.
3. Full omission discards required bytes, producing unplayable output.
4. No known production MPD uses overlapping initialization and media ranges.

## Design decision: Initialization shallow inheritance

The `Initialization` child element inherits as a **wholesale override**, not
field-by-field. A more-specific `Initialization` element replaces the parent's
entirely. This matches DASH-IF dash.js behavior:
- `src/dash/parser/maps/SegmentValuesMap.js` (L37-L49)
- `src/dash/parser/objectiron.js` (L35-L42)

Rationale: combining `sourceURL` from a parent with `range` from a child would
request the child's byte range from the wrong resource.

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
