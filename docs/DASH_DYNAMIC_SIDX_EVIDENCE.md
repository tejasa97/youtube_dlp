# DASH Dynamic SegmentBase / SIDX Evidence

## Scope

Bounded single-period dynamic MPD refresh, SegmentBase `indexRange` / SIDX
expansion, append-only accumulation, and download for the native Go DASH
downloader.

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

This Go implementation therefore extends the product with bounded append-only
dynamic SIDX polling while using the reference only to confirm that static
indexRange fixtures exist and that live DASH remains out of scope for yt-dlp
itself.

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

4. **Append-only accumulation contract**: Because all MPD/SIDX snapshots are
   polled before any media download, each refreshed snapshot's media leaf
   sequence must preserve the entire prior snapshot as an exact ordered prefix
   (same URL, range start, range length metadata). Snapshots may append new
   suffix leaves or remain unchanged. Dropped prefixes, reordering, insertion
   before the end, exact-range reuse in a different position, shrink, and
   rolling-window evolution all fail closed with `ErrUnsupportedAddressing`
   before publication. Rolling windows are a known unsupported deviation.
   Append-only identity compares URL/range metadata only; it does not verify
   remote byte content behind an unchanged URL/range between polling and later
   media download.

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
     only; unchanged snapshots do not re-consume this budget. The effective media
     leaf ceiling is `min(configured MaxSegments, 10_000 - initCount)` so the
     shared fragment engine hard total-fragment cap (10,000 including init) is
     never exceeded. Violations fail during polling or final validation with
     `ErrTooManySegments`, not at fragment download.
   - **Parser work** (separate): index bytes transferred 16 MiB
     (`maxCumulativeIndexBytes`), parsed SIDX boxes 256
     (`maxSIDXBoxesPerRepresentation`), cumulative leaf discoveries bounded by
     `maxSegmentsPerRepresentation`, recursion depth 8 (`maxSIDXDepth`), per-request
     index fetch 16 MiB (`maxIndexRangeBytes`).
   - HTTP 200 fallback reads count toward the cumulative index-byte budget.

9. **Stop conditions**: Transition to static MPD includes the final snapshot and
   stops polling. Remaining dynamic after the snapshot budget downloads the
   accumulated bounded window.

10. **Rejections**: Representation disappearance, ambiguous duplicate keys,
    identity/codec/media-URL mutation, non-prefix evolution, overlapping
    byte-range suffixes, malformed `Content-Range`, cyclic nested SIDX, changed
    initialization, mixed addressing, invalid marker sets, and budget exhaustion
    remain fail-closed with `ErrUnsupportedAddressing` (or categorized fragment
    errors for media).

11. **Atomic publication**: Manifest polling and SIDX expansion complete before
    any final media write; existing fragment-engine cleanup on later failure is
    unchanged.

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

### Product (`pkg/ytdlp`)

- `TestDASHDownloadLiveOptionsEnableDynamicSIDXPolling`

## Remaining deviations

- Dynamic multi-period SegmentBase/SIDX remains unsupported.
- Rolling-window SIDX evolution (dropping prior prefix leaves) is unsupported
  because media ranges are not downloaded until after polling completes.
- Append-only prefix checks compare URL/range metadata only; remote byte mutation
  behind an unchanged URL/range between polling and media download is not
  detected.
- Mixed selected addressing across representations (SIDX plus
  SegmentTemplate/List) is unsupported in dynamic SIDX mode.
- yt-dlp reference at the pinned commit does not implement dynamic SIDX polling;
  parity is defined by this bounded append-only Go contract and tests, not Python
  behavior.
- Index fetches remain single-attempt; media fragments use the fragment engine
  retry policy.
- Remote/cross-resource nested indexes are not followed.

## Known uncertainties

- Real-world dynamic SIDX manifests that relocate existing leaf byte ranges (not
  just append at new offsets) are rejected as overlapping evolution; this is
  intentional but may exclude some hostile or malformed publishers.
