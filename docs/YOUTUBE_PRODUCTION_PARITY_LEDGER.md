# YouTube production-parity remaining-gap ledger

Pinned baseline: `origin/main` @ `848f964`.

Pinned yt-dlp reference: `aefce1eea4d0b6bab1ec2bd3beff09bff91a39c8`.

## Baseline (do not redo)

| Workstream | Status on main | Evidence |
|------------|----------------|----------|
| B PO-token lifecycle | Implemented | `docs/YOUTUBE_POT_EVIDENCE.md` |
| F shared renderer engine | Implemented core walker | PR #83; `YOUTUBE_BROWSE_RENDERER_EVIDENCE.md` |

## This PR — implemented remainders

| Workstream | Status | Evidence |
|------------|--------|----------|
| D Innertube client breadth | Implemented | `docs/YOUTUBE_CLIENT_BREADTH_EVIDENCE.md`; `youtube_client_test.go` |
| F renderer remainders | Implemented | show/availability/counts/hashtag; browse evidence |
| Canary harness | Implemented (opt-in dry-run) | `internal/youtubecanary`; `cmd/youtube-canary` |

Deferred (not claimed): unregistered Music browse prefixes; authenticated/premium
Music browse success; live canary network execution.

## Retained extension excluded from parity

PRs #68, #78, #80, #84, and #88 delivered a bounded finite-VOD SABR/UMP
implementation and related recovery machinery. That code, its tests, and its
technical evidence remain in the repository, but SABR is no longer a
production-parity workstream. Its unsupported live/full-client boundaries are
non-goals rather than blockers and do not contribute to roadmap or completion
estimates. See `docs/YOUTUBE_SABR_BOUNDARIES.md` for the frozen maintenance
boundary.

## Architecture freeze

| Component | Behavior | Budgets | Failure |
|-----------|----------|---------|---------|
| Anonymous client rotator | android → android_vr → web_safari → ios → mweb | ≤8 | typed; cancel aborts |
| Authenticated client rotator | WEB → premium tv→web_creator; else tv→web_safari (+web_creator only if age-gated); GVS fail-closed | ≤8 | no anon downgrade; age-gate attributable |
| Renderer show/hashtag | shared walker + `youtube_hashtag` | existing depth/entry limits | omit hostile |
| Playlist counts | sidebar/header parse into Info | bounded deterministic token | omit if unparseable |
| Availability badges | Entry.availability order-independent precedence | badge walk bound | omit unknown/limit errors |

## Shared-file policy

README / SUPPORTED_SITES / parity_manifest YouTube sections are reconciled after
rebase onto current main, preserving concurrent non-YouTube claims.
