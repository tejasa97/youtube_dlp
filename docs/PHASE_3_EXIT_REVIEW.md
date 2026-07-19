# Phase 3 exit review

Review date: 2026-07-19  
Reference baseline: `yt-dlp/yt-dlp@aefce1eea4d0b6bab1ec2bd3beff09bff91a39c8`  
Decision: **Gate G3 blocked; Phase 3 is not complete**

## Outcome

The repository-side Phase 3 implementation and independent audits are complete.
All deterministic evidence is local, Python-free, and reproducible without
GitHub Actions. The gate cannot honestly pass because its beta/deployment facts
do not exist: there is no intended-traffic measurement window, no deployment
semantic-shadow review window, no traffic-derived high-value site matrix, and
no executed live/authenticated/regional canary set. Native Windows execution
and production distribution authority are also unavailable.

This is an external-evidence block, not a request to weaken the gate. Synthetic
fixtures, cross-compilation, catalog test keys, and repository history are not
substituted for deployment observations.

## Work-package disposition

| Package | Repository evidence | Disposition |
| --- | --- | --- |
| P3-01 measurement | Bounded concurrent telemetry, overflow/loss accounting, canonical merge/export, full-denominator product integration, hostile-label rejection, race/fuzz evidence, and `telemetrycheck`. | Repository complete; no measured intended-traffic window or 95% result. |
| P3-02 semantic shadow | Typed redacted routing/request/metadata/format/playlist/warning/protocol/error comparison, canonical corpus, `shadowcheck`, evidence-existence enforcement, and review ledger. | Repository complete; only synthetic equal-corpus observations exist. |
| P3-03 SDK v1.x | Native RPC v1.0/v1.1 exchanges, author SDK, signed contract matrix, and catalog-to-artifact-to-contract-to-install transaction (`20547e8`, `7f40b37`). | Complete for deterministic ABI/pack compatibility. |
| P3-04 shared hosting | Streamable, PeerTube, and earlier reusable shared backends have routing, request, failure, cancellation, fixture, and protocol evidence. | Implemented proxy set; traffic-derived priority remains unavailable. |
| P3-05 public/API | Internet Archive and existing API families provide bounded playlists, pagination, errors, and rate-limit classification. | Implemented proxy set; real demand/rate-limit interoperability is external. |
| P3-06 authentication | Scoped netrc, Firefox, Chromium-family macOS/Linux/Windows imports, partial app-bound handling, bounds, identity-stable snapshots, and secret-safe errors. | Deterministic support complete for claimed profiles; real-profile and native-Windows observations remain opt-in/external. |
| P3-07 live media | Twitch live/VOD/clip, scheduled-state handling, LL-HLS parts/deltas, discontinuities, DASH/HLS pipelines, cancellation, and overflow regression evidence. | Deterministic representative breadth complete; live entitlement/interoperability observation is external. |
| P3-08 impersonation/regional | Versioned Chrome/Firefox profiles, explicit proxy policy, challenge execution, geo failures, and region contracts. Implicit helper `PATH` execution was removed in `49c3440`. | Claimed deterministic profiles complete; Safari/HTTP3 and regional execution remain deviations. |
| P3-09 distribution/sandbox | Signed offline catalog, revocation, v1.x transactional install/rollback, public API, and explicit fail-closed native sandbox host (`b2b7bb1`). | Repository transaction complete. Linux/macOS adapter availability, deprecated macOS adapter, Windows lifecycle/sandbox, key custody, transport, and publishing authority remain external/declared. The source project is Apache-2.0. |
| P3-10 canaries | Opt-in suites, opaque secret handles, expiry, suite binding, minimum interval/window limits, bounded ledger, replay capture, redaction, timeout, panic handling, and local validation command. | Framework complete; scheduler, atomic distributed ledger, credential resolver, regional infrastructure, and actual runs are external. |
| P3-11 operations | Coverage/differential/canary reports, bounded outcome and failure-class aggregation, replay, incident schema, and attributable LL-HLS fix `9ad13de`. | Local tooling complete. Git proves a 53-second introduce-to-fix interval, not a production detection-to-patch drill; G3-4 remains blocked. |
| P3-12 audit/exit | Three independent audits, repository remediation, full local matrix, Python-free scratch container, and this review. | Review complete; exit decision is blocked. |

## Gate G3 decision

| Criterion | Decision | Evidence and missing fact |
| --- | --- | --- |
| G3-1: at least 95% of measured intended traffic | **Blocked** | The denominator model and local report pass, but no declared traffic window, sampling/loss statement, or measured result exists. |
| G3-2: zero Python fallback for high-priority capabilities | **Pass for the committed inventory** | `paritycheck` validates 55 capabilities and zero temporary fallbacks; source/container audits find no Python runtime or build dependency. “High priority” is still a proxy until G3-1 supplies traffic. |
| G3-3: no unreviewed critical semantic/security difference | **Pass for repository beta coverage; deployment review absent** | Independent audits found zero critical issues. All repository findings were dispositioned. The semantic ledger records no deployment shadow window. |
| G3-4: representative regression repaired in 24–48 hours | **Blocked** | LL-HLS commits prove deterministic reproduction, regression assertion, fix, and 53-second repository history. They do not prove live detection or a separately timed operational drill. |
| G3-5: compatible v1-to-v1.x upgrade | **Pass** | Native old/new exchanges and all pack host/contract combinations pass; the signed catalog transaction binds artifact, publisher, contract, permission review, activation, rollback, and revocation. |
| G3-6: high-value beta-site evidence | **Blocked** | Representative sites have deterministic evidence, but the authoritative high-value set cannot be derived without intended-traffic data. |
| G3-7: bounded live/regional/auth canaries | **Blocked** | Contracts and local controls pass. No approved live, credentialed, or regional execution evidence exists. |

## Independent audit disposition

- Coverage/parity audit `96ff28d`: six stale manifest references were corrected
  and made mechanically enforceable by `c24d9fd`. Its four high deployment
  blockers remain accurately open.
- Security/privacy/isolation audit `1eb873b`: implicit helper search was removed
  (`49c3440`), native sandbox launch was integrated (`b2b7bb1`), and browser
  row/value bounds plus snapshot identity checks were added (`bbe4610`). No
  critical finding remains.
- Release/operations/ABI audit `d073dd1`: the Alpine timestamp flake was fixed
  (`b8e0194`), the Python-free image passed, the pack upgrade reached the
  installer (`20547e8`, `7f40b37`), and expiry/rate/replay/failure aggregation
  landed (`ee5c8cb`, `ac1e35e`). Its production drill, native-platform, and
  distribution-authority findings remain external.

The audit documents preserve their audited baseline conclusions. This review
records later remediation rather than rewriting independent reports.

## Local verification

GitHub Actions was disabled and was not used as evidence. The final review uses:

- formatting and `go mod tidy -diff` drift checks;
- `go run ./cmd/paritycheck` and manifest-evidence existence checks;
- `go test ./...`, `go test -race ./...`, and `go vet ./...`;
- bounded parser/security fuzz targets;
- no-CGO Linux, Darwin, and Windows amd64 builds (Windows cross-build only);
- `govulncheck` with zero reachable vulnerabilities;
- two byte-identical release assemblies with checksums and SBOM;
- a locally built Linux scratch image containing only static Go tools, CA
  certificates, and license notices; runtime version probes pass and exported
  contents contain no Python or shell.

Native macOS execution was observed on the local host. Linux execution was
observed in Docker. Windows was not run natively and is never described as such.

## External closure record required

Gate G3 can be reconsidered only after the repository receives attributable,
redacted evidence for:

1. a declared intended-traffic window and denominator yielding the measured
   coverage result and high-value site set;
2. a beta semantic-shadow window with reviewer dispositions;
3. an executed representative detection-to-fix drill with real monotonic
   timestamps and promoted regression evidence;
4. opted-in public, authenticated, and regional canary observations under the
   committed expiry/rate/redaction controls;
5. any claimed native Windows lifecycle/isolation behavior; and
6. production distribution decisions for signing custody, rotation, revocation
   delivery, artifact transport, publishing authority, and rollback authority.

Until those records exist, Phase 3 remains blocked and Phase 4 must not treat G3
as passed.
