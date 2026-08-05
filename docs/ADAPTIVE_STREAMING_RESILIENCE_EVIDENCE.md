# Adaptive streaming resilience v2

## Scope and provenance

This document records the product integration around the merged HLS group and
DASH dynamic-MPD protocol APIs. Evidence uses only repository-owned scripted
HTTP fixtures; it does not use live or copyrighted media.

The HLS protocol layer owns parsing, map/key state, low-latency de-duplication,
retry, cancellation, and `SelectedDiscontinuityGroup`. The DASH protocol layer
owns dynamic-MPD validation, bounded polling/SIDX expansion, and
`ErrDynamicMPDUnsupported`. The product layer in `pkg/ytdlp` exposes those
decisions only after ordinary format selection, through the approved HLS
selectors, event redaction, and the existing output transaction.

## HLS selection behavior

`--hls-split-discontinuity` preserves ordinary format selection first, then
discovers groups only from the selected HLS representation. With no explicit
sequence list it pins the native HLS downloader to the first eligible group in
playlist order and keeps the existing destination. The repeatable
`--hls-discontinuity-sequence` flag and `Request.HLSDiscontinuitySequences`
select absolute sequence IDs from that same bounded group plan: duplicate IDs
are ignored, groups are emitted in playlist order, one explicit group keeps the
existing destination, and multiple groups become transactional output plans
named `.d<absolute-sequence>` before the extension. No extractor format list is
expanded and unselected renditions are never probed. Empty, advertisement-only,
missing, or malformed explicit selections fail before media, sidecar, or
archive mutation with the documented `invalid_input` category; AllowedHosts
violations remain `security`, transport failures remain `network`, and
cancellation remains `cancelled`. An ordinary selector that produces a merged
output plan with more than one HLS native track is rejected as `unsupported`
before either representation is probed; separate audio/video group identities
are not inferred or synchronized.

The selected HLS representation remains responsible for all protocol work:
AES-128 maps and keys, low-latency parts, retry bounds, cancellation, and
credential/header isolation. Group labels, progress messages, format IDs, and
archive state never derive from signed URL queries. External downloaders remain
outside authenticated media transport boundaries.

The bounded selected-representation response used for discovery is also the
initial media-playlist snapshot for every explicitly selected group. Thus a
two-group fan-out fetches the selected manifest once, never fetches an
unselected rendition, and still performs network-backed live polling/reloads
after the initial snapshot. Snapshot state is copied only within the current
media entry and is discarded afterwards.

## DASH policy behavior

`--allow-dynamic-mpd` is the default. `--no-allow-dynamic-mpd` and
`--ignore-dynamic-mpd` pass the explicit DASH deny policy and categorize
`ErrDynamicMPDUnsupported` as the exact unsupported product category. Static
MPDs and already-supported dynamic paths retain their existing behavior.

Dynamic multi-period SegmentBase/SIDX remains fail-closed with the existing
unsupported-addressing boundary; this integration does not claim a parity
acceptance path for it. MPD/index cancellation, bounded retry, no-duplicate
fragment publication, cleanup, and SIDX boundary errors remain owned and
tested by the DASH protocol layer.

## Transaction and evidence

All selected output destinations are preflighted before publication. If any
selected group fails or is cancelled, the existing #205 transaction rolls back
all media and sidecars, removes native HLS fragment scratch, and leaves the
archive unchanged. Archive records are written only after every selected group
commits.

Product evidence:

- `engine.TestProductHLSSplitDiscontinuitySelectsOnlySelectedRepresentation`
- `engine.TestProductHLSSplitDiscontinuityRejectsMergedHLSRepresentations`
- `engine.TestProductHLSSplitDiscontinuityRollbackArchiveAndRedactedProgress`
- `engine.TestProductHLSExplicitDiscontinuitySequencesFanOutInPlaylistOrder`
- `engine.TestProductHLSExplicitSingleDiscontinuityKeepsDestination`
- `engine.TestProductHLSExplicitDiscontinuityRejectsBeforeMediaArtifactAndArchiveMutation`
- `engine.TestProductHLSExplicitDiscontinuityPreflightsCollisionsBeforeMedia`
- `engine.TestProductHLSExplicitDiscontinuityRollsBackPartialFailure`
- `engine.TestProductHLSExplicitDiscontinuityCancellationCleansScratchAndArchive`
- `engine.TestProductHLSExplicitDiscontinuityRejectsInvalidAPIValueBeforeNetwork`
- `internal/protocol/hls.TestDownloadInitialPlaylistSnapshotReusesInitialLoadAndReloadsLive`
- `engine.FuzzDeduplicateHLSDiscontinuitySequences`
- `engine.TestProductDASHDynamicMPDPolicyCategories`
- `internal/cli.TestAdaptiveStreamingCLIFlagsAndNegativeAliases`

Protocol resilience evidence remains in the HLS and DASH parity entries and
their fixture provenance documents. Together these tests cover selected-group
output, selected-representation probing, transaction cleanup/archive state,
redacted group-aware progress, map/key and low-latency de-duplication,
cancellation/retry bounds, initial-snapshot reuse with live reload, dynamic
SIDX and multi-period boundaries, and credential/header isolation. Dynamic
multi-period DASH SegmentBase/SIDX remains fail-closed and unsupported; this
HLS fan-out does not change DASH or the HLS protocol core.
