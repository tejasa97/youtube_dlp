# Phase 0 Implementation Plan: Foundation and Walking Skeleton

## 1. Objective

Build the first end-to-end, Python-free vertical slice of the Go port. At the
end of Phase 0, a user must be able to give the CLI a URL served by the local
fixture server, have a Go extractor produce ordered metadata, select a direct
media format, and download it through the Go HTTP stack with progress,
cancellation, resumability, JSON output, and a minimally compatible output
template.

This phase proves the architecture and delivery loop. It does not claim broad
site or yt-dlp feature parity.

Expected duration: two to four weeks for a small core team. The exit gate is
capability-based rather than date-based.

## 2. Phase 0 success statement

The following command shape works without a Python interpreter installed:

```text
ytdlp-go --output '%(title)s.%(ext)s' --print-json <fixture-url>
```

It must:

1. Parse CLI options and establish a cancellable operation.
2. Fetch a deterministic fixture page through the shared HTTP transport.
3. Select a matching extractor and return normalized, ordered metadata.
4. Choose a direct HTTP media format.
5. Render a safe output filename from a supported template subset.
6. Download to a temporary file, report structured progress, and atomically
   finalize the output.
7. Resume an interrupted range-capable transfer.
8. Emit stable JSON suitable for automated assertions.
9. Pass the Phase 0 test suite in an environment where `python`, `python3`, and
   Python shared libraries are absent.

## 3. Scope

### Included

- Go module, build commands, lint/test conventions, and cross-platform CI.
- A capability/parity manifest with machine-readable status and evidence.
- A typed dynamic value and ordered metadata representation that distinguishes
  missing values from explicit null values.
- Stable Go interfaces for extraction, networking, downloading, events, and
  orchestration.
- Shared HTTP transport with headers, cookies, redirects, proxy configuration,
  timeouts, compression, and request cancellation.
- One synthetic fixture extractor plus a generic direct-media extractor.
- Direct HTTP download with temporary files, range resume, progress, retry
  boundaries, cancellation, and atomic completion.
- A deliberately small, documented output-template subset.
- CLI commands/options required for the walking skeleton.
- Deterministic fixtures, unit tests, integration tests, fuzz targets, and a
  Python-free verification job.
- Short architecture decision records for the high-risk compatibility areas
  that later phases depend on.

### Explicitly deferred

- Production YouTube support and broad extractor migration.
- HLS, DASH, fragmented media, multi-stream merging, and live streams.
- ffmpeg post-processing beyond defining its future process boundary.
- Full yt-dlp CLI, configuration, output-template, archive, and info-JSON
  compatibility.
- Browser impersonation, JavaScript challenge execution, OAuth/device login,
  and browser-cookie import.
- Plugin execution, updater/signing, embedding API stabilization, and release
  packaging.
- Performance optimization not required to meet the Phase 0 acceptance tests.

## 4. Architectural boundaries

Phase 0 should establish boundaries that can survive later feature expansion.
The concrete implementations can remain intentionally small.

```text
CLI
  -> Application/operation lifecycle
       -> Extractor registry
            -> Shared HTTP transport
       -> Format selection
       -> Output template renderer
       -> Downloader
            -> Shared HTTP transport
       -> Event sinks (terminal and JSON)
```

The application layer owns operation sequencing. Extractors describe media;
they do not write files. Downloaders transfer selected resources; they do not
scrape pages. Terminal rendering consumes structured events and must not be
called from core packages.

### Proposed repository layout

```text
cmd/ytdlp-go/                    CLI entry point
pkg/ytdlp/                       supported embedding API
internal/app/                    operation lifecycle and orchestration
internal/value/                  dynamic ordered value model
internal/extractor/              interfaces, registry, and extractors
internal/network/                HTTP transport, cookies, proxy, retry policy
internal/downloader/             direct HTTP downloader
internal/format/                 selection and normalization
internal/compat/template/        output-template compatibility layer
internal/events/                 structured event types and sinks
internal/testserver/             deterministic fixture server
conformance/                     capability manifest and test definitions
docs/adr/                        architecture decision records
testdata/                        small checked-in fixtures
```

`internal/` is the default until an interface is intentionally supported for
external consumers. Only the orchestration surface belongs in `pkg/ytdlp/`.

## 5. Core interface sketches

These sketches constrain responsibilities; exact names may change during the
first implementation review.

```go
type Extractor interface {
    Name() string
    Suitable(rawURL string) bool
    Extract(ctx context.Context, request ExtractRequest) (Info, error)
}

type Transport interface {
    Do(ctx context.Context, request *http.Request) (*http.Response, error)
}

type Downloader interface {
    Download(ctx context.Context, job DownloadJob, sink EventSink) (DownloadResult, error)
}

type EventSink interface {
    Emit(context.Context, Event) error
}

type Client interface {
    Run(ctx context.Context, request Request) (Result, error)
}
```

All blocking APIs accept `context.Context`. Errors must retain a stable category
for CLI exit codes and automation while wrapping the underlying cause. Progress
is event data, not terminal text.

## 6. Data-model decisions

The metadata model must not be based solely on `map[string]any`. yt-dlp behavior
depends on ordered fields, heterogeneous values, and the distinction between a
missing field and a field whose value is null.

Phase 0 will implement:

- A closed set of dynamic value kinds: missing, null, boolean, integer,
  floating point, string, bytes where required internally, list, and ordered
  object.
- Deterministic JSON encoding with documented numeric behavior.
- Ordered object lookup without losing insertion order.
- Copy/merge operations with explicit overwrite and missing-value rules.
- A typed `Info` wrapper for frequently accessed fields such as ID, title,
  extension, webpage URL, and formats while preserving additional fields.

Before package consumers multiply, tests must lock down null/missing behavior,
field order, numeric round trips, merges, and JSON output.

## 7. Work packages

### P0-01: Repository and toolchain baseline

Deliverables:

- Initialize the Go module and choose the minimum supported Go version.
- Add `make` or a small Go-native task entry point only if it reduces command
  duplication; standard `go` commands remain authoritative.
- Add format, vet, unit-test, race-test, and build checks.
- Establish Linux, macOS, and Windows CI coverage.
- Document contribution, generated-file, fixture-size, and dependency rules.
- Record licenses and provenance for copied or translated fixture material.

Acceptance criteria:

- A fresh clone can build and test using documented commands.
- CI rejects unformatted code, vet failures, test failures, and dependency
  metadata drift.
- The repository contains no runtime Python dependency or generated Python
  artifact.

Depends on: nothing.

### P0-02: Capability manifest and evidence model

Deliverables:

- Define `conformance/parity_manifest.yaml` and a schema/checker.
- Seed it with the Phase 0 capabilities and the major future capability groups.
- Give every entry an ID, compatibility target, status, evidence, owner, and
  known deviation.
- Generate a human-readable status summary from the manifest.

Recommended status values: `not_started`, `partial`, `compatible`,
`intentional_deviation`, and `blocked`.

Acceptance criteria:

- CI rejects invalid status values, duplicate IDs, missing evidence for a
  compatibility claim, and unknown dependencies.
- No document or release can call a capability compatible unless its manifest
  entry links to a passing automated test.

Depends on: P0-01.

### P0-03: Ordered value and metadata model

Deliverables:

- Implement the dynamic value kinds and ordered object.
- Implement deterministic JSON encode/decode.
- Add typed metadata accessors without discarding unknown fields.
- Define format entries and the minimum normalized metadata contract.

Acceptance criteria:

- Unit tests cover each value kind, nested objects, field order, merges,
  missing versus null, and numeric edge cases.
- Fuzzing malformed JSON never panics and produces bounded failures.
- JSON fixtures round-trip according to the documented normalization rules.

Depends on: P0-01.

### P0-04: Application API and lifecycle

Deliverables:

- Define request, result, error-category, and event contracts.
- Implement a context-driven operation lifecycle.
- Implement an extractor registry with deterministic matching priority.
- Keep global mutable state out of the application path.

Acceptance criteria:

- Two operations can run concurrently without sharing mutable request state.
- Cancellation reaches all blocking components.
- Unit tests prove deterministic extractor selection and stable error
  categorization.

Depends on: P0-03. Can be designed in parallel with P0-05.

### P0-05: Shared HTTP transport

Deliverables:

- Build one injectable transport used by both extractors and downloaders.
- Support caller/default headers, redirects, compression, cookies, proxies,
  connect/request timeouts, cancellation, and response-size limits for pages.
- Define retry eligibility without embedding retries invisibly in every call.
- Redact credentials and cookie values in logs and errors.

Acceptance criteria:

- Fixture tests cover redirects, gzip, cookies, headers, range requests,
  cancellation, timeouts, proxy selection, and oversized page responses.
- Bodies are always closed through tested ownership rules.
- Sensitive header values do not appear in captured logs or returned errors.

Depends on: P0-01. Integrates with P0-04.

### P0-06: Fixture and direct-media extractors

Deliverables:

- Implement the extractor interface and registry integration.
- Add a fixture extractor whose page describes deterministic media formats.
- Add a generic direct-media extractor based on URL and response metadata.
- Normalize both into the shared `Info` model.

Acceptance criteria:

- Matching is deterministic when multiple extractors report suitability.
- Expected metadata and format order match golden fixtures.
- Malformed pages, unsupported URLs, HTTP failures, and missing required fields
  produce stable error categories without panics.

Depends on: P0-03, P0-04, P0-05, and P0-10 fixture endpoints.

### P0-07: Direct HTTP downloader

Deliverables:

- Stream to a same-filesystem temporary file and atomically finalize it.
- Resume from validated partial state when the server supports ranges.
- Emit start, progress, retry, completion, cancellation, and error events.
- Define bounded retry and cleanup behavior.
- Reject unsafe destination traversal and unexpected overwrite.

Acceptance criteria:

- Tests cover complete downloads, unknown content length, interruption and
  resume, a server ignoring ranges, changed ETag/size, retryable failures,
  cancellation, existing destinations, and cleanup.
- The final file checksum matches the fixture source.
- Race tests pass while multiple downloads execute concurrently.

Depends on: P0-04 and P0-05.

### P0-08: Minimal output templates and filename safety

Deliverables:

- Support literal text, `%%`, and simple `%(field)s` substitutions for the
  fields used by the walking skeleton.
- Define missing/null rendering for the supported subset.
- Sanitize platform-invalid path characters and prevent path escape.
- Publish the exact Phase 0 subset in the capability manifest.

Acceptance criteria:

- Golden tests cover Unicode, missing fields, explicit null, percent escapes,
  invalid syntax, reserved filenames, and traversal attempts.
- Fuzzing arbitrary templates and metadata never panics or writes outside the
  chosen output directory.
- Unsupported yt-dlp template features fail clearly rather than silently
  producing a misleading name.

Depends on: P0-03.

### P0-09: CLI and structured output

Deliverables:

- Add the `ytdlp-go` executable and wire the Phase 0 operation.
- Implement only the walking-skeleton switches: output path/template,
  print/dump JSON, verbosity, timeout, proxy, overwrite policy, and version.
- Add terminal and newline-delimited JSON event sinks.
- Define exit-code mapping from error categories.

Acceptance criteria:

- Help text labels the implementation experimental and does not imply full
  yt-dlp parity.
- Machine-readable output is not corrupted by progress or diagnostic text.
- SIGINT/SIGTERM cancel the operation and leave resumable state according to
  policy.
- CLI integration tests cover successful, unsupported, network-failure,
  template-failure, cancellation, and resume paths.

Depends on: P0-04, P0-06, P0-07, and P0-08.

### P0-10: Deterministic fixture server and conformance harness

Deliverables:

- Build an in-process HTTP fixture server with page, media, redirect, cookie,
  gzip, range, slow-response, disconnect, and mutation endpoints.
- Generate deterministic media bytes at test time or check in only small,
  license-safe fixtures.
- Create golden metadata, event, and file-checksum assertions.
- Allow tests to run without public internet access.

Acceptance criteria:

- The full Phase 0 suite is deterministic offline.
- Failure injection is seeded or explicitly scripted.
- Test logs identify the fixture scenario and request sequence on failure.

Depends on: P0-01. Endpoint design should run in parallel with P0-05.

### P0-11: High-risk architecture spikes

These are bounded research tasks, not production implementations.

Deliverables:

- ADR: regex compatibility strategy and policy for unsupported constructs.
- ADR: JavaScript runtime boundary, isolation, resource limits, and candidate
  engines for later phases.
- ADR: browser impersonation/TLS strategy and transport abstraction impact.
- ADR: Go-native plus out-of-process/WASM plugin direction.
- ADR: ffmpeg discovery and supervised-process boundary.

Acceptance criteria:

- Each ADR records the problem, evaluated alternatives, decision or remaining
  experiment, consequences, and the next phase that consumes it.
- Any unresolved choice has a bounded experiment and owner; it is not hidden in
  a generic risk entry.

Depends on: P0-01. Runs in parallel and does not block the walking skeleton
unless it changes a Phase 0 interface.

## 8. Recommended sequence

### Week 1: Contracts and test bed

- Complete P0-01.
- Define and seed P0-02.
- Implement the core of P0-03.
- Establish P0-10 endpoints needed by networking tests.
- Draft the P0-04 API and the first P0-11 ADRs.

Gate: metadata fixtures round-trip, the fixture server is deterministic, and CI
runs on all target operating systems.

### Week 2: Extraction path

- Complete P0-04 and P0-05.
- Integrate P0-06 with the fixture server.
- Start P0-08 against the settled value model.
- Continue bounded P0-11 experiments.

Gate: an in-process Go test accepts a fixture URL and returns the expected
normalized metadata and direct-media format without writing a file.

### Week 3: Download and CLI path

- Complete P0-07 and P0-08.
- Wire P0-09 end to end.
- Add cancellation, resume, machine-output, and failure-path integration tests.

Gate: the walking-skeleton command downloads the expected file and resumes an
interrupted transfer.

### Week 4: Hardening and exit review

- Run race, fuzz-smoke, platform, and dependency audits.
- Run the Python-free verification environment.
- Close documentation gaps and link evidence in P0-02.
- Review every Phase 0 failure path and intentional deviation.

Gate: all definition-of-done items below pass. If completed earlier, Phase 0
ends earlier; the fourth week is contingency, not scope expansion.

## 9. Parallel work lanes

When several contributors are available, work can proceed in three lanes:

| Lane | Initial work | Integration point |
| --- | --- | --- |
| Core | P0-03, P0-04, P0-08 | Stable metadata and operation contracts |
| I/O | P0-05, P0-07, P0-10 | Shared transport and fixture scenarios |
| Conformance | P0-02, P0-09 tests, P0-11 | Evidence links and exit-gate review |

Interface changes require a short design note and updates to consumers in the
same change. Parallelism must not create duplicate HTTP clients, metadata
models, or event types.

## 10. Test and verification strategy

### Per-change checks

```text
go test ./...
go vet ./...
go test -race ./...
```

Formatting and module-tidiness checks should run in CI. Fuzz targets should run
as short smoke tests on normal changes and for a longer scheduled duration.

### Test layers

- Unit tests: values, ordered objects, templates, registry, errors, retry and
  resume decisions.
- Component tests: transport and downloader against the fixture server.
- End-to-end tests: CLI subprocess against the fixture server and temporary
  output directories.
- Conformance tests: manifest capabilities mapped to named automated tests.
- Cross-platform tests: filename rules, signals/cancellation equivalents,
  atomic rename behavior, and path handling.

### Python-free gate

The release candidate must be built and tested in a minimal environment that:

- Does not install Python executables, standard libraries, or shared objects.
- Builds with Go and runs the produced binary in a separate minimal runtime
  image or equivalent clean host.
- Records spawned child processes during the end-to-end suite.
- Scans the binary, package metadata, scripts, and documentation commands for
  accidental runtime Python assumptions.

Python may be used outside this gate as a temporary differential oracle against
upstream yt-dlp. Oracle output must be checked in as attributable, reproducible
test data where licensing permits; the Go implementation must never invoke the
oracle in production.

## 11. Security and reliability rules

- Test fixtures and logs must never contain real account cookies or tokens.
- HTTP response bodies used for extraction have explicit size limits.
- Destination paths are confined to the selected output root.
- Temporary and partial-file policy is deterministic and documented.
- Logs redact authorization, cookies, proxy credentials, and signed URL query
  parameters where practical.
- Dependencies require a stated purpose and license check; convenience alone is
  not sufficient for adding a large runtime surface.
- No extractor or downloader can bypass context cancellation.

## 12. Phase 0 definition of done

Phase 0 is complete only when all of the following are true:

- The walking-skeleton command succeeds against the fixture server on Linux,
  macOS, and Windows.
- Direct-media extraction, deterministic metadata JSON, filename rendering,
  complete download, cancellation, and resume have automated evidence.
- Unit, integration, race, and required fuzz-smoke checks pass.
- The clean Python-free gate passes and observes no Python subprocess or
  library dependency.
- Machine-readable output is stable and separated from diagnostics.
- The capability manifest links each claimed Phase 0 capability to a passing
  test and labels every unsupported behavior explicitly.
- Public Go interfaces have package documentation and no known global-state or
  concurrency hazard.
- Security checks cover credential redaction, bounded page responses, output
  path confinement, and partial-file cleanup.
- The high-risk ADRs exist, with unresolved experiments assigned to a later
  phase.
- The next phase can add playlist/format-selection work and additional
  protocols without replacing the metadata, transport, event, or lifecycle
  foundations.

## 13. Suggested implementation change sequence

Keep changes independently reviewable. A practical sequence is:

1. Repository baseline, Go module, CI, and contribution documentation.
2. Capability manifest schema/checker and initial entries.
3. Ordered value model, metadata wrapper, JSON behavior, and tests.
4. Fixture server and transport contract/tests.
5. Application lifecycle, event model, and extractor registry.
6. Fixture and direct-media extractors.
7. Direct HTTP downloader, progress, cancellation, and resume.
8. Minimal template renderer and path safety.
9. CLI wiring and end-to-end conformance tests.
10. Python-free gate, cross-platform hardening, ADR review, and Phase 0 exit.

Avoid a single foundational mega-change: each step should leave the repository
building and the applicable tests passing.

## 14. First tasks to pick up

The first implementation slice can start immediately with these ordered tasks:

1. Create `go.mod`, the command stub, and the package layout from Section 4.
2. Add CI for build, test, vet, race, and formatting/module checks.
3. Write the parity-manifest schema and seed only the Phase 0 entries.
4. Implement the ordered value model test-first.
5. Add the fixture server's basic page and direct-media endpoints.

Tasks 3 and 5 can proceed alongside task 4 after the module and CI baseline are
merged. The first integration checkpoint is metadata extraction only; file
downloading follows after the model and transport contracts are stable.
