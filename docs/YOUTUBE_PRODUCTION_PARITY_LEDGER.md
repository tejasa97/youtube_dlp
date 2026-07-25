# YouTube production-parity remaining-gap ledger

Pinned baseline: `origin/main` @ `848f964` (includes PR #83 shared renderer,
PR #84 crash-safe finite-VOD SABR resume, PR #88 PO-token lifecycle /
SABR_ERROR / RELOAD_PLAYER_RESPONSE / signed extraction refresh).

Pinned yt-dlp reference: `aefce1eea4d0b6bab1ec2bd3beff09bff91a39c8`.
Pinned SABR wire provenance: LuanRT/googlevideo @ `d2fa40d7` (existing evidence).

## Baseline (do not redo)

| Workstream | Status on main | Evidence |
|------------|----------------|----------|
| A crash-safe finite-VOD SABR resume | Implemented | `youtubeump` checkpoint/resume tests; PR #84 |
| B PO-token lifecycle | Implemented | `youtubepot` skew/single-flight/episode; PR #88 |
| C SABR_ERROR + RELOAD_PLAYER_RESPONSE | Implemented (attributable cases) | `youtubeump` recovery + `ReloadYouTubePlayer`; PR #88 |
| E signed media refresh (SABR) | Implemented | product SABR refresh coordinator; PR #88 |
| F shared renderer engine | Implemented core walker | PR #83; `YOUTUBE_BROWSE_RENDERER_EVIDENCE.md` |

## This PR — implemented remainders

| Workstream | Status | Evidence |
|------------|--------|----------|
| D Innertube client breadth | Implemented | `docs/YOUTUBE_CLIENT_BREADTH_EVIDENCE.md`; `youtube_client_test.go` |
| F renderer remainders | Implemented | show/availability/counts/hashtag; browse evidence |
| Canary harness | Implemented (opt-in dry-run) | `internal/youtubecanary`; `cmd/youtube-canary` |

## Explicit fail-closed blockers

| Workstream | Blocker | Evidence |
|------------|---------|----------|
| C STREAM_PROTECTION_STATUS | No attributable attestation mint/retry lifecycle | `docs/YOUTUBE_PROTOCOL_BLOCKERS.md` |
| G live/post-live SABR | No attributable live sequence/window/completion semantics | `docs/YOUTUBE_PROTOCOL_BLOCKERS.md` |

Deferred (not claimed): unregistered Music browse prefixes; authenticated/premium
Music browse success; live canary network execution.

## Architecture freeze

| Component | Behavior | Budgets | Failure |
|-----------|----------|---------|---------|
| Anonymous client rotator | android → android_vr → web_safari → ios → mweb | ≤8 | typed; cancel aborts |
| Authenticated client rotator | WEB → tv_downgraded → premium web_creator | ≤8 | no anon downgrade |
| Renderer show/hashtag | shared walker + `youtube_hashtag` | existing depth/entry limits | omit hostile |
| Playlist counts | sidebar/header parse into Info | bounded text | omit if unparseable |
| Availability badges | Entry.availability from badges | badge walk bound | omit unknown |
| STREAM_PROTECTION / live SABR | fail-closed | n/a | unsupported |

## Shared-file policy

README / SUPPORTED_SITES / parity_manifest YouTube sections are reconciled after
rebase onto current main, preserving concurrent non-YouTube claims.
