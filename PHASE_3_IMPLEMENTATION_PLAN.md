# Phase 3 Implementation Plan: High-Value Coverage and Beta

Status: Active implementation baseline  
Date: 2026-07-19  
Behavioral baseline: `yt-dlp/yt-dlp@aefce1eea4d0b6bab1ec2bd3beff09bff91a39c8`

## 1. Objective

Phase 3 turns the native alpha into a measurable beta. It prioritizes work by
observed intended traffic, expands high-value and difficult extractor behavior,
proves a compatible plugin/SDK upgrade, and establishes the local-first
operational evidence needed to detect and repair breakage quickly.

The compatibility promise remains capability- and corpus-scoped. Measured
coverage never converts an untested behavior into a compatible claim. Live
canaries are interoperability evidence, not deterministic conformance
authority. No product, build, release, updater, plugin, test, telemetry, or
operations path may require Python.

GitHub Actions is intentionally optional. Every deterministic acceptance gate
must be runnable locally or in a user-provided native environment. Phase 2's
unobserved Windows-native result remains explicit until native evidence is
available; Phase 3 work does not silently waive or retroactively satisfy it.

## 2. Gate G3

Phase 3 exits only when all of the following are true:

1. Native Go handles at least 95% of measured intended traffic when suitable
   traffic data exists. The denominator, sampling window, exclusions, unknowns,
   and overflow are published with the result.
2. Every high-priority capability has zero Python fallback, and the
   machine-readable inventory contains no silent fallback.
3. No unreviewed critical semantic or security difference remains in beta
   coverage.
4. A representative major-site regression can be classified, reproduced,
   patched, and verified within 24–48 hours using documented local tooling.
5. The extractor/plugin SDK and pack contracts complete at least one compatible
   v1-to-v1.x upgrade with old-host/new-plugin and new-host/old-plugin evidence.
6. High-value beta sites have deterministic routing, request, metadata, format,
   playlist, warning, protocol, failure, cancellation, and security evidence.
7. Live, regional, and authenticated canaries are opt-in, secret-safe,
   independently bounded, and incapable of changing compatibility claims.

## 3. Measurement rules

Coverage uses privacy-safe observations rather than raw requests. Allowed
dimensions are registered extractor/capability IDs, coarse outcome categories,
declared protocol classes, and bounded version identifiers. Raw URLs, query
strings, paths, titles, usernames, cookie values, authorization material,
arbitrary errors, and free-form user labels are prohibited.

The traffic denominator includes all intended operations presented to the Go
product during the declared window. Unsupported, unknown, failed, cancelled,
and overflow observations remain in the denominator unless the published policy
explicitly identifies a non-product probe. Sampling and dropped observations
must be reported; a coverage estimate with unknown loss cannot support G3.

## 4. Work packages

### P3-01: Privacy-safe measurement and coverage model

Implement bounded concurrent observations, deterministic snapshots and merge,
overflow accounting, canonical export, coverage calculation, and a threat model
that prevents identifiers or secrets from becoming telemetry dimensions.

Evidence: concurrency/race tests, cardinality and overflow tests, canonical
round trips, hostile-label rejection, redaction tests, fuzzing, and product
integration with telemetry disabled by default.

### P3-02: Semantic shadow comparison

Extend differential conformance to compare routing, request shape, normalized
metadata, formats, playlists, warnings, protocol usability, errors, and side
effects. Classify differences by domain and review severity; persist only
bounded redacted canonical observations.

Evidence: deterministic paired corpora, missing/null and order policies,
secret-bearing URL/header tests, cancellation and bounds, fuzzing, and stable
reports that can become regression fixtures without a Python runtime.

### P3-03: Extractor/plugin SDK v1.x upgrade

Publish compatible optional-field and capability evolution rules, permission
invariants, old/new host matrices, canonical transcripts, deprecation policy,
and signed-pack compatibility evidence. Reject unknown major versions,
permission escalation, interpreter declarations, and ambiguous downgrade.

Evidence: v1.0 and v1.x fixture plugins/packs, cross-version exchanges,
malformed/crashed/cancelled cases, deterministic package hashes, and SDK docs.

### P3-04: Shared hosting and embed breadth

Prioritize backend families that unlock many URLs. Implement reusable helpers
before site wrappers and prevent generic routing from claiming opaque or
provider-specific inputs.

Evidence per family: attributable fixtures, routing, metadata, formats,
playlists, failures, cancellation, protocol downloads, fuzzing, and explicit
legacy API deviations.

### P3-05: High-usage public and API extractors

Use measured demand when available; otherwise publish the documented proxy used
for ordering. Implement bounded pagination, rate-limit behavior, API error
classification, and lazy reusable playlists.

### P3-06: Authentication and browser credentials

Expand scoped authentication, account/session failures, login boundaries,
multi-profile behavior, and supported browser stores without exposing secrets.
Real-profile tests remain opt-in and may never read a developer profile by
default.

### P3-07: Live and scheduled media

Cover scheduled/upcoming, active, DVR, ended, replay, low-latency, discontinuity,
refresh, ad-marker, and cancellation behavior across representative sites.
Returned media must drive usable deterministic HLS/DASH/ISM downloads.

### P3-08: Impersonation, JavaScript, proxy, and regional behavior

Expand explicit browser profiles, proxy compatibility, challenge execution,
geo failures, and regional runners. Unknown profiles and unsupported proxy
combinations fail closed rather than falling back to a weaker transport.

### P3-09: Pack distribution and sandbox maturity

Complete the v1.x pack upgrade path, publisher/revocation metadata, permission
review, offline discovery, and maintained platform sandbox decision. Production
signing custody remains an external operational choice.

### P3-10: Canary and runner framework

Implement opt-in bounded canaries, secret handles, regional/authenticated runner
contracts, deterministic replay capture, expiry, rate limits, and result
redaction. A canary cannot write the parity manifest.

### P3-11: Diagnosis and patch operations

Produce local dashboards/reports for coverage, failure class, mismatch class,
fallback, overflow, and patch latency. Exercise a timed regression drill from
observation through deterministic fixture, fix, and verification.

### P3-12: Beta exit and security review

Reconcile every claim and deviation, audit measurement privacy and hostile
inputs, run the complete local matrix, verify native environments that are
actually available, and write `docs/PHASE_3_EXIT_REVIEW.md` mapping every G3
criterion to authoritative evidence.

## 5. Wave order and subagent lanes

### Wave 1: Measurement and contracts

- Lane A owns `internal/telemetry/**` and telemetry conformance fixtures.
- Lane B owns `internal/differential/**` and Phase 3 differential fixtures.
- Lane C owns isolated plugin-upgrade conformance packages and fixtures.
- Primary owns public APIs, events, manifest, CLI, shared docs, and integration.

### Wave 2: Shared and high-usage breadth

- Lane A: shared hosting/embed backends.
- Lane B: high-volume public/API sites.
- Lane C: channel/search/playlist and pagination breadth.
- Primary: shared registry/helpers, measured priority, protocol integration.

### Wave 3: Difficult runtime behavior

- Lane A: authenticated and browser-cookie sites.
- Lane B: live/scheduled/DVR/low-latency behavior.
- Lane C: impersonation, challenge, proxy, and regional behavior.
- Primary: credential/transport policy and security integration.

### Wave 4: SDK, packs, and distribution

- Lane A: SDK documentation/examples and v1.x compatibility matrix.
- Lane B: signed-pack lifecycle and sandbox portability.
- Lane C: offline pack catalog, revocation, and deterministic distribution.
- Primary: public ABI and trust-policy decisions.

### Wave 5: Operations

- Lane A: bounded live canaries.
- Lane B: regional/authenticated runner contracts.
- Lane C: local dashboards, mismatch triage, and patch-latency drill.
- Primary: operational policy and deterministic fixture promotion.

### Wave 6: Independent G3 audits

- Audit A: coverage and extractor parity.
- Audit B: security, credentials, telemetry privacy, and isolation.
- Audit C: releases, operations, reproducibility, and ABI upgrade.
- Primary resolves findings and owns the exit decision.

## 6. Integration discipline

Each lane receives a dedicated `codex/*` branch and isolated worktree with
strict file ownership. It implements production code, deterministic fixtures,
success/failure/cancellation/security tests, fuzzing where inputs are parsed,
provenance, and deviations. The primary reviews and cherry-picks sequentially;
agents never commit in the primary worktree.

After each integration run scoped unit/race/vet/fuzz checks. After each wave run
formatting, module drift, parity validation, full unit/race/vet, the complete
fuzz inventory, no-cgo Linux/macOS/Windows builds, vulnerability scanning,
reproducibility, and Python-free scratch-container checks. Native-runtime claims
are made only for environments actually executed.

## 7. Definition of done

Phase 3 is complete only when every P3 package is complete or has an accepted
non-critical deviation; the coverage denominator and result are reproducible;
G3 has concrete evidence for all seven criteria; the compatible ABI upgrade is
executed; all high-priority paths are Python-free; and the committed Phase 3
exit review contains no unsupported claim.
