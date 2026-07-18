# Phase 2 Implementation Plan: Native Foundation and Alpha

Status: Implementation complete; Gate G2 awaits external Windows native-run evidence
Date: 2026-07-18  
Behavioral reference: `yt-dlp/yt-dlp@aefce1eea4d0b6bab1ec2bd3beff09bff91a39c8`

## 1. Objective and compatibility promise

Phase 2 turns the Phase 1 risk-retirement pilots into an installable alpha. It
stabilizes the public Go boundary, implements the principal persisted user
workflows, promotes the plugin experiment into a secured ABI, proves signed
updates and rollback, broadens media behavior, and reaches at least 25 native
representative extractors spanning every major risk class.

The alpha promise is behavioral and capability-scoped. A manifest entry is
`compatible` only for its named corpus and passing evidence. Unsupported
syntax, sites, platforms, protocols, and side effects remain explicit. Python
source compatibility and legacy Python-plugin loading are not promised.

The pinned checkout is a read-only behavioral source. Reference-derived
fixtures record the commit, source locations, derivation, sanitization, and
license. Product, build, release, plugin, and updater paths must not read that
checkout or invoke Python.

## 2. Gate G2

Phase 2 exits only when all of the following are true:

1. All declared core and compatibility tests pass without a Python runtime.
2. The Python reference is used only as a read-only behavioral source or an
   explicitly isolated fixture oracle, never by product/build/release paths.
3. Every temporary fallback is instrumented and has an owner and removal
   milestone; silent fallback is forbidden.
4. No critical credentials, updater, plugin, external-command, archive, cache,
   signature, or filesystem security finding remains open.
5. Alpha artifacts build, install, update, roll back, and run across the
   primary Linux, macOS, and Windows matrix.
6. At least 25 representative native extractors cover simple/direct,
   shared-backend, playlist/API, live, authenticated, manifest-heavy,
   anti-bot/impersonated, regional, and JavaScript-challenge risks.

Deterministic offline evidence is authoritative. Live canaries are opt-in
interoperability evidence and cannot independently support compatibility.

## 3. Ownership and integration rules

The primary agent exclusively owns `pkg/ytdlp/**`, `internal/cli/**`, shared
registries, public contracts, `conformance/parity_manifest.yaml`, `cmd/**`
unless an example is explicitly delegated, CI/release workflows, this plan,
security/trust decisions, and the Phase 2 exit review.

Subagents receive disjoint package/fixture ownership, work in isolated Git
worktrees on `codex/*` branches, and create coherent commits. The primary
reviews and cherry-picks sequentially, performs shared integration, and updates
claims only after evidence passes. No lane edits another lane's files or the
primary-owned integration surface.

## 4. Work packages

### P2-01: Public API and compatibility policy

Deliverables:

- stabilize request/result/event/error, streaming playlist, hook, credential,
  archive/cache, postprocessor, plugin, and update-facing contracts;
- publish semantic-version and compatibility/deprecation policy;
- make fallback/capability decisions observable and categorized;
- retain context cancellation across every public operation.

Acceptance and evidence:

- API compile/behavior tests, concurrent-operation and cancellation tests;
- golden error/event serialization and compatibility-version tests;
- an inventory assigning every fallback an owner and removal milestone.

Owner: primary. Depends on Phase 1 foundations; gates all product integration.

### P2-02: Configuration and CLI compatibility

Deliverables:

- typed configuration discovery and merge API;
- yt-dlp-compatible principal default locations, explicit files, encoding,
  comments, quoting/escaping, aliases, repeated options, and precedence;
- CLI integration with deterministic diagnostics and cancellation;
- exact fixtures for output, warnings, exit categories, and side effects.

Acceptance and evidence:

- table/property tests for discovery and precedence on all primary platforms;
- bounded tokenizer/parser fuzzing and exact source-location errors;
- malformed encoding, unsafe path, recursion, cancellation, and secret-safe
  diagnostic tests;
- reference-derived corpus with provenance.

Owners: configuration lane owns `internal/compat/config/**` and fixtures;
primary owns CLI/API integration. Depends on P2-01.

### P2-03: Download archive and cache compatibility

Deliverables:

- compatible archive identity parsing/matching and migration;
- atomic append/update, cross-process locking, duplicate prevention, corruption
  handling, cancellation, and crash recovery;
- bounded namespaced cache with expiry, lookup, atomic store, removal, and
  platform-safe paths;
- product-flow integration and observable archive/cache decisions.

Acceptance and evidence:

- property and concurrency tests for idempotence, locking, and migration;
- malicious path/symlink, truncation, partial-write, oversized-record, clock,
  cancellation, and corruption tests;
- cache/archive fuzz targets and no-secret diagnostics;
- end-to-end skip/update/cache-hit product tests.

Owners: archive lane owns `internal/archive/**`, `internal/cache/**`, and
fixtures; primary owns product integration. Depends on P2-01.

### P2-04: Portable cookies and credentials

Deliverables:

- Netscape/Mozilla cookie-file read/write compatibility;
- Firefox profile discovery/import, containers, locked SQLite copies, schema
  variants, expiry and domain/path semantics;
- Chromium support across macOS plus the feasible Windows DPAPI and Linux
  Secret Service/KWallet/AES flows, with unsupported stores explicit;
- typed credential-source boundary without secret-bearing events/errors.

Acceptance and evidence:

- synthetic databases/files for every claimed platform and schema;
- locked-database, partial decrypt, wrong key, unsafe profile, cancellation,
  zeroization-where-practical, redaction, and fuzz tests;
- no automated access to a developer's real profile;
- no-cgo platform builds and opt-in real-profile canaries only.

Owners: cookie lane owns new `internal/cookies/**` packages and fixtures;
primary owns public/CLI integration. Depends on P2-01 and shared transport.

### P2-05: Principal compatibility languages

Deliverables:

- expanded format selection, filtering, sorting, merge/fallback and preferences;
- output, progress, metadata parsing/replacement, and match-filter languages;
- bounded ASTs/evaluators independent of CLI parsing;
- compatible missing/null, escaping, numeric/date, traversal and rejection
  semantics for the declared corpus.

Acceptance and evidence:

- attributable differential golden corpora;
- exact source-span diagnostics and explicit unsupported syntax;
- property tests for parse/render stability and ordering;
- size/depth/complexity limits and fuzz targets for every parser.

Owner: compatibility-language lane owns new/subpackage implementations and
fixtures; primary owns product/CLI wiring. Depends on P2-01 and P2-02.

### P2-06: Downloader and protocol breadth

Deliverables:

- rate limits, retry policy, throttling detection, file-access retry,
  per-host fragment controls, and artifact manifests;
- cancellation-safe resume and deterministic retry telemetry;
- ISM support and a typed external-downloader boundary; add other roadmap
  protocols only with deterministic usable-media evidence;
- platform-correct external process cancellation and missing-tool behavior.

Acceptance and evidence:

- virtual-clock/property tests for pacing/backoff and host isolation;
- cancellation, partial-state, retry exhaustion, filesystem contention,
  malformed manifest, and command-injection tests;
- generated-media end-to-end checksum/semantic verification;
- fuzzed bounded parsers and product dispatch tests.

Owner: downloader lane owns `internal/downloader/**`, new protocol packages and
fixtures; primary owns API/CLI/events. Depends on Phase 1 fragment/media core.

### P2-07: Core postprocessors

Deliverables:

- typed audio extraction/conversion, subtitle/thumbnail/metadata embedding,
  fixups, concat, chapters, and file-move operations;
- deterministic postprocessor graph, side-effect/artifact reporting, and
  overwrite policy;
- shell-free ffmpeg/ffprobe commands with atomic finalization and cancellation.

Acceptance and evidence:

- license-safe generated fixtures with semantic ffprobe assertions;
- bounded/redacted diagnostics, hostile metadata/path, missing tool, partial
  output, cancellation/process-tree, atomicity, and rollback tests;
- no user-derived shell evaluation.

Owner: postprocessing lane owns `internal/media/**` additions and fixtures;
primary owns product/API/CLI integration. Depends on P2-01 and Phase 1 ffmpeg.

### P2-08: Plugin ABI v1

Deliverables:

- promote RPC to a documented extractor/postprocessor/provider ABI v1;
- explicit secure discovery, SDK types, compatibility negotiation, permission
  approval, packaging, cancellation, structured errors, and crash isolation;
- compatible v1-to-v1.x upgrade test; keep WASM as a constrained secondary ABI;
- reject Python runtime declarations.

Acceptance and evidence:

- malformed/crashed/hung/oversized/permission-change/version-upgrade tests;
- writable-path, environment/argument secret, process-tree, and redaction tests;
- Linux/macOS/Windows example builds and exchanges;
- fuzzed protocol/manifest decoders and a published compatibility policy.

Owner: plugin ABI lane owns `internal/plugin/**`, SDK, fixtures, and explicitly
delegated example commands. Primary owns trust policy and product integration.
Depends on P2-01 and Phase 1 RPC/WASM spikes.

### P2-09: Signed packs and sandbox policy

Deliverables:

- signed extractor/plugin pack manifest and deterministic package format;
- signature/hash verification, path safety, rollback/revocation metadata, and
  permission-change review;
- explicit trusted discovery roots and platform sandbox adapters where safely
  feasible;
- threat model for hostile packages, writable paths, secret handles and
  sandbox gaps.

Acceptance and evidence:

- deterministic test keys only; no production key selection or creation;
- tamper, traversal, symlink, duplicate, downgrade, rollback, expiration,
  oversized archive, permission escalation and unsupported sandbox tests;
- atomic install/removal and cross-platform path tests.

Owner: signed-pack lane owns a new pack/sandbox subpackage and fixtures; primary
owns trust decisions and integration. Depends on P2-08.

### P2-10: Signed updater, releases, reproducibility and SBOM

Deliverables:

- signed update metadata and channels, locking, atomic install, health check,
  rollback and failure recovery;
- deterministic test-key ceremony and documented external production ceremony;
- reproducible no-cgo builds, alpha archives, checksums, licenses and SBOM;
- install/update/rollback/run verification on Linux, macOS and Windows.

Acceptance and evidence:

- tamper, downgrade/freeze, wrong channel/platform, path, partial write, lock,
  cancellation, failed-health-check and rollback tests;
- two clean builds compare reproducibly for the declared inputs;
- CI produces and verifies platform artifacts entirely without Python;
- no critical updater/release security finding remains open.

Owner: updater lane owns `internal/update/**`, release tooling packages and
fixtures; primary owns commands, workflows, trust/release policy. Depends on
P2-01 and shares signing primitives/policy with P2-09.

### P2-11: Representative extractor factory

Deliverables:

- at least 25 product-registered extractors total;
- shared hosting/backend helpers before duplicative site code;
- deterministic success/failure/cancellation/routing/playlist/protocol tests,
  fuzzing where appropriate, provenance, and known deviations per extractor;
- lazy bounded playlists and categorized authentication/geo/anti-bot failures.

The planned breadth baseline is the eight Phase 1 extractors plus 17 Phase 2
targets. Substitutions require a recorded decision preserving the same risk
coverage.

| Family | Planned representatives |
| --- | --- |
| Phase 1 retained | Generic, YouTube, Vimeo, Twitch, SoundCloud, TikTok, synthetic authenticated, SVT Play |
| shared hosting/backend | Brightcove, Kaltura, JW Platform, Wistia, SproutVideo |
| high-usage public/API | Dailymotion, Reddit, Twitter/X, Bandcamp, Mixcloud, Rumble, Bilibili |
| auth/live/regional/anti-bot | Instagram, Kick, BBC iPlayer, ARD, NRK |

Acceptance and evidence:

- registry count and risk-class test fails below 25 or on a missing class;
- reference-derived request/result expectations have provenance;
- returned protocols drive usable deterministic downloads where applicable;
- no extractor invokes a Python bridge or silently falls back.

Owners: successive extractor lanes own disjoint extractor files and fixtures;
primary owns registry, manifest, shared API changes and integration. Depends on
the relevant transport/protocol/auth/shared-helper foundations.

### P2-12: Conformance, security and alpha exit

Deliverables:

- expanded differential policies for CLI, config, archives/cache, cookies,
  DSLs, downloads, postprocessing, plugins, updates and extractors;
- fallback inventory, threat-model review and stale-deviation reconciliation;
- Python-free source/dependency/artifact audit;
- Phase 2 exit review mapping every package and G2 criterion to evidence.

Acceptance and evidence:

- formatting, module drift, manifest, unit, race, vet and relevant fuzz gates;
- no-cgo Linux/macOS/Windows builds and install/update/rollback/run tests;
- scratch/container Python-free verification including plugins and updater;
- no unreviewed critical semantic or security difference.

Owner: primary, with lane-owned evidence. Depends on P2-01 through P2-11.

## 5. Dependency and milestone order

```text
P2-01 public policy
  ├── P2-02 config/CLI ──┐
  ├── P2-03 archive/cache├── P2-12 alpha exit
  ├── P2-04 cookies ─────┤
  ├── P2-05 DSLs ────────┤
  ├── P2-06 downloader ──┤
  ├── P2-07 postprocess ─┤
  └── P2-08 ABI ── P2-09 packs ──┐
                   P2-10 updater ─┤
shared foundations ── P2-11 extractors ─┘
```

### M2.1: Native persisted-workflow foundation

P2-01 through P2-04. Wave 1 runs configuration, archive/cache and cookie lanes
in parallel; primary integrates API, CLI, product flow and manifest evidence.

### M2.2: User-visible compatibility and media breadth

P2-05 through P2-07. Wave 2 runs DSL, downloader/protocol and postprocessor
lanes in parallel; primary reconciles events, filenames and side effects.

### M2.3: Trust and release foundation

P2-08 through P2-10. Wave 3 runs plugin ABI, signed-pack/sandbox and updater/
reproducibility lanes; primary owns trust decisions, commands and CI.

### M2.4: Extractor migration waves

After relevant foundations stabilize, three lanes repeatedly implement shared
backends, high-usage public sites, then auth/live/regional/anti-bot sites.
Integration is sequential and the registry gate must reach 25 representatives.

### M2.5: G2 closure

P2-12: resolve stale claims, run the complete platform/security/Python-free
matrix, build alpha artifacts, verify install/update/rollback/run, and commit
the Phase 2 exit review.

## 6. Security gates

No milestone advances with an open critical issue in its surface. Required
reviews are:

- credentials: origin/path scoping, locked copies, platform key access,
  partial-decrypt behavior, memory/log/fixture secret exposure;
- filesystem/archive/cache: traversal, symlink/hardlink, race, lock, atomic
  rename, permissions, corruption, resource exhaustion;
- external commands/postprocessing: no shell, validated argv, process-tree
  cancellation, bounded/redacted diagnostics, atomic outputs;
- plugins/packs: trusted roots, signature/hash before execution, permissions,
  upgrade/downgrade, crash/resource isolation, hostile manifests;
- updater/releases: offline-root policy, threshold/rotation ceremony design,
  expiry/freeze/downgrade, channels, platform targeting, rollback and locks;
- parsers/network/extractors: bounded input/depth/concurrency, cancellation,
  URL/secret redaction, no silent transport/profile fallback.

Deterministic fixture keys are test-only. Selecting real publishers, release
keys, key custodians, transparency infrastructure, or production credentials is
an external operational decision and is not required to prove the repository
mechanisms.

## 7. Continuous verification and commit policy

Each lane commits reviewable increments. The primary cherry-picks one lane at a
time, resolves integration failures, updates shared claims, and commits the
integration separately. At milestone boundaries run:

```text
test -z "$(gofmt -l .)"
go mod tidy -diff
go run ./cmd/paritycheck
go test ./...
go test -race ./...
go vet ./...
all relevant fuzz targets
CGO_ENABLED=0 builds for Linux, macOS and Windows
Python-free Docker build and runtime checks
```

Live canaries remain opt-in. Known deviations state the unsupported behavior,
owner and intended later milestone. Any blocked credential, signing, regional,
legal or infrastructure-dependent case is documented while unaffected work
continues.

## 8. Definition of done

Phase 2 is done only when every P2 package is complete or has an explicit
accepted, non-critical deviation; the compatible manifest claims all pass;
there are at least 25 representative registered extractors covering every risk
class; the security review has no open critical finding; Python-free alpha
artifacts install, update, roll back and run on the primary matrix; and
`docs/PHASE_2_EXIT_REVIEW.md` maps every G2 assertion to concrete automated
evidence.
