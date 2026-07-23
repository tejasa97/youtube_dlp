# Phase 2 downloader and protocol lane

This lane supplies the native transport foundation for direct files, media
fragments, Smooth Streaming (ISM), and opt-in external downloader tools.

## Evidence-backed behavior

- Direct files preserve resumable partial metadata, add a bounded response
  size, deterministic exponential retry backoff, bounded transient file
  operation retries, and a context-aware sustained byte-rate throttle. An
  optional bounded slow-response detector restarts a throttled response
  resumably. Native retry/throttle paths admit injected clock/sleeper hooks
  for deterministic tests.
- Final media destinations must be regular files. On Windows an existing
  destination is preserved when native rename cannot atomically replace it,
  even with overwrite selected; callers must choose a new destination or
  explicitly remove the old file before retrying.
- Fragment work directories keep a plan hash and SHA-256 artifact manifest.
  A cancelled job resumes only artifacts whose digest matches. Legacy state
  without digest evidence is intentionally re-downloaded. State is bounded,
  strictly decoded, atomically written, and rejects symlinks. The absolute
  10,000-artifact cap fits within the 4 MiB manifest bound; writes reject an
  encoded manifest that would exceed that bound.
- Fragment concurrency has both global and per-host limits, bounded segment
  count/size, categorized retryability, ordered atomic assembly, and
  cancellation preservation.
- Native HLS suppresses media and low-latency parts attributed by the pinned
  Anvato and Uplynk ad-marker state machine while preserving physical
  sequence identities for live and delta reconciliation.
- ISM parses `SmoothStreamingMedia`, selects the best quality in each stream
  type, expands timeline repeats with a hard 10,000-segment cap, addresses fragment URLs, and
  uses the native fragment engine. Multi-track ISM output remains explicitly
  `MergeRequired` for the media pipeline.
- The external adapter receives a structured executable and argv, validates
  scheme/host and bounded arguments, never invokes a shell, redacts diagnostic
  output, and categorizes unavailable tools, invalid input, failure, and
  cancellation.
- The public API and CLI expose the bounded rate, retry, throttling, file retry,
  fragment concurrency/size/count, ISM, and external-downloader controls.
  Selected-format HTTP headers are propagated to direct files and to HLS,
  DASH, and ISM manifests, keys, and fragments.

## Explicit deviations

- The external process uses Go's `exec.CommandContext`, which reliably stops
  the direct child on supported platforms. Recursive process-group teardown is
  intentionally not claimed here; the postprocessing lane owns process-tree
  supervision. The adapter never launches a shell or interpolates arguments.
- The generic external-downloader argv contract does not synthesize
  tool-specific HTTP-header flags. Callers that need protected media must pass
  the relevant tool's explicit header arguments themselves; native protocols
  remain the preferred header-aware path.
- ISM downloads fMP4 fragments and exposes track/merge requirements. Native
  PIFF header synthesis and final container merge belong to postprocessing,
  not the network downloader.
- HLS ad suppression is limited to the documented attributed Anvato and Uplynk
  markers. It does not guess SCTE-35, cue/date-range, asset-list, or
  markerless server-side ad insertion.

See `conformance/downloader/PROVENANCE.md` and
`conformance/media/ism/PROVENANCE.md` for the original lane provenance. See
`docs/HLS_AD_FRAGMENT_SUPPRESSION_EVIDENCE.md` for the later HLS extension.
