# Phase 1 Implementation Plan: Risk-Retirement Pilot

## 1. Objective

Retire the technical risks that could invalidate a Python-free Go port before
scaling extractor coverage. Phase 1 extends the Phase 0 walking skeleton through
adaptive streaming, media processing, JavaScript challenge execution, browser
pressure, authentication, representative production extractors, differential
comparison, and portable plugin boundaries.

This phase is a pilot, not a broad parity release. A capability is compatible
only with its declared pilot corpus and automated evidence. Unsupported sites,
options, manifests, browsers, and plugin behaviors remain explicit.

## 2. Gate G1 outcome

Phase 1 is complete only when all of the following are true:

1. Direct HTTP, HLS VOD/live, and DASH paths complete without Python.
2. ffmpeg/ffprobe outputs pass deterministic end-to-end checks.
3. A bounded JavaScript helper executes the selected EJS and YouTube challenge
   corpus without Python packages.
4. A selected impersonating transport passes a pinned protected-flow fixture
   and a separately controlled live canary.
5. One Chromium-family cookie store can be imported on macOS with explicit user
   authorization and secrets redacted from diagnostics.
6. The pilot extractor set covers simple, playlist, live, authenticated,
   manifest-heavy, and anti-bot risks.
7. Differential fixtures have no unreviewed critical semantic differences from
   the pinned yt-dlp reference.
8. RPC and WASM plugin spikes demonstrate cancellation, version negotiation,
   resource limits, and cross-platform viability.
9. An upstream-delta replay demonstrates that eight weeks of reference changes
   can be classified and absorbed without replacing core boundaries.
10. Product processes and release artifacts pass the Python-free gate.

Live canaries are evidence of current interoperability, not deterministic test
dependencies. Offline fixtures remain authoritative in CI.

## 3. Work packages

### P1-01: Differential conformance runner

Deliverables:

- Define a normalized comparison document for metadata, formats, playlists,
  events, filenames, and output checksums.
- Compare checked-in Go results with attributable oracle fixtures captured from
  the pinned yt-dlp reference.
- Support exact, ordered, set-like, ignored, and tolerance-based field rules.
- Emit a machine-readable diff and a concise review report.
- Never invoke Python from the product binary or Python-free test job.

Acceptance criteria:

- The runner detects missing/null, order, numeric, URL-redaction, and format
  selection differences.
- Every ignore/tolerance rule has a reason and capability owner.
- Oracle fixture provenance follows `docs/FIXTURE_POLICY.md`.

### P1-02: HLS protocol core

Deliverables:

- Parse master and media playlists, including relative URLs, byte ranges,
  initialization maps, discontinuities, AES-128 key references, and end lists.
- Select variants through the shared format-selection model.
- Download VOD fragments concurrently with bounded memory and deterministic
  assembly.
- Poll live/event playlists with context cancellation, de-duplication, and a
  documented stop policy.
- Emit fragment-level structured events and preserve resumable state.

Acceptance criteria:

- Offline fixtures cover master/VOD/live, redirects, transient failures,
  sequence changes, byte ranges, discontinuities, and encrypted segments.
- Cancellation leaves documented resumable state and no finalized corrupt file.
- Unsupported encryption or low-latency features fail explicitly.

### P1-03: DASH protocol core

Deliverables:

- Parse static and dynamic MPDs with BaseURL inheritance.
- Support SegmentTemplate with number/time timelines, SegmentList, initialization
  segments, and separate audio/video representations.
- Download and validate representations through the shared fragment engine.
- Produce merge jobs rather than embedding ffmpeg behavior in the parser.

Acceptance criteria:

- Offline fixtures cover static MPDs, dynamic updates, timelines, number
  templates, representation selection, retry, cancellation, and missing data.
- Selected representation metadata is deterministic and comparable to oracle
  fixtures.

### P1-04: ffmpeg and ffprobe supervision

Deliverables:

- Discover configured and PATH tools and verify versions/capabilities.
- Invoke argument vectors without a shell and bind child lifetime to context.
- Bound stderr, parse progress, redact sensitive arguments, and categorize
  missing-tool and media failures.
- Probe, merge audio/video, remux, and generate deterministic test output.

Acceptance criteria:

- Tests use generated license-safe media and skip with an explicit reason only
  when ffmpeg is unavailable in a developer environment.
- CI installs/pins ffmpeg for the media job.
- Cancellation terminates the process tree and temporary outputs are cleaned.

### P1-05: Compatibility parser prototypes

Deliverables:

- Extend format selection with best/worst, audio/video predicates, fallback,
  merge, and simple filters.
- Extend output templates with numeric/string formatting, alternatives,
  replacement/default traversal, and date/numeric conversions required by the
  pilot corpus.
- Keep both parsers independent from CLI parsing and extraction.

Acceptance criteria:

- Differential golden tests cover the selected yt-dlp syntax corpus.
- Unsupported syntax reports its exact source span and never silently changes
  selection or filenames.
- Both parsers have bounded fuzz targets.

### P1-06: Isolated JavaScript and EJS pilot

Deliverables:

- Define the versioned helper protocol and engine-neutral request model.
- Implement a supervised helper with time, memory, input, output, and module
  limits and no ambient filesystem/network access.
- Execute the pinned EJS corpus and selected YouTube challenge functions.
- Cache compiled scripts by content hash without sharing mutable execution state.

Acceptance criteria:

- Timeout, cancellation, malformed scripts, infinite loops, oversized output,
  and helper crashes are deterministic categorized failures.
- The YouTube challenge corpus matches recorded expected outputs.
- No Python executable, package, or library is present in the runtime path.

### P1-07: Impersonating transport pilot

Deliverables:

- Add a named network-profile request to the transport contract.
- Evaluate maintained TLS/HTTP fingerprint candidates against the ADR criteria.
- Implement one selected Chromium-like profile behind an explicit capability.
- Pin protected-flow fixtures and maintain a manually controlled live canary.

Acceptance criteria:

- Native and impersonated transports share cookies, cancellation, redaction,
  page bounds, and proxy policy without sharing unsafe mutable state.
- Unavailable profiles return a capability error rather than falling back
  silently.
- Fingerprint/profile versions are visible in diagnostic metadata.

### P1-08: Chromium cookie import on macOS

Deliverables:

- Locate explicitly selected Chromium-family profiles.
- Copy locked databases before reading and query the required schema read-only.
- Integrate macOS Keychain authorization and the selected encryption version.
- Normalize cookies into the operation jar without logging values.

Acceptance criteria:

- Synthetic databases cover schema variants, expiry, host-only/domain, secure,
  SameSite, decryption failure, and locked database behavior.
- Real-profile access is opt-in and never runs in CI.
- Imported secrets are zeroed where practical and excluded from events/errors.

### P1-09: Representative extractor pilots

The initial target set is risk-based and can change only through a recorded
decision preserving equivalent coverage:

- Generic/direct media and embeds.
- YouTube for EJS, playlists, live, and rapid reference change.
- Vimeo for manifest-heavy media.
- Twitch for live behavior.
- SoundCloud for API/playlist behavior.
- TikTok for impersonation/anti-bot pressure.
- One synthetic authenticated extractor used in deterministic CI.
- One region-specific extractor selected after transport/cookie evidence.

Deliverables:

- Reusable extractor helpers rather than site-specific copies of transport,
  JSON traversal, pagination, or manifest parsing.
- Deterministic sanitized fixtures and capability entries per pilot path.
- Playlist/lazy entry model with explicit cancellation and error policy.

Acceptance criteria:

- Each pilot has success, malformed, unavailable, and expected-authentication
  failures.
- Production canaries are isolated from deterministic CI.
- No pilot requires Python in the product process.

### P1-10: Plugin architecture spikes

Deliverables:

- Implement a versioned process/RPC handshake and one extractor plugin example.
- Implement a WASM host spike with fuel/time/memory limits and one equivalent
  example where the runtime permits.
- Propagate cancellation and structured errors; reject incompatible versions.
- Document permissions, secret transfer, discovery, signing, and update risks.

Acceptance criteria:

- Host survives malformed messages, crash, timeout, and oversized output.
- Examples run on Linux, macOS, and Windows CI where supported.
- The exit review selects a primary Phase 2 architecture or records a bounded
  blocking experiment.

### P1-11: Upstream-delta replay

Deliverables:

- Inventory eight consecutive weeks after the pinned reference commit.
- Classify extractor, transport, challenge, CLI, downloader, and postprocessor
  changes.
- Replay relevant changes against pilot abstractions and record required edits.
- Measure time-to-classify, affected packages, and fixture churn.

Acceptance criteria:

- No change class requires an undocumented Python bridge.
- Cross-cutting changes have a reusable home rather than being copied into each
  extractor.
- The G1 review has evidence for the projected maintenance load.

## 4. Milestones and commit policy

### M1: Streaming and media foundation

P1-02, P1-03, and P1-04. Commit parser models separately from fragment transfer
and ffmpeg integration. Gate on offline HLS/DASH end-to-end checksums.

### M2: Compatibility parsers and differential runner

P1-01 and P1-05. Commit grammar/AST, evaluator, fixtures, and reports in
reviewable changes. Gate on the pinned syntax corpus and fuzzing.

### M3: JavaScript and YouTube proof

P1-06 plus the YouTube subset of P1-09. Commit the helper protocol before any
engine implementation. Gate on the offline challenge corpus before live canary
work.

### M4: Browser pressure and authentication

P1-07 and P1-08 plus protected pilot extractors. Gate on deterministic profile
and cookie fixtures; live checks remain separately controlled.

### M5: Extractor breadth and plugins

Complete P1-09 and P1-10. Gate on risk-category coverage and host fault
isolation.

### M6: Delta replay and G1 review

Complete P1-11, the Python-free product audit, conformance review, and Phase 1
exit document. No milestone is squashed into a single unreviewable phase commit.

## 5. Dependency order

```text
fragment engine -> HLS -----------+
                 -> DASH ----------+-> ffmpeg merge -> manifest-heavy/live pilots

comparison model -> parser corpus -> extractor differential reports

JS helper protocol -> EJS engine -> YouTube challenge -> YouTube pilot

transport profiles -> impersonation --+
cookie import --------------------------+-> authenticated/anti-bot pilots

event/error/lifecycle contracts -> RPC/WASM host -> plugin examples
```

## 6. Testing and security gates

- Unit, fixture integration, subprocess E2E, race, and fuzz tests remain
  mandatory for every milestone.
- Network-facing tests use bounded bodies, explicit timeouts, redacted secrets,
  and no account credentials in repository fixtures.
- Manifest and script parsers have input-size/depth limits before fuzz claims.
- Fragment and helper concurrency is bounded and cancellation-tested.
- External processes never use a shell and receive the minimum environment.
- Cookie and plugin permissions are opt-in and capability-scoped.
- CI continues to build the final product in an environment without Python and
  run it from a minimal runtime image with CA roots only.

## 7. Definition of done

- Every Phase 1 capability is `compatible`, `intentional_deviation`, or has a
  G1-blocking explanation; none is vaguely `partial`.
- All G1 requirements have named evidence in the capability manifest.
- The Phase 1 exit review records deterministic evidence separately from live
  canary observations.
- Linux, macOS, and Windows jobs pass for portable product paths.
- Python-free Docker tests include streaming, JS helper, extractor, and plugin
  pilot paths.
- Phase 2 can scale coverage without replacing the value, transport, event,
  downloader, process, JavaScript, or plugin boundaries.
