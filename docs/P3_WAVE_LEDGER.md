# Phase 3 wave ledger

Updated: 2026-07-19

This ledger records isolated lane delivery and primary integration. Commit
claims are added only after review and scoped verification.

## Wave 1: Measurement and contracts

| Lane | Branch / worktree | Ownership | State | Delivery |
| --- | --- | --- | --- | --- |
| Privacy-safe measurement | `codex/p3-telemetry` / `youtube_dlp-p3-telemetry` | `internal/telemetry/**`, `conformance/telemetry/**` | In progress | Pending |
| Semantic differential | `codex/p3-differential` / `youtube_dlp-p3-differential` | `internal/differential/**`, `conformance/differential/phase3/**` | In progress | Pending |
| ABI upgrade conformance | `codex/p3-sdk-upgrade` / `youtube_dlp-p3-sdk-upgrade` | `internal/plugin/upgrade/**`, `conformance/plugin/abi-v1.1/**` | In progress | Pending |
| Primary integration | `main` / `youtube_dlp` | public API, events, manifest, CLI, docs, shared policy | In progress | Phase plan drafted |

## Gate boundary

GitHub Actions is intentionally disabled and is not Phase 3 evidence. Local
unit/race/vet/fuzz, cross-build, vulnerability, reproducibility, and scratch
container checks remain authoritative. Phase 2's Windows-native observation is
still unavailable and is not silently converted into a pass.
