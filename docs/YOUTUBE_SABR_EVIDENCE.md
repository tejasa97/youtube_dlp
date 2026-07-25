# YouTube SABR/UMP evidence

Status: partial — bounded finite-VOD slice only. Synthetic fixtures only; no claim of full SABR parity.

## Supported wire subset

- UMP framing with 1–5 byte prefix varints (LuanRT/GoogleVideo `UmpReader.ts`, commit `d2fa40d761034a286cf60ee033653307a1295b0c`).
- Response parts handled: `FORMAT_INITIALIZATION_METADATA` (42), `MEDIA_HEADER` (20), `MEDIA` (21), `MEDIA_END` (22), `NEXT_REQUEST_POLICY` (35), `SABR_REDIRECT` (43), `SABR_ERROR` (44), `RELOAD_PLAYER_RESPONSE` (46), `SABR_CONTEXT_UPDATE` (57), `SABR_CONTEXT_SENDING_POLICY` (59), `END_OF_TRACK` (62).
- Multiplexed responses: unselected itag headers are consumed, length-validated, and discarded without writing; selected format initialization metadata is required.
- `NEXT_REQUEST_POLICY`: canonical protobuf validation for `backoff_time_ms` (field 4, max 30s) and embedded `PlaybackCookie` (field 7, max 4096 bytes, known fields 1/2 varints and 7/8 `FormatID`). At most one policy per response; cookie replaces prior state on success; policy without cookie preserves prior cookie. Backoff is applied cancellation-safely before the next POST when the track is not yet complete.
- `SABR_REDIRECT` (43): exactly one nonempty field-1 URL (≤4096 bytes), validated with `ValidateSABRURL` (HTTPS, trusted googlevideo host, no userinfo/port/fragment/encoded separators). At most one redirect per response. No HTTP redirect following. POSTs always use the exact original signed URL bytes (plus normal `rn` progression). Loop detection uses a separate canonical key derived only after validation: lowercase scheme/host and trailing-dot host equivalence, with signed path/query bytes preserved exactly (no reordering or decoding). The initial endpoint is seeded with the same canonicalizer. Committed UMP redirects are capped at 8; the 9th redirect fails before commit. The redirected URL becomes the next-round POST target only after a successful response commit.
- `SABR_CONTEXT_UPDATE` (57): `type` (positive int32), `scope` (0..4), nonempty `value` (≤16KiB), `send_by_default`, `write_policy` (0 unspecified / 1 overwrite / 2 keep-existing). Keep-existing skips updates when the type is already stored (including send_by_default). Stored context entries and the active/orphan ID set are each capped at 64; cumulative stored values are capped at 256KiB. Bound violations reject transactionally without mutating caller state.
- `SABR_CONTEXT_SENDING_POLICY` (59): repeated `start`/`stop`/`discard` int32 lists, accepting unpacked varints and packed length-delimited encodings evidenced by generated protobuf code. Multiple policy parts in one response are applied in arrival order (matching pinned `SabrStream.handleSabrContextSendingPolicy`), with one response-wide operation budget of ≤192 start/stop/discard values. Within each part, application order is start, then stop, then discard. Discard removes stored values only; orphaned active marks are inert because request marshalling iterates stored entries only.
- Active contexts marshal into `StreamerContext` field 5 (`type`/`value`) in ascending type order; inactive stored types marshal into packed field 6 in ascending order.
- Context-only and redirect-only responses may continue immediately to the next round without media, still counting toward `MaxRounds`, redirect/context budgets, and cancellation.
- `END_OF_TRACK` (62): empty payload only, once per response, with no active media headers and after init plus at least one finalized selected segment. Authoritative finite-VOD completion even below declared duration. Duration-based completion remains the fallback when `END_OF_TRACK` is absent. When `END_OF_TRACK` completes the track, no further POST is issued for redirect or backoff.
- `SABR_ERROR` (44): canonical `SabrError` protobuf with required nonempty `type` (field 1, ≤256 bytes) and `code` (field 2 int32). Duplicate fields, wrong wire types, empty type, or missing either field fail closed. A typed `SabrErrorSignal` aborts the response without committing cookie/redirect/context/token/inventory. Recovery retries the same committed state within `MaxSabrErrorRecoveries` (3) with cancellation-safe backoff. Code-specific branching beyond generic retry is unsupported (proto enumerates no codes). Observed issue strings such as `sabr.no_audio_selected` are accepted only as opaque type values.
- `RELOAD_PLAYER_RESPONSE` (46): canonical LuanRT `ReloadPlaybackContext` → nested `ReloadPlaybackParams.token` (field 1 → field 1, nonempty ≤4096 bytes). Duplicate/empty/oversized tokens fail closed. Emits a redacted `ReloadPlayerSignal` (token never appears in `Error`/formatting). Recovery calls an explicit typed reload callback, requires identical video ID and compatible client/visitor/itag/track/duration identity, atomically replaces signed URL + ustreamer (+ optional PO token), clears playback cookie/contexts, resets `rn` to 0, and is capped at `MaxReloadAttempts` (2). davidzeng0 alternate unnamed nested wrappers are unsupported.
- Fail-closed on remaining critical parts: live metadata and `STREAM_PROTECTION_STATUS` (protection changes).
- `VideoPlaybackAbrRequest` protobuf fields: `client_abr_state` (player time, enabled tracks, DRC, audio track id), `selected_format_ids`, `buffered_ranges`, `video_playback_ustreamer_config`, preferred audio/video format ids, and `streamer_context` (`client_info`, `po_token`, `playback_cookie` field 3 once supplied, plus SABR contexts fields 5/6). Visitor data is not a playback cookie and is used only for PO-token binding at download time.
- POST to `serverAbrStreamingUrl` with preserved signed query bytes and `rn` progression, `Content-Type: application/x-protobuf`, `Accept: application/vnd.yt-ump`.
- Credential-isolated transport: no `Cookie` / `Authorization` / `Proxy-Authorization`, no redirect following, no response cookie persistence (`network.Client.DoWithoutCredentialsNoRedirect`), including after cross-CDN googlevideo host changes from UMP redirects.
- Caller `Config.Headers` may supply ordinary safe headers but cannot override or duplicate protected control headers (`Content-Type`, `Accept`, `Accept-Encoding`, `User-Agent`, `Host`, hop-by-hop headers, credentials, or `X-Goog-Visitor-Id`).
- SABR format metadata keeps `url` empty; download dispatch uses `_youtube_sabr` / `_youtube_sabr_server_url` markers. Format selection binds to one player candidate inventory without cross-player merging.
- WEB SABR transport identity requires attributable `ytcfg` evidence: `INNERTUBE_CONTEXT_CLIENT_NAME`, matching `clientName`, `clientVersion`, and `userAgent`.
- Completion requires init segment, at least one selected media segment, and either cumulative selected media duration ≥ declared finite `duration_sec` or a valid `END_OF_TRACK`. Active media headers at response EOF are rejected as truncated state. Transactional control state (cookie, policy backoff, redirect URL, SABR contexts, `END_OF_TRACK` completion flag) is committed only when `consumeStream` returns without error; failed responses leave prior committed control unchanged and return zero `roundControl`. HTTP retries reuse one immutable request body with the pre-commit endpoint/cookie/context. Media bytes written before a failed response may remain in the assembler and are handled by existing sequence replay/dedup on later successful rounds, not rolled back. Post-publish `completed` events are best-effort and do not fail an already published artifact.
- Crash-safe finite-VOD resume: committed init/media segments persist under deterministic output-confined `destination.part` + `destination.part.json` checkpoints. Checkpoints bind a required non-empty bounded video id, client name/version, track kind, format identity, duration, DRC/audio-track id, and a SHA-256 of the ustreamer config. They never store PO tokens, playback cookies, visitor data, SABR contexts, authorization/cookie headers, or signed CDN URLs. Segment bytes are synced before an atomic checkpoint replace; recovery verifies every recorded init/segment digest by streaming the partial file, truncates uncommitted tail bytes, restores buffered ranges / player time / sequence digests, and starts a fresh SABR session (`rn=0`) with newly extracted signed URLs and PO tokens. Sequences in checkpoints must start at zero and increase contiguously. Identical server replays of media and init segments are accepted; changed bytes fail closed. Checkpoint and completion-marker JSON must decode as a single object (EOF after optional whitespace; trailing values/garbage fail closed). Resumable downloads reject empty or oversized video ids (`ErrMissingConfig` / invalid input). Pair A/V sidecar completion is crash-atomic: an identity-bound `destination.sabr.json` marker is made durable while `.part` + checkpoint still exist, then media is published and only then is the checkpoint removed; marker-write failure never deletes recoverable media. Standalone finals leave only published media. Product A/V merges download tracks concurrently into deterministic sidecars (`destination.sabr.{audio|video}.{itag}.{ext}`) with shared cancellation and a serialized event sink so completed audio + partial video (and the reverse) and interrupted merge retries resume independently without deleting valid committed peer progress.
- Signed extraction refresh: operation-scoped product coordinator re-extracts through the existing YouTube extractor path (never by editing stored signed URLs). Compatible A/V identities sharing video/client/visitor share one extraction attempt (`MaxSabrRefreshAttempts` = 2); per-itag materials remain separate. Accepted material must match video, client, visitor binding, itag, track, duration, and DRC/audio-track identity; exact signed query bytes are preserved; trusted-host / no-redirect / credential isolation is enforced. Resume refresh is best-effort when offline re-extraction is unavailable; `RELOAD_PLAYER_RESPONSE` refresh is hard-fail. Mid-session PO tokens resolve through `youtubepot.Director` with expiry skew, single-flight, and operation-scoped forced-refresh episodes.

## Provenance

| Source | Commit / URL | Use |
|--------|----------------|-----|
| LuanRT/GoogleVideo | `d2fa40d761034a286cf60ee033653307a1295b0c` | UMP varints, part IDs, protobuf field numbers, `BufferedRange`, `NextRequestPolicy`, `PlaybackCookie`, `SabrRedirect`, `SabrError`, `ReloadPlaybackContext`/`ReloadPlaybackParams`, `SabrContextUpdate`, `SabrContextSendingPolicy`, `StreamerContext` fields 3/5/6; `SabrStream` SABR_ERROR retry + reload emit semantics |
| davidzeng0/innertube `googlevideo/ump.md` | main @ 2026-07-24 | UMP part semantics documentation (`RELOAD_PLAYER_RESPONSE` ⇒ new `/player` request) |
| ColeSpringer/WaxTap v2.0.1 | `5d4b07dbfad5c2831c35ea7b95006b576e08f694` | Request marshaling shape cross-check (not a dependency) |
| yt-dlp reference | `aefce1eea4d0b6bab1ec2bd3beff09bff91a39c8` | SABR-only detection + PO Director binding behavior; no direct transport |

Synthetic fixtures: `conformance/extractors/youtube/sabr-only-watch.html`, `conformance/media/youtube_sabr_directives/**` (byte-identical rebuilds asserted by `TestDirectiveFixturesByteIdenticalAndSynthetic`), and deterministic UMP bytes in `internal/protocol/youtubeump/*_test.go`.

## Measured bounds

- `MaxRoundBytes` = 64 MiB per SABR response
- `MaxParts` = 10,000 per response
- `MaxActiveHeaders` = 8 concurrent in-flight media headers per response
- `MaxMediaBytes` = 8 GiB per track (shared direct downloader ceiling)
- `MaxRounds` = 64 POST rounds for finite VOD
- `MaxPlaybackCookieBytes` = 4096 per validated cookie
- `MaxPolicyBackoffMs` = 30,000 per `NEXT_REQUEST_POLICY`
- `MaxSabrContexts` = 64 stored context entries and 64 active/orphan IDs
- `MaxSabrContextValueBytes` = 16 KiB per value
- `MaxSabrContextValueBytesTotal` = 256 KiB cumulative
- `MaxSabrContextPolicyOps` = 192 start/stop/discard operations across all sending-policy parts in one response
- `MaxRedirectURLBytes` = 4096
- `MaxDirectiveRedirects` = 8 committed UMP redirects
- `MaxSabrErrorTypeBytes` = 256
- `MaxReloadTokenBytes` = 4096
- `MaxSabrErrorRecoveries` = 3
- `MaxReloadAttempts` = 2
- `MaxSabrRefreshAttempts` = 2
- `MaxCheckpointSegments` = 8192 committed media segments per checkpoint
- `MaxCheckpointBytes` = 1 MiB per checkpoint JSON file
- HTTP `Location` redirects remain rejected (fail-closed)

## Remaining deviations

- Live/post-live SABR and full client parity remain unsupported.
- `STREAM_PROTECTION_STATUS` attestation mint/retry remains fail-closed.
- Code-specific SABR_ERROR recovery beyond generic retry is unsupported.
- davidzeng0 alternate `ReloadPlayerResponse` unnamed nesting is unsupported.
- No claim of parity with WaxTap/GoogleVideo full clients or yt-dlp transport.
- Authenticated WEB SABR-only pages outside the synthetic fixture are not evidenced here.
- PO tokens are resolved at download time through the public provider boundary; they are not exported in extraction JSON.

See also `docs/YOUTUBE_SABR_RECOVERY_ARCHITECTURE.md` for the frozen architecture table.

## Tests

- `go test ./internal/protocol/youtubeump ./internal/youtubepot ./internal/extractor ./internal/format ./internal/network ./pkg/ytdlp`
- Fuzz: `FuzzUMPVarint`, `FuzzUMPStream`, `FuzzProtobufWire`, `FuzzNextRequestPolicy`, `FuzzSabrRedirect`, `FuzzSabrContextUpdate`, `FuzzSabrContextSendingPolicy`, `FuzzSabrError`, `FuzzReloadPlayerResponse`, `FuzzRefreshMaterialValidation`, `FuzzMixedUMPStream`, `FuzzSabrCheckpoint` in `internal/protocol/youtubeump`; `FuzzNormalizeToken` in `internal/youtubepot`
