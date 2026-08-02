# HLS closure fixture provenance

These deterministic, synthetic media-playlist snapshots are owned by this
repository. They contain no upstream media, credentials, encrypted keys, or
network captures.

The behavioral baseline remains read-only
`yt-dlp/yt-dlp@aefce1eea4d0b6bab1ec2bd3beff09bff91a39c8`, inspected in
`yt_dlp/downloader/hls.py`. It establishes native playlist ordering,
`EXT-X-MEDIA-SEQUENCE`, discontinuities, AES-128 state, byte ranges, and
conservative unsupported-encryption behavior. That pinned downloader has no
native implementation of low-latency `EXT-X-PRELOAD-HINT`,
`EXT-X-RENDITION-REPORT`, or HTTP delivery-directive continuation; those are
bounded Go extensions derived from the HLS specification's low-latency media
playlist tags.

The fixtures prove only these claims:

- a `CAN-BLOCK-RELOAD=YES` playlist can request the next part with generated
  `_HLS_msn`/`_HLS_part` directives while preserving pre-existing manifest
  query values;
- a preload hint is continuation metadata and is never fetched before it
  appears as a declared part or complete segment;
- a server rejecting delivery directives falls back once to the canonical
  playlist URL;
- a decreasing media sequence creates a new physical epoch so old and new
  fragments cannot collide; and
- repeated AES key URIs are fetched per key declaration, not treated as an
  immutable URI-level cache.

The explicit-group product fixture additionally proves that the selected
representation manifest is fetched once for discovery and the initial load
shared by all selected groups. It does not fetch an unselected rendition;
segments and later live playlist reloads remain network-backed and retain the
selected headers and URL policy.

Hostile URI and duplicate-attribute cases are generated inline by the parser
tests so every input remains visibly local and deterministic.

Product integration is covered by local scripted HTTP fixtures in
`pkg/ytdlp/adaptive_streaming_resilience_product_test.go`. The product exposes
the approved `--hls-split-discontinuity` behavior after ordinary format
selection: it discovers groups only for the selected HLS representation and
pins the downloader to the first eligible group. It preserves the normal
single-group destination and keeps maps/keys, retry, cancellation, transaction
cleanup, archive state, and redacted group-aware progress within the existing
boundaries. The repeatable `--hls-discontinuity-sequence` selector and
`Request.HLSDiscontinuitySequences` select absolute IDs only from the one
ordinary-selected representation. Duplicate IDs are deduplicated, playlist
order is authoritative, one explicit group keeps the existing destination, and
multiple groups use transactional `.d<absolute-sequence>` names. Missing,
empty, ad-only, and malformed selections fail before media/artifact/archive
mutation with `invalid_input`; AllowedHosts violations are `security` and
transport failures remain `network`. Merged output plans containing multiple
native HLS tracks are rejected before probing because their absolute
discontinuity identities may differ; the product never synchronizes them.
