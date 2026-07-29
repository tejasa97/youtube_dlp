# HLS protocol closure evidence

This evidence closes the normal, playable HLS gaps in the bounded native
downloader. The pinned behavioral reference is read-only
`yt-dlp/yt-dlp@aefce1eea4d0b6bab1ec2bd3beff09bff91a39c8`, principally
`yt_dlp/downloader/hls.py`; its line-oriented handling establishes the
baseline for media sequence, discontinuity, maps, AES-128, ranges, and
fragment ordering. Low-latency continuation tags are documented HLS behavior
outside that pinned downloader's native path, and are implemented as a narrow
extension rather than represented as a Python-oracle result.

The Go implementation parses bounded `EXT-X-SERVER-CONTROL`,
`EXT-X-PRELOAD-HINT`, `EXT-X-RENDITION-REPORT`, and
`EXT-X-DISCONTINUITY-SEQUENCE` metadata. It requests `_HLS_msn` and
`_HLS_part` only when `CAN-BLOCK-RELOAD=YES`; it preserves the selected
manifest URL's query values, retries the canonical URL once only for 400, 404,
or 501 delivery-directive rejection, and never fetches a preload hint itself.
Rendition reports are retained as bounded evidence but never cause a silent
rendition switch.

Live aggregation keys each segment by stream epoch, discontinuity sequence,
media sequence, and part. A wholly decreasing window or an overlapping logical
identity whose URL or byte range changes starts a new epoch; identical sliding
snapshots remain deduplicated. Complete media segments replace only parts in
that same physical identity. This preserves physical media-sequence IV
derivation and prevents reset collisions. Map/key context is carried across a delta snapshot only inside an
epoch. A key declaration plus its parsed live-snapshot identity is a cache
boundary even when its URI repeats, so later key rotation cannot reuse stale
bytes merely because parser-local declaration ordinals restart. A reset clears
inherited crypto state. AES-128 bytes are fetched and validated while their
playlist snapshot is current, retained only in unexported in-memory
segment/map state, and never re-fetched during final assembly or included in
diagnostics.

Safety bounds remain explicit: playlist input and entry counts are capped;
only credential-free HTTP(S) HLS resource URIs are accepted; duplicate
attributes fail closed; blocking reload is bounded by caller context and
`MaxPolls`; preload hints, arbitrary interstitial asset lists, markerless ads,
DRM/unknown key delivery, SAMPLE-AES-CTR, and process-visible authenticated
ffmpeg delegation remain unsupported. Clear-key identity SAMPLE-AES retains
the pre-existing guarded product delegation.

Evidence is in `internal/protocol/hls.TestParseLowLatencyContinuationMetadata`,
`TestParseRejectsHostileOrAmbiguousURIs`,
`TestDownloadLiveUsesBlockingReloadWithoutPrefetchingHint`,
`TestDownloadLiveBlockingReloadFallsBackOnce`,
`TestDownloadLiveSequenceResetUsesNewPhysicalEpoch`, and
`TestDownloadRefetchesKeyAfterSameURIKeyRotation`, plus the deterministic
fixtures under `conformance/media/hls_closure/`.
