# YouTube production-parity remaining-gap ledger

Pinned baseline: `origin/main` @ `848f964` (includes PR #83 shared renderer,
PR #84 crash-safe finite-VOD SABR resume, PR #88 PO-token lifecycle /
SABR_ERROR / RELOAD_PLAYER_RESPONSE / signed extraction refresh).

Pinned yt-dlp reference: `aefce1eea4d0b6bab1ec2bd3beff09bff91a39c8`.
Pinned SABR wire provenance: LuanRT/googlevideo @ `d2fa40d7` (existing evidence).

This document freezes the remaining attributable scope for
`codex/youtube-production-parity`. Merged workstreams are treated as baseline
only where current automated tests and evidence files support them.

## Baseline (do not redo)

| Workstream | Status on main | Evidence |
|------------|----------------|----------|
| A crash-safe finite-VOD SABR resume | Implemented | `youtubeump` checkpoint/resume tests; PR #84 |
| B PO-token lifecycle | Implemented | `youtubepot` skew/single-flight/episode; PR #88 |
| C SABR_ERROR + RELOAD_PLAYER_RESPONSE | Implemented (attributable cases) | `youtubeump` recovery + `ReloadYouTubePlayer`; PR #88 |
| E signed media refresh (SABR) | Implemented | product SABR refresh coordinator; PR #88 |
| F shared renderer engine | Implemented core walker | PR #83; `YOUTUBE_BROWSE_RENDERER_EVIDENCE.md` |

## Remaining mandatory gaps

### Workstream D — Innertube client breadth (mandatory)

Current matrix:

- Anonymous format recovery: `ANDROID`, `ANDROID_VR` only
- Authenticated recovery: webpage `WEB` only
- Music: isolated `WEB_REMIX` (browse/search only; not video format recovery)

Gaps to close in this PR:

- Add bounded high-value anonymous profiles from the pinned client table:
  `web_safari`, `ios`, `mweb`
- Add bounded authenticated profiles: `tv_downgraded`; `web_creator` only when
  an attributable premium-subscriber signal is present
- Exact identity isolation (name/ID/version/UA/origin/visitor/auth policy)
- No authenticated→anonymous downgrade; no WEB↔WEB_REMIX leakage
- Deterministic rotation order, bounded attempts, redacted failure diagnostics
- Reject conflicting SABR inventories across incompatible candidates (preserve
  existing first-successful-candidate SABR selection)
- Adversarial tests for cancellation between attempts, cookie isolation, and
  contradictory/malformed player responses

### Workstream F — remaining renderer breadth (mandatory remainder)

Already covered by PR #83 walker: video/grid-video/Shorts/reel/playlist/channel/
lockup/shelf/Music list/community posts; custom tabs; channel search.

Remaining gaps:

- `showRenderer` / `gridShowRenderer` playlist emission
- Playlist-level `playlist_count` / `view_count` pre-fetch metadata
- Entry availability badges (private / premium / members-only / unlisted) on
  URL results without inventing unsupported download claims
- Dedicated consumable `/hashtag/` route so validated hashtag tiles can emit
  registered URL results (tiles currently omit until a consumer exists)
- Preserve Music isolation and fail-closed premium/authenticated Music behavior
- Continuation identity and counts must remain bounded and deterministic

Explicitly deferred (not claimed):

- Arbitrary unregistered Music browse prefixes
- Hashtag nested expansion beyond the dedicated route
- Authenticated/premium Music browse success

### Workstream C remainder — STREAM_PROTECTION_STATUS

Fail-closed blocker: LuanRT `stream_protection_status.proto` and product
attestation mint/retry lifecycle are not attributable enough in-repo to safely
retry. Part 58 remains `ErrUnsupportedDirective`. Do not invent recovery.

### Workstream G — live/post-live SABR

Fail-closed blocker: finite-VOD SABR request lifecycle, sequence, refresh, and
completion semantics are evidenced; live/post-live SABR sequence/window/
completion semantics are not. Preserve `ErrLiveUnsupported` /
`ErrUnsupportedDirective` for live metadata. Existing non-SABR live-from-start
and post-live DVR paths remain separate and unchanged.

## Architecture freeze (remaining work)

| Component | Intended behavior | State ownership | Secrets | Budgets | Failure |
|-----------|-------------------|-----------------|---------|---------|---------|
| Anonymous client rotator | Try pinned profiles in fixed order; cookie-isolated POST | per-extract local | none persisted | ≤ profile count (≤8) | skip retryable; cancel aborts; terminal typed |
| Authenticated client rotator | Webpage WEB then auth profiles; never anon fallback | SID session from jar + ytcfg | SID hash headers only | ≤ auth profile count | auth errors typed; no cookie copy across clients |
| Premium gate | `web_creator` only with attributable premium signal | page/initialData derived | none | 1 extra attempt | omit profile if signal absent |
| Renderer show/hashtag | Shared walker + dedicated hashtag consumer | policy-gated walk | none | existing renderer depth/entry limits | omit hostile; fail closed on identity |
| Playlist counts | Parse sidebar/header counts into playlist Info | extract-local | none | bounded text parse | omit if unparseable |
| Availability badges | Emit `availability` on Entry when badge attributable | entry metadata | none | badge list bound | omit unknown badges |
| STREAM_PROTECTION | unchanged fail-closed | n/a | n/a | n/a | unsupported |
| Live SABR | unchanged fail-closed | n/a | n/a | n/a | unsupported |

## Canary

Add opt-in YouTube interop harness (disabled by default, secret handles,
redaction, resource bounds). Must not run under ordinary `go test ./...`.

## Shared-file policy

Defer README / SUPPORTED_SITES / parity_manifest YouTube section edits until
implementation stabilizes; rebase and reconcile semantically before final push.
