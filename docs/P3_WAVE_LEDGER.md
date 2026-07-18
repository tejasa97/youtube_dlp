# Phase 3 wave ledger

Updated: 2026-07-19

This ledger records isolated lane delivery and primary integration. Commit
claims are added only after review and scoped verification.

## Wave 1: Measurement and contracts

| Lane | Branch / worktree | Ownership | State | Delivery |
| --- | --- | --- | --- | --- |
| Privacy-safe measurement | `codex/p3-telemetry` / `youtube_dlp-p3-telemetry` | `internal/telemetry/**`, `conformance/telemetry/**` | Integrated | `b36d549`, public/CLI integration `6e41260`, `0b232ec` |
| Semantic differential | `codex/p3-differential` / `youtube_dlp-p3-differential` | `internal/differential/**`, `conformance/differential/phase3/**` | Integrated | `3e6d453`, review hardening `e4a466c` |
| ABI upgrade conformance | `codex/p3-sdk-upgrade` / `youtube_dlp-p3-sdk-upgrade` | `internal/plugin/upgrade/**`, `conformance/plugin/abi-v1.1/**` | Integrated | `00215f2`, native compatibility matrix `39764c1` |
| Primary integration | `main` / `youtube_dlp` | public API, events, manifest, CLI, docs, shared policy | Complete | Opt-in full-denominator telemetry, semantic-shadow claim, and actual ABI matrix integrated |

## Wave 2: Shared and high-usage breadth

| Lane | Branch / worktree | Ownership | State | Delivery |
| --- | --- | --- | --- | --- |
| Federated shared hosting | `codex/p3-peertube` / `youtube_dlp-p3-peertube` | PeerTube extractor and fixtures | Integrated | `c49b388`, primary registration and claim pending commit |
| Public API and playlists | `codex/p3-internetarchive` / `youtube_dlp-p3-internetarchive` | Internet Archive extractor and fixtures | Integrated | `b1f5ca6`, literal-plus review fix and product registration pending commit |
| Direct shared hosting | `main` / `youtube_dlp` | Streamable extractor, registry, manifest | Integrated | `8b5b851` |
| Primary integration | `main` / `youtube_dlp` | registry, public API, manifest, priority policy | Complete | Three Python-free extractor families integrated with scoped unit/race/vet/fuzz evidence |

## Gate boundary

GitHub Actions is intentionally disabled and is not Phase 3 evidence. Local
unit/race/vet/fuzz, cross-build, vulnerability, reproducibility, and scratch
container checks remain authoritative. Phase 2's Windows-native observation is
still unavailable and is not silently converted into a pass.
