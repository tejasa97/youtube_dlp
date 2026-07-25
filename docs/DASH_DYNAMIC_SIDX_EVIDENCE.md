# DASH Dynamic SegmentBase / SIDX Evidence

## Scope

Bounded single-period dynamic MPD refresh, SegmentBase `indexRange` / SIDX
expansion, append-only accumulation with bounded live-window prefix eviction,
and download for the native Go DASH downloader.

Status: implemented and locally verified.

## Provenance (behavioral reference only)

Reference checkout: `/Users/tejas/projects/yt-dlp-reference` at commit
`aefce1eea4d0b6bab1ec2bd3beff09bff91a39c8` (read-only, not a dependency).

Observations from that commit:

- `yt_dlp/downloader/dash.py` rejects live DASH (`Live DASH videos are not
  supported`) and downloads a precomputed fragment list; it does not refresh
  MPDs or re-fetch evolving SIDX bodies.
- Test fixtures under `test/testdata/mpd/*.mpd` include static
  `SegmentBase@indexRange` examples only; no dynamic SIDX polling behavior is
  implemented in Python at this commit.

This Go implementation therefore extends the product with bounded dynamic SIDX
polling (including rolling live windows) while using the reference only to
confirm that static indexRange fixtures exist and that live DASH remains out of
scope for yt-dlp itself.

## Behavior implemented

1. **Single-period dynamic only**: `pollDynamicSIDX` handles dynamic
   SegmentBase/SIDX. Dynamic multi-period remains rejected in
   `selectRepresentations` and during refresh.

2. **Snapshot budget**: `Config.DynamicPolls` counts snapshots including the
   initial MPD (`1` = one bounded snapshot). Product dispatch maps existing
   `DownloaderOptions.LiveMaxPolls` and `LivePollInterval` into
   `dash.Config.DynamicPolls` and `dash.Config.PollInterval`.

3. **Per-snapshot processing**:
   - Parse refreshed MPD and match the initially selected representation keys.
   - Require exactly one refreshed match per profile; zero means disappeared,
     more than one means ambiguous and unsupported.
   - Validate stable identity (ID, MIME/codecs/language/track properties, media
     URL).
   - Re-fetch and expand the snapshot SIDX (including hierarchical references).
   - Never attach an index to a different media URL or representation.

4. **Leaf identity and live-window contract**:
   - Stable leaf identity is `URL + absolute RangeStart + RangeLength` metadata.
   - The accumulator keeps two sequences: the last accepted **live window** and
     the **append-only download plan**.
   - Snapshots may append new suffix leaves, remain unchanged, or **evict a
     prefix** of the prior live window when the retained suffix is an exact
     ordered identity prefix of the new window.
   - Each newly observed media leaf is appended to the download plan exactly
     once. Eviction removes leaves from the live window only; previously seen
     leaves remain scheduled for download.
   - Shared identities that cannot form that suffix/prefix relationship fail
     closed as non-prefix evolution (mutation of a retained leaf, reorder,
     insertion before the live edge, or rewind/shrink of the live edge).
   - Replaying an already-accumulated leaf identity after eviction/reorder fails
     closed. Overlapping byte ranges on the same media URL fail closed.
     Duplicate identities inside one window fail closed.
   - When no identities are shared, the prior window may be fully replaced by
     novel non-overlapping leaves (extreme live-window roll). Equal-sized
     rebuilds that reuse the same absolute ranges are indistinguishable from an
     unchanged window under URL/range identity and are not treated as content
     mutation.
   - Identity compares URL/range metadata only; it does not verify remote byte
     content behind an unchanged URL/range between polling and later media
     download.

5. **Homogeneous addressing**: When dynamic SIDX mode is entered because any
   selected representation has an `IndexRange` marker, every selected
   representation must use SegmentBase/SIDX markers. Mixed selected addressing
   (for example video SIDX plus audio SegmentTemplate/List) fails closed
   explicitly.

6. **Representation marker validation**: Each representation must have exactly
   one SegmentBase index marker on a single media URL. Multiple marker URLs or
   init/index marker overlap on the same segment are rejected.

7. **Initialization**: One init segment per representation; identical URL/range
   deduplicates. Changed initialization identity fails closed with
   `ErrUnsupportedAddressing` before media download.

8. **Cumulative budgets** across the full polling session:
   - **Output leaves**: `MaxSegments` applies to unique accumulated media leaves
     only; unchanged snapshots and pure prefix eviction do not re-consume this
     budget. The effective media leaf ceiling is
     `min(configured MaxSegments, 10_000 - initCount)` so the shared fragment
     engine hard total-fragment cap (10,000 including init) is never exceeded.
     Violations fail during polling or final validation with
     `ErrTooManySegments`, not at fragment download.
   - **Parser work** (separate): index bytes transferred 16 MiB
     (`maxCumulativeIndexBytes`), parsed SIDX boxes 256
     (`maxSIDXBoxesPerRepresentation`), cumulative leaf discoveries bounded by
     `maxSegmentsPerRepresentation`, recursion depth 8 (`maxSIDXDepth`), per-request
     index fetch 16 MiB (`maxIndexRangeBytes`).
   - HTTP 200 fallback reads count toward the cumulative index-byte budget.

9. **Stop conditions**: Transition to static MPD includes the final snapshot and
   stops polling. Remaining dynamic after the snapshot budget downloads the
   accumulated bounded window (including leaves already rolled out of the live
   SIDX window).

10. **Rejections**: Representation disappearance, ambiguous duplicate keys,
    identity/codec/media-URL mutation, non-prefix evolution, replay after
    eviction, overlapping byte-range suffixes, malformed `Content-Range`, cyclic
    nested SIDX, changed initialization, mixed addressing, invalid marker sets,
    and budget exhaustion remain fail-closed with `ErrUnsupportedAddressing`
    (or categorized fragment errors for media).

11. **Atomic publication**: Manifest polling and SIDX expansion complete before
    any final media write; existing fragment-engine cleanup on later failure is
    unchanged. Historical leaf ranges that have left the live window must still
    be fetchable at download time; CDN purge of evicted bytes before download
    surfaces as a fragment fetch failure rather than silent omission.

12. **Unchanged paths**: Static SegmentBase/SIDX, hierarchical SIDX,
    SegmentTemplate/List dynamic polling, static multi-period, headers, retry,
    and cancellation for non-dynamic-SIDX paths are preserved.

## Tests

### Protocol (`internal/protocol/dash`)

- `TestDownloadDynamicSIDXSecondSnapshotAddsLeaves`
- `TestDownloadDynamicSIDXPrefixDropRejected`
- `TestDownloadDynamicSIDXPrefixReorderRejected`
- `TestDownloadDynamicSIDXPrefixInsertionRejected`
- `TestDownloadDynamicSIDXPrefixShrinkRejected`
- `TestDownloadDynamicSIDXUnchangedSnapshotsWithinMaxSegments`
- `TestDownloadDynamicSIDXAppendBeyondMaxSegmentsRejected`
- `TestDownloadDynamicSIDXStableIndexRangeAppendPreservesLeafRanges`
- `TestDownloadDynamicSIDXDynamicToStaticTransition`
- `TestDownloadDynamicSIDXUsesMinimumUpdatePeriod`
- `TestDownloadDynamicSIDXUsesConfiguredPollInterval`
- `TestDownloadDynamicSIDXVideoAudioIndependentEvolution`
- `TestDownloadDynamicSIDXInitDeduplicated`
- `TestDownloadDynamicSIDXChangedInitRejected`
- `TestDownloadDynamicSIDXRepresentationDisappeared`
- `TestDownloadDynamicSIDXIgnoresHigherBandwidthSibling`
- `TestDownloadDynamicSIDXTrackPropertyMutationRejected`
- `TestDownloadDynamicSIDXAudioRateMutationRejected`
- `TestDownloadDynamicSIDXMediaURLMutationRejected`
- `TestDownloadDynamicSIDXMixedAddressingRejected`
- `TestDownloadDynamicSIDXDuplicateRepresentationKeyRejected`
- `TestValidateRepresentationMarkers`
- `TestDownloadDynamicSIDXOverlappingEvolutionRejected`
- `TestDownloadDynamicSIDXCumulativeRootBoxBudgetAcrossPolls`
- `TestDownloadDynamicSIDXCumulativeLeafBudgetAcrossPolls`
- `TestDownloadDynamicSIDXCancellationDuringWait`
- `TestDownloadDynamicSIDXCancellationDuringRootFetch`
- `TestDownloadDynamicSIDXCancellationDuringMediaFetch`
- `TestDownloadDynamicSIDXNoDestinationOnFailure`
- `TestDownloadDynamicSIDXRangeHeaderPropagation`
- `TestDownloadDynamicSIDX200FallbackBudget`
- `TestDownloadDynamicSIDXDeterministicRaceOutput`
- `TestEffectiveDynamicSIDXMediaLeafLimit`
- `TestSegmentAccumulatorRespectsFragmentEngineMediaCap`
- `TestValidateDynamicSIDXOutputBudgetRejectsFragmentCapOverflow`
- `TestSegmentAccumulatorMergeFuzzIdentity`
- `FuzzDynamicSIDXAccumulatorMerge`
- `TestDownloadSIDXDynamicSingleSnapshot` (replaces former rejection test)
- `TestAlignLiveWindowPrefixEvictionAndAppend`
- `TestAlignLiveWindowRejectsRewindMutationAndReorder`
- `TestSegmentAccumulatorRollingWindowAppendsOnce`
- `TestSegmentAccumulatorRejectsReplayAfterEviction`
- `TestSegmentAccumulatorRejectsDuplicateWindowIdentity`
- `TestSegmentAccumulatorFullWindowReplacementWithoutSharedIdentity`
- `TestDownloadDynamicSIDXRollingWindowEvictAndAppend`
- `TestDownloadDynamicSIDXRollingWindowPureEvictionKeepsHistory`
- `TestDownloadDynamicSIDXRollingWindowToStaticTransition`
- `TestDownloadDynamicSIDXRollingWindowRelocationRejected`
- `TestDownloadDynamicSIDXRollingWindowRewindRejected`
- `TestDownloadDynamicSIDXRollingWindowRetainedMutationRejected`
- `TestDownloadDynamicSIDXRollingWindowReplayRejected`
- `TestDownloadDynamicSIDXRollingWindowExceedsMaxSegmentsRejected`
- `TestDownloadDynamicSIDXRollingWindowNoDestinationOnFailure`
- `TestDownloadDynamicSIDXRollingWindowDeterministicRaceOutput`
- `TestDownloadDynamicSIDXRollingWindowCancellationDuringWait`
- `FuzzDynamicSIDXRollingWindowMerge`

### Product (`pkg/ytdlp`)

- `TestDASHDownloadLiveOptionsEnableDynamicSIDXPolling`

## Remaining deviations

- Dynamic multi-period SegmentBase/SIDX remains unsupported.
- Mixed selected addressing across representations (SIDX plus
  SegmentTemplate/List) is unsupported in dynamic SIDX mode.
- yt-dlp reference at the pinned commit does not implement dynamic SIDX polling;
  parity is defined by this bounded Go contract and tests, not Python behavior.
- Index fetches remain single-attempt; media fragments use the fragment engine
  retry policy.
- Remote/cross-resource nested indexes are not followed.
- Polling still completes before media download. Leaves that roll out of the
  live SIDX window remain on the download plan and must still be HTTP-fetchable;
  early CDN purge is not compensated by mid-poll media writes.
- Leaf identity does not hash remote bytes; equal-sized absolute-range reuse can
  hide hostile content swaps behind unchanged URL/range metadata.

## Known uncertainties

- Real-world dynamic SIDX manifests that relocate existing leaf byte ranges (not
  just append at new offsets or evict a stable-offset prefix) are rejected as
  overlapping or non-prefix evolution when the relocated ranges collide with
  accumulated history; this is intentional but may exclude some hostile or
  malformed publishers.
- Extreme full-window replacement with entirely novel non-overlapping ranges is
  accepted when no identities are shared. Publishers that rewrite the media
  resource between polls without retaining at least one leaf identity therefore
  rely on the overlap/replay ceilings rather than suffix continuity.
