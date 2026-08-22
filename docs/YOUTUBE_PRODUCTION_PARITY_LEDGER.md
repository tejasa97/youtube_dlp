# YouTube production-parity evidence ledger

Pinned yt-dlp reference: `aefce1eea4d0b6bab1ec2bd3beff09bff91a39c8`.

## Implemented scope

| Component | Status | Evidence |
|------------|--------|----------|
| PO-token lifecycle | Implemented | `docs/YOUTUBE_POT_EVIDENCE.md` |
| Shared renderer engine | Implemented core walker | `YOUTUBE_BROWSE_RENDERER_EVIDENCE.md` |
| Innertube client breadth | Implemented | `docs/YOUTUBE_CLIENT_BREADTH_EVIDENCE.md`; `youtube_client_test.go` |
| Renderer coverage | Implemented | show/availability/counts/hashtag; browse evidence |
| Canary harness | Implemented (opt-in dry-run) | `internal/youtubecanary`; `cmd/youtube-canary` |

Outside the current claim: unregistered Music browse prefixes; authenticated/premium
Music browse success; live canary network execution.

## Retained extension excluded from parity

The repository contains a bounded finite-VOD SABR/UMP implementation and
related recovery machinery. SABR is excluded from the production-parity claim.
Its unsupported live and full-client boundaries are non-goals rather than
blockers. See `docs/YOUTUBE_SABR_BOUNDARIES.md` for the current boundary.

## Architecture freeze

| Component | Behavior | Budgets | Failure |
|-----------|----------|---------|---------|
| Anonymous client rotator | visionos → android → web_safari → ios → mweb (android_vr removed from defaults, #17461, retained fail-closed); made-for-kids web_embedded→tv fallback when JS available | ≤8 | typed; cancel aborts |
| Authenticated client rotator | WEB → premium web_creator→tv→web; else web_embedded→tv→web (+web_creator only if age-gated); GVS fail-closed | ≤8 | no anon downgrade; age-gate attributable |
| Renderer show/hashtag | shared walker + `youtube_hashtag` | existing depth/entry limits | omit hostile |
| Playlist counts | sidebar/header parse into Info | bounded deterministic token | omit if unparseable |
| Availability badges | Entry.availability order-independent precedence | badge walk bound | omit unknown/limit errors |
