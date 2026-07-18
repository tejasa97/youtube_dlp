# Phase 3 coverage and parity audit

- Audit lane: independent Audit A
- Baseline: `main@cc54963d75a704eadc93b5691979c63da0f0b93d`
- Date: 2026-07-19
- Scope: P3-01 through P3-08 and Gate G3 criteria 1, 2, 3, 6, and 7

## Conclusion

Gate G3 is **not demonstrated** at this baseline. The repository has strong
deterministic foundations for privacy-safe telemetry, typed semantic
comparison, the v1.0-to-v1.1 ABI matrix, representative extractor breadth,
credential isolation, live media, and explicit impersonation profiles. The
local tests exercised by this audit pass. However, no measured intended-traffic
window exists, no beta semantic-difference review ledger exists, the set of
"high-value beta sites" is not defined by traffic or mapped to every required
evidence axis, and no live/authenticated/regional canary has been executed by
an attributable deployment runner. Those are gate facts, not failures that a
synthetic fixture can convert into a pass.

No critical finding was identified. Four high-severity findings block the
reviewed G3 criteria. Medium and low findings concern manifest traceability and
incomplete work-package breadth; they do not indicate a hidden Python runtime
or silent fallback.

## Method and deterministic checks

This audit was read-only except for this report. It did not use CI, live
credentials, a network canary, or the upstream checkout at runtime.

- Parsed all 564 evidence entries belonging to `compatible` manifest claims.
  All 92 file references exist. The 472 test/fuzz references were compared
  with `go test -list` output from their declared packages; six references do
  not resolve.
- `go run ./cmd/paritycheck` passed with 55 capabilities and zero declared
  temporary fallbacks.
- `go test` passed for `internal/telemetry`, `cmd/telemetrycheck`,
  `internal/differential`, `internal/plugin/upgrade`,
  `internal/pack/upgrade`, `internal/operations`, `cmd/ytdlp-ops`,
  `internal/extractor`, `internal/protocol/hls`, `internal/fragment`,
  `internal/network/impersonate`, `internal/credentials/netrc`, the Chromium,
  Linux Chromium, and Firefox cookie packages, and `pkg/ytdlp`.
- Focused conformance checks passed for the manifest, fallback inventory,
  required `python-backed`/`silent` prohibitions, and production-source
  Python invocation tripwire.
- Attempting to evaluate `conformance/telemetry/snapshot_v1.json` with
  `telemetrycheck` failed closed because that foundation fixture contains
  `download` and `live` capabilities while the product collector and report
  command admit only `extract`.

## Findings

### High

#### H-01 — No measured traffic denominator; G3 criterion 1 cannot pass

`docs/P3_EXTRACTOR_PRIORITY.md` explicitly says the project has no suitable
production traffic dataset. `conformance/telemetry/PROVENANCE.md` says its
counts are arbitrary synthetic data, not captured traffic. The checked-in
snapshot is therefore not a coverage result; it also contains 42 successes out
of a 48-observation full denominator (8,750 basis points) before considering
its incompatibility with the product report command.

The implementation correctly keeps errors, unsupported operations, fallbacks,
unknown extractors, and overflow in the denominator
(`internal/telemetry.CalculateCoverage` and
`cmd/telemetrycheck.TestRunMergesSnapshotsAndEvaluatesCoverage`). It does not
create the deployment facts required by G3: sampling window, intended-traffic
population, exclusions, loss policy, retention/export policy, and an actual
result at or above 9,500 basis points. This is honestly disclosed in
`operations.privacy_safe_telemetry.known_deviation`.

Required closure evidence: an opt-in, attributable traffic report plus its
published denominator policy. Repository fixtures remain supporting schema
evidence only.

#### H-02 — No Phase 3 beta semantic-review state; G3 criterion 3 cannot pass

`internal/differential` can parse, redact, classify, bound, aggregate, and
canonically serialize typed shadow observations. Its Phase 3 corpus is wholly
synthetic and intentionally equal after policy normalization. The provenance
explicitly says it proves the mechanism, not live extractor parity.

There is no product producer for `ObservationEnvelope`, no command that invokes
`CompareObservations`, and no Phase 3 review ledger recording disposition,
owner, acceptance rationale, or closure of critical mismatches.
`cmd/diffcheck` still exercises the earlier `Document`/`Compare` model rather
than the Phase 3 shadow model. The severity tests prove that critical routing,
format-usability, and protocol-usability differences can be labeled; they do
not prove that beta observations were collected or reviewed.

Required closure evidence: bounded paired observations for the declared beta
surface, canonical reports, and a reviewed disposition ledger showing no open
critical semantic difference. The separate Phase 3 security audit must also
close the security half of criterion 3.

#### H-03 — “High-value beta sites” and their complete evidence matrix are undefined

`pkg/ytdlp.TestRepresentativeExtractorCatalogCountRoutingAndRiskCoverage`
routes 28 representative extractors and covers nine self-declared risk classes.
That is useful registry evidence, but it is neither traffic-weighted nor a
matrix for G3 criterion 6. `docs/P3_EXTRACTOR_PRIORITY.md` is explicit that its
ordering is an engineering proxy.

The manifest contains strong per-capability fixtures, but no authoritative
high-value site set maps each selected site to routing, request shape,
metadata, formats, playlists where applicable, warnings, protocol usability,
failure, cancellation, and security evidence. For example, the new Streamable,
PeerTube, and Internet Archive claims prove routing and normalized output, but
do not each demonstrate an end-to-end protocol download or a warning contract.
The catalog test itself validates routing and manually assigned class labels;
it does not execute those evidence dimensions.

Required closure evidence: after H-01, publish the traffic-derived high-value
set (or an explicitly accepted proxy if the exit policy is changed) and a
machine-checkable per-site/per-axis evidence matrix with deviations.

#### H-04 — Canary framework exists, but live/auth/region canary evidence does not

`internal/operations` provides explicit opt-in, per-canary timeouts,
cancellation, closed outcomes, redacted records, secret-handle references, and
bounded aggregation. It has no manifest-writing dependency, so repository code
supports the “cannot change compatibility claims” boundary.

The checked-in suite contains synthetic `credential` and `region` definitions,
plus a public YouTube definition, but no canary identified as live. The only
checked-in incident is a synthetic 23-hour drill. `docs/P3_OPERATIONS_EVIDENCE.md`
explicitly says there is no scheduler, credential resolver, regional runner,
network transport, or alert sink. There are no attributable result records
from a live, authenticated, or regional environment. The suite schema also has
timeouts and count bounds but no expiry or execution-rate policy; those remain
deployment responsibilities.

Required closure evidence: opt-in public-live, authenticated, and regional
runner executions with independent rate/expiry/timeout policy, redacted
records, provenance, and a documented rule that observations may trigger
review but never mutate compatibility status.

### Medium

#### M-01 — Six compatible manifest claims contain stale test references

The manifest validator checks strings and duplicates but does not verify that
evidence files or test functions exist. These exact references do not resolve:

| Capability | Stale reference | Current nearby evidence |
| --- | --- | --- |
| `protocol.hls` | `internal/fragment.TestEngineReusesCompletedFragments` | Fragment reuse/revalidation tests exist under different names, including `TestEngineRevalidatesLegacyFragmentsWithoutDigests`. |
| `network.impersonation` | `internal/network/impersonate.TestRequestAdapterPreservesSemanticsAndAppliesProfile` | `TestClientPreservesRequestSemanticsAndAppliesProfile` exists. |
| `network.impersonation` | `internal/network/impersonate.TestCookieJarAdapterRoundTripsAllSupportedFields` | `TestClientUsesStandardCookieJar` exists. |
| `extractor.soundcloud_pilot` | `pkg/ytdlp.TestProductRegistryIncludesPhaseOneExtractors` | `TestProductRegistryIncludesIntegratedExtractors` exists. |
| `compat.format_selector_pilot` | `internal/compat_test.TestPinnedCompatibilityCorpus` | The test is `internal/compat.TestPinnedCompatibilityCorpus`. |
| `compat.output_template_pilot` | `internal/compat_test.TestPinnedCompatibilityCorpus` | The test is `internal/compat.TestPinnedCompatibilityCorpus`. |

This does not invalidate all underlying behavior, but it weakens exact claim
traceability for P3-07 and P3-08 and allows future evidence drift to pass
`TestRepositoryManifest`. Add existence validation for file evidence and exact
`go test -list` validation in the local parity workflow.

#### M-02 — P3-05 has an honest priority proxy but incomplete rate-limit policy evidence

Internet Archive provides bounded parsing, deterministic reusable playlists,
failure/cancellation tests, and classifies HTTP 429 as a network error.
Several earlier API extractors also classify 429. No Phase 3 evidence exercises
`Retry-After`, bounded retry/backoff, or a site-specific rate-limit policy.
Because measured demand is absent, the package cannot establish that its API
selection represents high-usage traffic. Treat P3-05 as partial rather than a
traffic-prioritized completion claim.

#### M-03 — P3-06 remains a scoped, fail-closed subset

Native netrc, Firefox, macOS Chromium, and Linux Chromium boundaries have
deterministic security and cancellation evidence, and no automated test reads
a developer profile. Known deviations remain material: Windows
Chromium/DPAPI is pending on this baseline, Firefox profile aliases are not
resolved, Linux v11 depends on an injected Secret Service/KWallet provider,
and real third-party login/refresh lifecycles are not modeled. These are
explicit fail-closed coverage gaps, not secret-safety failures.

#### M-04 — P3-07 live breadth is substantial but not complete against its plan

Repository evidence covers upcoming/post-live classification, Twitch live/VOD/
clip replay, polling and end-of-stream, cancellation, LL-HLS parts/delta skips,
discontinuity parsing, and replacement of partial segments. The Phase 3 work
package also names DVR breadth, ad markers, refresh behavior, and live-from-start.
No deterministic ad-marker behavior was found, and Twitch live-from-start,
subscriber entitlements, and chat remain explicit deviations. The stale HLS
evidence reference in M-01 further reduces manifest precision.

#### M-05 — P3-08 is a versioned subset, not regional-runner completion

Chrome 133 and Firefox 120 profiles, explicit proxy tunnels, cancellation, and
fail-closed unknown profiles have passing tests. Safari is deliberately absent
because the pinned engine's advertised and actual fingerprints disagree;
HTTP/3 is not represented. Regional SVT/BBC/ARD/NRK behavior is deterministic
fixture evidence, not an attributable regional runner result. The two stale
impersonation references in M-01 should be corrected before relying on the
manifest as an exact review index.

### Low

#### L-01 — The telemetry foundation fixture is not consumable by the local product report

`conformance/telemetry/snapshot_v1.json` contains `download` and `live`
capabilities. `pkg/ytdlp.NewTelemetryCollector` fixes the public product
capability to `extract`, and `telemetrycheck` offers extractor extensions but
no capability extension. The command therefore rejects the fixture as outside
configured dimensions. This is fail-closed and production snapshots are
currently extract-only, but either a product-compatible report fixture or a
clear foundation-only label would improve evidence usability.

## P3-01 through P3-08 assessment

| Package | Repository assessment | External/deployment dependency |
| --- | --- | --- |
| P3-01 measurement | Foundation complete and tests pass; product counts one outcome per `Run`, disabled by default. H-01 and L-01 prevent gate use. | Traffic window, denominator policy, loss/sampling statement, retention/export controls. |
| P3-02 semantic shadow | Comparator/redaction mechanism complete for its synthetic corpus. Operational comparison and review closure are absent (H-02). | Attributable beta observations and reviewer dispositions. |
| P3-03 SDK v1.x | Repository evidence passes old-host/new-plugin, new-host/old-plugin, permission invariants, malformed/cancelled inputs, and signed pack contracts. Product installer adoption of the pack v1.1 contract remains a declared deviation. | Publisher/installer rollout policy where the pack contract is deployed. |
| P3-04 shared hosting | Streamable, PeerTube, and reusable earlier shared backends have deterministic routing and fixture evidence. Criterion-6 completeness is not established (H-03). | Traffic-based priority and live-instance interoperability where claimed. |
| P3-05 public/API | Internet Archive and earlier API families provide useful bounded fixtures and playlists; priority and rate-limit breadth remain partial (M-02). | Measured demand and real service rate-limit interoperability. |
| P3-06 authentication | Scoped native credential/profile support passes deterministic tests; explicit platform/login gaps remain (M-03). | Opt-in real-profile and account-flow evidence; Windows Chromium support is absent here. |
| P3-07 live media | Strong representative live and LL-HLS evidence, but the full planned behavior matrix is incomplete (M-04). | Opt-in live interoperability and service-specific entitlement evidence. |
| P3-08 impersonation/regional | Versioned Chrome/Firefox, proxy, challenge, and regional fixtures pass; Safari/HTTP3 and regional runners remain absent (M-05). | Regional runner and protected-service interoperability evidence. |

## Gate G3 decision for audited criteria

| Criterion | Repository evidence | External evidence | Decision |
| --- | --- | --- | --- |
| 1. At least 95% measured intended traffic | Correct bounded calculation and local report tooling. | No traffic dataset, window, or published denominator policy. | **Not passable / not measured.** |
| 2. Zero Python fallback and no silent fallback | Python source tripwire passes; fallback inventory validates zero temporary entries and mandatory `python-backed`/`silent` prohibitions. Source review found no hidden execution bridge in audited high-priority paths. | No external dependency for the repository invariant; deployment-installed extensions still require their own trust review. | **Repository pass, deployment extensions out of scope.** |
| 3. No unreviewed critical semantic/security difference | Typed severity mechanism and older Phase 1/2 reviews exist. | No Phase 3 beta semantic review ledger; independent security audit not closed in this audit. | **Not demonstrated.** |
| 6. Complete evidence for high-value beta sites | 28 representative routes and nine risk classes; strong per-extractor fixtures. | No measured high-value set; no complete per-site/per-axis matrix. | **Not demonstrated.** |
| 7. Safe live/regional/authenticated canaries | Bounded opt-in framework, secret handles, redacted records, no manifest mutation path. | No executed live/auth/region canary records or attributable runners. | **Not demonstrated.** |

## Recommended closure order

1. Fix the six stale manifest references and make evidence existence an
   automated local invariant.
2. Define the beta observation producer/review workflow and close all critical
   Phase 3 semantic reports; incorporate the independent security audit.
3. Collect an opt-in traffic window, publish its denominator policy, and derive
   the authoritative high-value site set and evidence matrix.
4. Execute independently bounded live, authenticated, and regional canaries
   through approved deployment runners; keep their records non-authoritative
   for compatibility claims.
5. Reconcile P3-05 through P3-08 deviations, then evaluate G3 criteria again.
