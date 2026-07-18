# Phase 2 downloader and protocol lane

This lane supplies the native transport foundation for direct files, media
fragments, Smooth Streaming (ISM), and opt-in external downloader tools.

## Evidence-backed behavior

- Direct files preserve resumable partial metadata, add a bounded response
  size, deterministic exponential retry backoff, and a context-aware sustained
  byte-rate throttle. The throttle admits an injected clock/sleeper for
  deterministic tests.
- Fragment work directories keep a plan hash and SHA-256 artifact manifest.
  A cancelled job resumes only artifacts whose digest matches. Legacy state
  without digest evidence is intentionally re-downloaded. State is bounded,
  strictly decoded, atomically written, and rejects symlinks. The absolute
  10,000-artifact cap fits within the 4 MiB manifest bound; writes reject an
  encoded manifest that would exceed that bound.
- Fragment concurrency has both global and per-host limits, bounded segment
  count/size, categorized retryability, ordered atomic assembly, and
  cancellation preservation.
- ISM parses `SmoothStreamingMedia`, selects the best quality in each stream
  type, expands timeline repeats with a hard 10,000-segment cap, addresses fragment URLs, and
  uses the native fragment engine. Multi-track ISM output remains explicitly
  `MergeRequired` for the media pipeline.
- The external adapter receives a structured executable and argv, validates
  scheme/host and bounded arguments, never invokes a shell, redacts diagnostic
  output, and categorizes unavailable tools, invalid input, failure, and
  cancellation.

## Explicit deviations

- The external process uses Go's `exec.CommandContext`, which reliably stops
  the direct child on supported platforms. Recursive process-group teardown is
  intentionally not claimed here; the postprocessing lane owns process-tree
  supervision. The adapter never launches a shell or interpolates arguments.
- ISM downloads fMP4 fragments and exposes track/merge requirements. Native
  PIFF header synthesis and final container merge belong to postprocessing,
  not the network downloader.

See `conformance/downloader/PROVENANCE.md` and
`conformance/media/ism/PROVENANCE.md` for the pinned-reference provenance.
