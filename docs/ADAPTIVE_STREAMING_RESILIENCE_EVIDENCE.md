# Adaptive streaming resilience v2

## Scope and provenance

This document records the product integration around the merged HLS group and
DASH dynamic-MPD protocol APIs. Evidence uses only repository-owned scripted
HTTP fixtures; it does not use live or copyrighted media.

The HLS protocol layer owns parsing, map/key state, low-latency de-duplication,
retry, cancellation, and `SelectedDiscontinuityGroup`. The DASH protocol layer
owns dynamic-MPD validation, bounded polling/SIDX expansion, and
`ErrDynamicMPDUnsupported`. The product layer in `pkg/ytdlp` only exposes those
decisions through ordinary format selection, one selected HLS group, the
approved CLI flag, event redaction, and the existing output transaction.

## HLS selection behavior

`--hls-split-discontinuity` preserves ordinary format selection first, then
discovers groups only from the selected HLS representation. It pins the native
HLS downloader to the first eligible group in playlist order, so it never
implicitly downloads every group and never probes unselected renditions. A
single selected non-empty group keeps the existing destination. Explicit
multi-group product selection is intentionally unsupported in v2; no extra
group-selection flag or public Request API is provided. Empty and
advertisement-only groups are not selectable and cannot create media, sidecars,
or archive records. An ordinary selector that produces a merged output plan
with more than one HLS native track is rejected as unsupported before either
representation is probed; separate audio/video group identities are not
inferred or synchronized.

The selected HLS representation remains responsible for all protocol work:
AES-128 maps and keys, low-latency parts, retry bounds, cancellation, and
credential/header isolation. Group labels, progress messages, format IDs, and
archive state never derive from signed URL queries. External downloaders remain
outside authenticated media transport boundaries.

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
all media and sidecars and leaves the archive unchanged. Archive records are
written only after every selected group commits.

Product evidence:

- `pkg/ytdlp.TestProductHLSSplitDiscontinuitySelectsOnlySelectedRepresentation`
- `pkg/ytdlp.TestProductHLSSplitDiscontinuityRejectsMergedHLSRepresentations`
- `pkg/ytdlp.TestProductHLSSplitDiscontinuityRollbackArchiveAndRedactedProgress`
- `pkg/ytdlp.TestProductDASHDynamicMPDPolicyCategories`
- `internal/cli.TestAdaptiveStreamingCLIFlagsAndNegativeAliases`

Protocol resilience evidence remains in the HLS and DASH parity entries and
their fixture provenance documents. Together these tests cover selected-group
output, selected-representation probing, transaction cleanup/archive state,
redacted group-aware progress, map/key and low-latency de-duplication,
cancellation/retry bounds, dynamic SIDX and multi-period boundaries, and
credential/header isolation. Explicit multi-group output and deterministic
`.d<sequence>` naming are deferred because v2 deliberately does not expose a
multi-group selector.
