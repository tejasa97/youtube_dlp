# YouTube SABR/UMP evidence

Status: partial — bounded finite-VOD slice only. Synthetic fixtures only; no claim of full SABR parity.

## Supported wire subset

- UMP framing with 1–5 byte prefix varints (LuanRT/GoogleVideo `UmpReader.ts`, commit `d2fa40d761034a286cf60ee033653307a1295b0c`).
- Response parts handled: `FORMAT_INITIALIZATION_METADATA` (42), `MEDIA_HEADER` (20), `MEDIA` (21), `MEDIA_END` (22), `NEXT_REQUEST_POLICY` (35), `END_OF_TRACK` (62).
- Multiplexed responses: unselected itag headers are consumed, length-validated, and discarded without writing; selected format initialization metadata is required.
- `NEXT_REQUEST_POLICY`: canonical protobuf validation for `backoff_time_ms` (field 4, max 30s) and embedded `PlaybackCookie` (field 7, max 4096 bytes, known fields 1/2 varints and 7/8 `FormatID`). At most one policy per response; cookie replaces prior state on success; policy without cookie preserves prior cookie. Backoff is applied cancellation-safely before the next POST when the track is not yet complete.
- `END_OF_TRACK` (62): empty payload only, once per response, with no active media headers and after init plus at least one finalized selected segment. Authoritative finite-VOD completion even below declared duration. Duration-based completion remains the fallback when `END_OF_TRACK` is absent.
- Fail-closed on all other part types, including `SABR_REDIRECT`, `SABR_ERROR`, `RELOAD_PLAYER_RESPONSE`, live metadata, context update/sending policy, and stream protection.
- `VideoPlaybackAbrRequest` protobuf fields: `client_abr_state` (player time, enabled tracks, DRC, audio track id), `selected_format_ids`, `buffered_ranges`, `video_playback_ustreamer_config`, preferred audio/video format ids, and `streamer_context` (`client_info`, `po_token`, and `playback_cookie` field 3 once the server supplies a validated cookie). Visitor data is not a playback cookie and is used only for PO-token binding at download time.
- POST to `serverAbrStreamingUrl` with preserved signed query bytes and `rn` progression, `Content-Type: application/x-protobuf`, `Accept: application/vnd.yt-ump`.
- Credential-isolated transport: no `Cookie` / `Authorization` / `Proxy-Authorization`, no redirect following, no response cookie persistence (`network.Client.DoWithoutCredentialsNoRedirect`).
- Caller `Config.Headers` may supply ordinary safe headers but cannot override or duplicate protected control headers (`Content-Type`, `Accept`, `Accept-Encoding`, `User-Agent`, `Host`, hop-by-hop headers, credentials, or `X-Goog-Visitor-Id`).
- SABR format metadata keeps `url` empty; download dispatch uses `_youtube_sabr` / `_youtube_sabr_server_url` markers. Format selection binds to one player candidate inventory without cross-player merging.
- WEB SABR transport identity requires attributable `ytcfg` evidence: `INNERTUBE_CONTEXT_CLIENT_NAME`, matching `clientName`, `clientVersion`, and `userAgent`.
- Completion requires init segment, at least one selected media segment, and either cumulative selected media duration ≥ declared finite `duration_sec` or a valid `END_OF_TRACK`. Active media headers at response EOF are rejected as truncated state. Failed or retried responses do not commit cookie, backoff, or end markers. Post-publish `completed` events are best-effort and do not fail an already published artifact.

## Provenance

| Source | Commit / URL | Use |
|--------|----------------|-----|
| LuanRT/GoogleVideo | `d2fa40d761034a286cf60ee033653307a1295b0c` | UMP varints, part IDs, protobuf field numbers, `BufferedRange`, `NextRequestPolicy`, `PlaybackCookie`, `StreamerContext.playback_cookie` |
| davidzeng0/innertube `googlevideo/ump.md` | main @ 2026-07-24 | UMP part semantics documentation |
| ColeSpringer/WaxTap v2.0.1 | `5d4b07dbfad5c2831c35ea7b95006b576e08f694` | Request marshaling shape cross-check (not a dependency) |
| yt-dlp reference | `aefce1eea4d0b6bab1ec2bd3beff09bff91a39c8` | SABR-only detection only; no direct transport |

Synthetic fixtures: `conformance/extractors/youtube/sabr-only-watch.html` and deterministic UMP bytes in `internal/protocol/youtubeump/*_test.go`.

## Measured bounds

- `MaxRoundBytes` = 64 MiB per SABR response
- `MaxParts` = 10,000 per response
- `MaxActiveHeaders` = 8 concurrent in-flight media headers per response
- `MaxMediaBytes` = 8 GiB per track (shared direct downloader ceiling)
- `MaxRounds` = 64 POST rounds for finite VOD
- `MaxPlaybackCookieBytes` = 4096 per validated cookie
- `MaxPolicyBackoffMs` = 30,000 per `NEXT_REQUEST_POLICY`
- Redirect responses and redirect directives are rejected (fail-closed)

## Remaining deviations

- Live/post-live, resume, server-driven redirect handling, PO-token refresh, and full client parity remain unsupported.
- No claim of parity with WaxTap/GoogleVideo full clients or yt-dlp transport.
- Authenticated WEB SABR-only pages outside the synthetic fixture are not evidenced here.
- PO tokens are resolved at download time through the public provider boundary; they are not exported in extraction JSON.

## Tests

- `go test ./internal/protocol/youtubeump ./internal/extractor ./internal/format ./internal/network ./pkg/ytdlp`
- Fuzz: `FuzzUMPVarint`, `FuzzUMPStream`, `FuzzProtobufWire`, `FuzzNextRequestPolicy`, `FuzzMixedUMPStream` in `internal/protocol/youtubeump`
