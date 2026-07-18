# Phase 3 release, operations, and ABI audit

Audit C date: 2026-07-19

Audited commit: `c40182f6f36f00c2f686c6d063e079d9a2fcfb44`

Host actually used: macOS/Darwin arm64, Go 1.25.12

Container actually used: Docker Desktop Linux/arm64, engine 28.5.1

## Decision

This audit does **not** support Phase 3 or Gate G3 exit on the audited commit.
The ABI and release primitives are substantial and their scoped deterministic
tests pass, but P3-09, P3-10, and P3-11 are not complete at their implementation
plan boundaries. G3 criteria 4, 5, and 7 therefore cannot all be marked passed.
In addition, the local Python-free scratch-container build failed on this exact
checkout, so the final Python-free container gate has no passing observation
from this audit.

No GitHub Actions result is used as evidence here. Windows was cross-built only;
this audit makes no claim that a Windows binary, installer, pack lifecycle, Job
Object path, or sandbox ran natively.

Classification used below:

- **Pass**: the claimed boundary was inspected and rerun locally.
- **Partial**: useful deterministic evidence exists, but the work-package or
  gate wording requires an additional integration or observation.
- **Blocker**: Phase 3/G3 exit would overclaim without closure evidence.
- **External**: closure requires an operator environment, trust decision, or
  native platform not present in this audit.

## Work-package and gate disposition

| Scope | Disposition | Audit conclusion |
| --- | --- | --- |
| P3-03 plugin SDK v1.x | Pass for native RPC/SDK contract | Real framed RPC helper exchanges cover old v1.0 host/new v1.1-capable plugin and new v1.1 host/v1.0 plugin. Public SDK negotiation, cancellation, malformed input, bounds, and secret-safe failures pass. |
| P3-03 signed pack upgrade | Partial | The signed v1.0/v1.1 compatibility matrix passes, but `internal/pack/upgrade` has no production caller and is not consumed by the existing pack verifier/installer. |
| P3-09 pack distribution | Partial | Signed canonical offline catalogs, exact resolution, expiry, signer revocation, package revocation, and CLI/public verification pass. There is no catalog-to-artifact-to-v1.1-negotiation-to-install transaction. Artifact transport and production revocation delivery remain external. |
| P3-09 sandbox maturity | Partial | Linux/macOS plan construction is tested, but `internal/sandbox` has no production call site or launcher. Windows is unsupported; macOS uses deprecated `sandbox-exec` and rejects requested quotas; Linux requires external `bwrap`/`prlimit`. |
| P3-10 canary framework | Partial | Opt-in execution, bounded secret handles, redacted records, timeout/cancellation, panic reduction, and rolling counts pass. The plan's expiry, rate-limit, and deterministic replay-capture requirements are absent, and no public, authenticated, or regional deployment runner exists. |
| P3-11 diagnosis/patch operations | Blocker | Local telemetry, differential, and operations report commands exist, but the committed drill is synthetic timestamp arithmetic with patch ref `deadbeef`; it did not diagnose, patch, commit, or verify a real representative regression. Operations summaries also discard failure-class aggregation. |
| G3 criterion 4 | **Blocker** | No executed 24–48-hour regression repair drill exists. The state-machine fixture proves validation and SLO bucket math only. |
| G3 criterion 5 | **Partial / not exit-pass** | Plugin old/new host compatibility is executed. The pack v1.1 contract is authenticated and matrix-tested but has not completed a compatible upgrade through the product installer/runtime path. |
| G3 criterion 7 | **Partial / external evidence required** | The neutral contracts are opt-in, bounded, redacted, and unable to mutate the parity manifest. No live, regional, or authenticated runner observation proves those properties in deployment. |

## Findings

### P3-AUD-C01 — synthetic drill is not G3 repair evidence

Severity: **Phase-exit blocker**

Scope: P3-11, G3 criterion 4

`conformance/operations/major_site_drill_v1.json` uses fixed timestamps and the
synthetic hash-shaped value `deadbeef`. Its provenance explicitly says the
incident and patch reference are synthetic. `TestMajorSiteDrillMatchesConformanceFixture`
constructs breakage, diagnosis, patch, and verification records directly; it
does not reproduce an extractor failure, create a differential fixture, change
production code, identify a real Git object, or run the repaired behavior.
`Drill.Patch` validates only the shape of a hexadecimal string.

The implementation is valid as a bounded incident evidence schema and SLO
calculator. It must not be described as an exercised 24–48-hour patch drill.

Closure requires a committed, attributable drill record that links:

1. a representative major-site regression observation;
2. deterministic reproduction and classification;
3. the promoted failing fixture/test;
4. the actual patch commit;
5. post-patch deterministic and relevant protocol/product verification; and
6. monotonic real timestamps establishing the measured latency.

### P3-AUD-C02 — P3-10 omits required operational controls

Severity: **High; phase-exit blocker for P3-10/G3-7**

Scope: P3-10, G3 criterion 7

`internal/operations` has a strong bounded interchange format and injected
`Runner` boundary. It has no suite/canary expiry, run-rate or frequency policy,
or deterministic replay-capture producer, despite those being explicit P3-10
requirements. `cmd/ytdlp-ops` validates and summarizes files; it does not run a
canary. There are no production runner implementations, scheduler, secret
resolver, regional transport, or authenticated runner.

The executor returns at a deadline, but a runner that ignores cancellation can
leave its goroutine alive. That deviation is correctly documented and makes a
process-isolated deployment runner preferable for untrusted implementations.

Closure requires the missing expiry/rate/replay semantics plus at least one
explicitly opted-in public run and controlled regional/authenticated runs in
approved environments. Exported evidence must demonstrate redaction, timeout,
and inability to write compatibility claims; unavailability may be reported,
but cannot be rewritten as a successful run.

### P3-AUD-C03 — v1.1 pack upgrade is not adopted by product installation

Severity: **High; P3-03/P3-09 and G3-5 blocker**

The pack contract tests authenticate deterministic Ed25519 v1.0/v1.1 records,
exercise all four host/pack combinations, reject unknown majors, downgrades,
removed capabilities and Python runtimes, and force permission-increase review.
Those tests pass.

However, repository call-site inspection finds `VerifyAndNegotiate` used only
inside `internal/pack/upgrade` tests. The existing `internal/pack` manifest and
installer do not consume the v1.1 upgrade contract. Likewise, catalog resolution
returns authenticated metadata but does not retrieve/open an artifact, bind the
resolved publisher/digest/size to pack verification, negotiate v1.1, or install
it. The manifest's own known deviation (“pending product installer adoption”)
is accurate and prevents an end-to-end compatible pack-upgrade claim.

Closure requires one product-level old-install/new-pack and new-host/old-pack
upgrade path that binds catalog entry, artifact bytes, publisher, digest, size,
contract negotiation, permission review, install/activation, rollback, and
revocation checks before filesystem mutation.

### P3-AUD-C04 — sandbox evidence constructs plans but enforces none

Severity: **High for hostile native plugin claims; explicit platform deviation**

Scope: P3-09

`internal/sandbox` has no non-test production caller. Tests prove deterministic
argument/profile construction, path checks, permission-shaped network policy,
and fail-closed adapter/limit errors; they do not launch a plugin under the
resulting plan. Therefore the repository cannot claim that installed native
plugins are actually executed through bubblewrap or `sandbox-exec`.

Platform boundaries remain:

- Windows: unsupported sandbox and secure pack install/rollback/remove.
- macOS: deprecated `sandbox-exec`; requested CPU/memory/process/file quotas
  are rejected rather than enforced.
- Linux: isolation requires deployment-installed `bwrap`; quotas additionally
  require `prlimit`. Missing adapters fail closed.
- Native RPC still lacks portable CPU, memory, and process-count quotas, and
  Windows health checking retains the documented start-to-Job-assignment race.
- WASM has bounded memory/messages/time and no WASI/imports, but no fuel meter.

Closure is either a maintained production launcher with native execution tests
for each claimed platform, or a narrower compatibility statement that permits
only the already constrained WASM boundary and treats native plugin execution
as unsupported where isolation policy requires stronger guarantees.

### P3-AUD-C05 — Python-free scratch-container gate fails on audited commit

Severity: **Verification blocker**

Scope: release/Python-free exit evidence

The local Docker build reached an Alpine build stage with neither `python` nor
`python3`, passed parity validation, and then failed at `go test ./...`:

```text
internal/cookies/chromiumwindows.TestDiscoveryAndUnsafePaths
got Default/Cookies; expected Default/Network/Cookies
```

The test creates both candidates with back-to-back writes and assumes the
second has a later modification time. Alpine assigned equal timestamps, so the
implementation's deterministic lexical tie-break selected the legacy path.
This appears to be a nondeterministic test fixture, not evidence of Python use,
but the scratch image was not produced and none of its runtime commands ran.

Closure requires making the fixture assign distinct explicit timestamps (or
asserting the documented tie behavior), then rerunning the Docker build and all
scratch runtime probes locally. The image contains `ytdlp-ops`, but the current
CI runtime probe list does not execute it; add a harmless version/API or
fixture-validation probe if operational tools are part of the shipped image.

### P3-AUD-C06 — operational summary omits failure-class aggregation

Severity: **Medium; P3-11 completeness blocker**

Canary records retain a closed `failure_class`, but `RollingMetrics.Snapshot`
aggregates only outcome counts and per-canary outcome counts. It does not report
extractor/network/auth/region/media/contract/runner failure classes. Coverage
and overflow are available separately through `telemetrycheck`, and mismatch
reports are available through `diffcheck`, but the P3-11 failure-class report is
missing.

Closure requires bounded failure-class totals (and tests) in the local report,
plus documentation that ties telemetry coverage, differential mismatch, canary
failures, fallback/overflow, and patch latency into the incident workflow.

### P3-AUD-C07 — production distribution authority remains external

Severity: **External release blocker; not a cryptographic implementation bug**

Catalog, pack, and updater trust keys are caller-supplied; deterministic
repository keys are explicitly test-only. The repository does not select or
operate production root custody, signer quorum, rotation, transparency,
artifact transport, publishing credentials, revocation-feed delivery, or
release scheduling. There is also no project-wide `LICENSE`/`COPYING` file, so
the documented public binary-distribution legal blocker remains.

Closure requires owner decisions and auditable operational evidence for key
custody/rotation/revocation, artifact publication and rollback, plus an approved
project-wide distribution license. Internal engineering artifacts may continue
to be used without presenting them as a public signed release service.

### P3-AUD-C08 — Windows evidence remains cross-build-only

Severity: **External platform blocker / explicit deviation**

`CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build ./...` passed. No Windows
binary was run. This audit therefore does not close Windows owner/DACL policy,
pack lifecycle, updater durability, descendant termination, or sandbox gaps.
The existing fail-closed Windows pack lifecycle statement remains accurate.

Closure requires a separately recorded native Windows run in a controlled
environment. An Actions matrix definition, cross-compilation, or OS-simulated
unit test is not native evidence.

## Evidence that did pass locally

### ABI, packs, catalog, operations, release, and updater

The following scoped packages and commands passed after a clean rerun:

```text
go test ./internal/plugin/upgrade ./internal/plugin/rpc \
  ./internal/pack/upgrade ./internal/pack/catalog ./internal/pack \
  ./internal/sandbox ./internal/operations ./internal/release \
  ./internal/update ./pkg/pluginapi/sdk ./pkg/ytdlp \
  ./cmd/ytdlp-pack ./cmd/ytdlp-ops ./cmd/ytdlp-release

go test -race ./internal/plugin/upgrade ./internal/plugin/rpc \
  ./internal/pack/upgrade ./internal/pack/catalog ./internal/pack \
  ./internal/sandbox ./internal/operations ./internal/release \
  ./internal/update ./pkg/pluginapi/sdk

go vet <the same production/test package scope>
go run ./cmd/paritycheck
go test ./internal/conformance
```

Parity validation reported 55 capabilities and zero temporary fallbacks.
Relevant fuzz targets for plugin upgrade, pack upgrade, catalog verification,
sandbox arguments, operations suite/drill decoding, release archive paths, and
updater envelopes each completed a bounded local 100-execution run.

One initial concurrent multi-package run observed
`TestApplySerializesConcurrentWriters` fail with `unsafe update path: lock
ownership`. The test then passed 20 consecutive isolated repetitions, the full
scoped rerun, and the race run. This audit classifies it as an unconfirmed
transient observation, not a blocker, but a future recurrence should preserve
the temp-root metadata and become a deterministic regression test.

### Reproducibility, SBOM, and vulnerability posture

- Two independent native macOS/amd64 no-cgo `ytdlp-go` builds using trim-path,
  disabled VCS stamping, empty build ID, fixed version, and fixed source epoch
  were byte-identical and the binary ran locally.
- Two independently assembled one-target release directories were recursively
  byte-identical; `SHA256SUMS` verified.
- Release tests covering normalized archives, checksums, canonical manifest,
  SPDX 2.3 SBOM, exact license coverage, bounded inputs, and exclusive
  publication passed, including race detection.
- `govulncheck v1.6.0 ./...` reported zero reachable vulnerabilities. It also
  reported 14 advisories in required modules whose vulnerable symbols are not
  called; this is a symbol-aware pass, not a statement that every required
  module version is advisory-free.
- No-cgo `go build ./...` passed for linux/amd64, darwin/amd64, and
  windows/amd64. Only native macOS execution occurred; Docker supplied a Linux
  arm64 build environment but failed before image publication as described in
  P3-AUD-C05.

### Strong controls retained despite the blockers

- Plugin RPC v1.0/v1.1 negotiation uses real framed helper-process exchanges,
  has cancellation/crash isolation, bounded messages, exact capability and
  permission checks, and secret-safe errors.
- Signed pack and catalog records are canonical Ed25519 documents with explicit
  trust, freshness, digest/size/publisher metadata, exact version resolution,
  and fail-closed key/package revocation.
- Canary records exclude targets, secret handles, regions, URLs, free-form
  errors, and media metadata. Execution requires explicit opt-in and cannot
  import or write the parity manifest through the operations package.
- Production-source and fallback-inventory conformance tests pass. The failed
  Docker test prevents a container-pass claim but does not reveal a Python
  production dependency.

## Minimum closure checklist for the Phase 3 exit review

1. Execute and commit a real representative regression drill satisfying
   P3-AUD-C01.
2. Add P3-10 expiry, rate-limit, replay-capture, and controlled runner evidence;
   record live/public plus controlled regional/authenticated outcomes without
   granting them conformance authority.
3. Adopt the v1.1 pack contract in the verified catalog/install/activation path
   and prove both upgrade directions at product level.
4. Either enforce sandbox plans in the production launcher or narrow native
   plugin support/deviations so no isolation claim exceeds runtime evidence.
5. Add failure-class aggregation to local operations reporting.
6. Fix the deterministic Chromium Windows discovery test and obtain a passing
   Python-free scratch-container build and runtime probe set.
7. Keep Windows lifecycle/isolation claims blocked until native evidence exists.
8. Record production distribution/legal decisions separately; do not substitute
   deterministic test keys or short-lived engineering artifacts.
