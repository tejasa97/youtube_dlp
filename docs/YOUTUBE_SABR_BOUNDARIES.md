# Retained YouTube SABR protocol boundaries

Scope decision: 2026-07-25.

Status: non-goal boundary record for the retained finite-VOD SABR
implementation after PR #88. Nothing in this document is an active
production-parity blocker or roadmap workstream.

Pinned wire provenance remains LuanRT/googlevideo @
`d2fa40d761034a286cf60ee033653307a1295b0c` and davidzeng0 `ump.md` notes already
recorded in `docs/YOUTUBE_SABR_EVIDENCE.md`.

## STREAM_PROTECTION_STATUS

| Item | Decision |
|------|----------|
| Wire part | UMP part 58 (`PartStreamProtectionStatus`) |
| Current behavior | `ErrUnsupportedDirective` / critical unsupported |
| Why not recovered | In-repo evidence names the part and treats protection changes as fail-closed. There is no attributable attestation mint/retry request lifecycle, challenge binding, or completion semantics pinned for the Go transport. |
| Allowed claim | Recognized and rejected |
| Forbidden claim | Retry, attestation mint, or silent skip |

Do not invent fixtures that mark STREAM_PROTECTION recovery as compatible.
Recovery remains an unsupported non-goal.

## Live / post-live SABR

| Item | Decision |
|------|----------|
| Current behavior | `youtubeump.ErrLiveUnsupported` / extractor `ErrUnsupported` for live SABR metadata; live metadata part 31 remains critical unsupported |
| Finite VOD SABR | Implemented (resume, SABR_ERROR, RELOAD, signed refresh) |
| Why live SABR not implemented | Request lifecycle, sequence windowing, missed-window recovery, and completion semantics for active live / post-live SABR are not justified from the pinned primary evidence set used for finite VOD. |
| Adjacent non-SABR paths | Bounded live-from-start and finite post-live DVR remain separate product paths and are not SABR claims |

Preserve fail-closed behavior. Synthetic live SABR framing alone must not flip
compatibility. Live/post-live SABR is not planned follow-up work.

## Evidence hygiene

- Observed wire facts stay in `YOUTUBE_SABR_EVIDENCE.md`.
- Deliberate Go hardening (identity binding, budgets, redaction) stays labeled as such.
- This file records maintenance boundaries only. New evidence does not reopen
  SABR scope without an explicit project decision.
