# Phase 2 exit review

Review date: 2026-07-18  
Behavioral reference: `yt-dlp/yt-dlp@aefce1eea4d0b6bab1ec2bd3beff09bff91a39c8`  
Decision: **repository work complete; Gate G2 blocked only on an external Windows native run**

## Executive decision

Phase 2's implementation, deterministic conformance, security review, native
extractor breadth, reproducible alpha assembly, and Python-free audits are
complete. All repository-controlled gates pass. The product registers 25
native representative extractors and covers every required risk class. The
security audit has no open critical finding, the fallback inventory contains
zero temporary fallbacks, and `govulncheck v1.6.0` reports zero reachable
vulnerabilities.

Gate G2 is not declared complete because criterion 5 literally requires alpha
artifacts to install, update, roll back, and run on Linux, macOS, and Windows.
Linux/amd64 was executed through the explicit Linux/amd64 container target;
the macOS/amd64 artifact ran under the host's native compatibility support and
the host-native macOS updater lifecycle passed. Windows binaries and updater
lifecycle tests cross-compile, and the repository's Windows CI job runs the lifecycle test, but
this checkout has no Git remote, Windows runner, Wine, or Wine64 with which to
observe that final native execution. This is an external infrastructure
evidence blocker, not an implementation or test failure.

## Work-package disposition

| Package | Result | Principal evidence |
| --- | --- | --- |
| P2-01 Public API and policy | Complete | `api.alpha_contract`; API compile, event JSON, cancellation, trust API, compatibility policy, and machine-readable fallback tests. |
| P2-02 Configuration and CLI | Complete | `compat.configuration`; precedence, aliases, encodings, locations, cancellation, source spans, fuzzing, CLI integration, and attributable corpus. |
| P2-03 Archive and cache | Complete | `compat.download_archive`, `cache.namespaced_storage`; cross-process locking, atomicity, corruption and hostile-path tests, fuzzing, and product-flow tests. |
| P2-04 Cookies and credentials | Complete with explicit store limitations | `cookies.portable_sources`, `cookies.browser_import`; Netscape, Firefox, Chromium macOS/Linux synthetic fixtures, locked-copy and redaction tests. Unsupported stores are categorized and fail closed. |
| P2-05 Compatibility languages | Complete for declared corpus | `compat.languages_principal`; format, output/progress, metadata, and match-filter parsing/evaluation with source spans, bounds, property tests, and fuzzing. |
| P2-06 Downloader and protocols | Complete | `downloader.protocol_matrix`; retry/throttle/file-retry policy, host fragment limits, resume artifacts, typed shell-free external adapter, ISM/HLS/DASH dispatch and fuzzing. |
| P2-07 Postprocessors | Complete | `postprocess.core_operations`; typed ffmpeg graph for audio, conversion, embeds, metadata, concat, chapters and moves, with cancellation, atomic cleanup, redaction, and generated-media verification. |
| P2-08 Plugin ABI v1 | Complete with declared isolation limits | `plugins.abi_v1`; secure discovery, signed identity, v1 negotiation, permissions, RPC/WASM capabilities, crashes, cancellation, bounds, Python declaration rejection, SDK and ABI policy. |
| P2-09 Signed packs and sandbox | Complete for declared platforms | `release.signed_packs`; deterministic signed archives, hostile input rejection, approval/revocation/rollback, crash recovery, Linux/macOS adapters, and explicit Windows lifecycle fail-closed behavior. |
| P2-10 Updater and alpha releases | Implementation complete; Windows native observation externally blocked | `release.updater`; signed metadata, targeting, locks, health checks, recovery, rollback, deterministic archives/checksums/licenses/SPDX, and native-artifact lifecycle test. |
| P2-11 Extractor factory | Complete | `extractor.phase2_breadth`; deterministic registry gate proves exactly 25 representatives and all nine risk classes, plus family fixtures, provenance, failures, cancellation, protocol integration, and fuzzing. |
| P2-12 Conformance, security and exit | Complete except external Windows observation | `verification.g2_security`, this review, security review, fallback inventory, 48-capability manifest, complete deterministic verification matrix, and Python-free container audit. |

## Gate G2 mapping

| Criterion | Status | Evidence and conclusion |
| --- | --- | --- |
| 1. Core and compatibility tests pass without Python | Pass | `go test ./...`, `go test -race ./...`, `go vet ./...`, all 58 fuzz targets at 100 deterministic mutations with `-parallel=1`, and the scratch image's complete Linux/amd64 suite passed. |
| 2. Reference is read-only and absent from product/build/release | Pass | Production-source/reference-path tripwire, provenance documents, `CGO_ENABLED=0 go list -deps ./...`, and scratch build/runtime. The pinned checkout is not a module, command, runtime, or fixture-generation dependency. |
| 3. Temporary and silent fallbacks accounted for | Pass | `go run ./cmd/paritycheck` validated 48 capabilities and zero temporary fallbacks. The inventory forbids silent and Python-backed fallback and requires owner/removal metadata for any future temporary entry. |
| 4. No critical security finding remains | Pass | `docs/P2_SECURITY_REVIEW.md` records the surface review and residual register. Pinned `govulncheck v1.6.0 ./...` reports zero reachable vulnerabilities after dependency remediation. |
| 5. Alpha artifacts install/update/rollback/run on Linux, macOS, Windows | Blocked externally | Reproducible artifacts exist for all three targets. Linux/amd64 scratch build and lifecycle ran; the macOS/amd64 artifact and host-native lifecycle ran; Windows cross-build and lifecycle test binary pass compilation. Native Windows execution is unobserved because no Windows/Wine runner or Git remote is available. |
| 6. At least 25 extractors span all major risks | Pass | `TestRepresentativeExtractorCatalogCountRoutingAndRiskCoverage` proves 25 registered representatives spanning simple/direct, shared backend, playlist/API, live, authenticated, manifest-heavy, anti-bot/impersonated, regional, and JavaScript-challenge classes. |

## Final verification record

The following were run from a cleanly integrated `main` worktree after the
extractor and security changes:

- `test -z "$(gofmt -l .)"`
- `go mod tidy -diff`
- `go run ./cmd/paritycheck` — 48 capabilities, zero temporary fallbacks
- `go test ./...`
- `go test -race ./...`
- `go vet ./...`
- all 58 discovered fuzz targets with `-fuzztime=100x -parallel=1`
- no-cgo Linux/amd64, Darwin/amd64, and Windows/amd64 builds
- `govulncheck v1.6.0 ./...` — zero reachable vulnerabilities
- two complete release assemblies — byte-identical output trees
- checksum verification for all three archives and SPDX/license coverage
- Python-free scratch runtime on host architecture and explicit Linux/amd64
- Linux/amd64 execution of `ytdlp-go`, `ytdlp-update`, `ytdlp-release`,
  `ytdlp-pack`, `ytdlp-js-helper`, and the isolated JavaScript/EJS probe
- `TestProductionSourcesDoNotInvokePython` and no-cgo dependency enumeration

## Residual deviations and blockers

The security residual register contains three medium, one low, and one
informational item; none is critical. They cover the Windows process-start
assignment window, portable Windows DACL/directory-durability proof, deprecated
macOS sandbox facilities, release-directory concurrent replacement assumptions,
and unavailable Windows signed-pack lifecycle. Each has an owner and later
milestone in `docs/P2_SECURITY_REVIEW.md`.

Public distribution remains legally blocked until the repository has a
project-wide distribution license and the pinned `fhttp` dependency has a
license conclusion. Production signing custody and publishing credentials are
also intentionally external operational decisions. These do not weaken the
internal deterministic alpha, but public artifacts must not be released before
resolution.

To close Gate G2, run the existing Windows CI matrix (or an equivalent clean
Windows/amd64 host), retain the passing output of
`TestNativeArtifactInstallUpdateRollbackAndRun`, and execute the assembled
`ytdlp-go.exe --version` after install, update, and rollback. No source change
is expected unless that native run exposes a platform defect.
