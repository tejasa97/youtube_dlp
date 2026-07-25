# YouTube finite-VOD SABR recovery architecture

Status: frozen design for the transport-recovery slice. Extends PR #84
crash-safe resume; does not redesign checkpoints or open live SABR.

## Architecture table

| Component | Responsibility | Identity / binding | Budgets | Provenance |
|-----------|----------------|--------------------|---------|------------|
| `youtubepot.Director` | Process-local PO cache with expiry-skew refresh, per-key single-flight, panic/malformed recovery | Context, client, visitor/data-sync, video, player URL, authenticated | `RefreshSkew` ≤ 5m (default 30s); `MaxForcedRefreshPerOperation` = 2; ≤1 forced bypass per rejection episode | yt-dlp `pot/_director.py` @ `aefce1ee`; Go-native extension |
| `youtubepot.Episode` | Extension-hook forced-refresh budget for embedding callers with attributable `ErrTokenRejected` | Same Director cache key ⇒ shared flight/refresh; incompatible keys never share | One pending bypass per `SignalRejection`; total forced ≤ 2/op | Extension API; not product-integrated SABR recovery |
| `youtubeump` SABR_ERROR parser | Canonical protobuf `type` (f1 string) + `code` (f2 int32) | N/A (wire-only) | Type ≤ 256 B; reject duplicate/unknown critical/oversized | LuanRT `sabr_error.proto` @ `d2fa40d7` |
| `youtubeump` RELOAD_PLAYER_RESPONSE parser | Canonical `ReloadPlaybackContext` → `ReloadPlaybackParams.token` (f1→f1) | Token redacted; never logged | Token ≤ 4096 B; nonempty required | LuanRT `reload_player_response.proto` @ `d2fa40d7`; davidzeng0 `ump.md` semantics |
| `extractor.ReloadYouTubePlayer` | Focused `/player` reload placing `playbackContext.reloadPlaybackContext.reloadPlaybackParams.token` | Exact video + client name/ID/version + visitor + UA | Token ≤ 4096 B | LuanRT/googlevideo `examples/sabr-shaka-example` @ `d2fa40d7` |
| `youtubeump.Downloader` recovery | Transactional response consume; HTTP retries reuse immutable body; SABR_ERROR retry vs reload callback | Committed cookie/redirect/contexts/token/inventory untouched on failed response; refresh/reload require ustreamer hash equality | HTTP `Attempts` (existing); `MaxSabrErrorRecoveries` = 3; `MaxReloadAttempts` = 2 | LuanRT `SabrStream.ts` retry + `SabrStreamingAdapter.ts` reload |
| Product SABR refresh coordinator | Context-aware keyed extraction flight; accept only exact identity match | Video, client name/ID/version, visitor, itag, track, duration, DRC, audio track, ustreamer hash; exact signed query bytes preserved | `MaxSabrRefreshAttempts` = 2; compatible A/V share one extraction; canceled waiters return promptly | Product live-refresh pattern scoped to SABR markers |

## Typed decisions (evidence-only)

| Signal | Decision | Notes |
|--------|----------|-------|
| Well-formed `SABR_ERROR` with nonempty `type` and present `code` | Retryable within `MaxSabrErrorRecoveries`; no control commit | Matches `SabrStream.handleSabrError` → `executeWithRetry`. No code-specific branching (codes not enumerated in proto). |
| Canonical `RELOAD_PLAYER_RESPONSE` with nonempty reload token | Reload via typed callback → `ReloadYouTubePlayer` with attributable `reloadPlaybackContext`; replace signed URL (+ optional PO) atomically; ustreamer hash must match; separate reload budget | Matches davidzeng0 “new `/player` request” and GoogleVideo sabr-shaka reload placement. |
| Malformed / duplicate / empty / oversized / unknown critical fields | Fail closed (`ErrInvalidProtobuf` / typed rejection) | No inference. |
| `STREAM_PROTECTION_STATUS`, live metadata, unlisted parts | `ErrUnsupportedDirective` | Protection changes remain unsupported. |
| `youtubepot.ErrTokenRejected` | Extension hook: arms ≤1 forced cache bypass on an operation `Episode` | Embedding callers only; SABR_ERROR alone does not invent POT rejection. Product SABR path does not call `SignalRejection`. |
| Resume with configured `Refresh` callback | Fail closed on callback/identity failure | Must not fall back to stale supplied inventory. Absence of `Refresh` may continue with caller-supplied material. |

## Explicit unsupported variants

- Live / post-live SABR recovery
- Code-specific SABR_ERROR recovery beyond generic retry
- `STREAM_PROTECTION_STATUS` attestation mint/retry
- davidzeng0 alternate `ReloadPlayerResponse` nesting (unnamed field wrappers) — not the pinned LuanRT shape
- Generic media URL refresh outside SABR/signed extraction identity
- Checkpoint schema redesign; CLI flags; new Innertube client profiles

## Secret hygiene

PO tokens, reload tokens, playback cookies, visitor data, and signed CDN URLs
never enter extraction JSON, checkpoints/markers, filenames, events, logs,
errors, telemetry, or test failure text. Redaction reuses existing
`youtubeump` / `network` helpers.
