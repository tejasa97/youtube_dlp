# YouTube protected-playback continuation

Status: Wave 1 and the caption and bounded WEB format-recovery portions of
Wave 3, including bounded sidecar conversion, are implemented and locally
verified. Wave 2 and broader authenticated clients remain open. Wave 4 now
includes a shared browse/search renderer walker, channel-advertised custom
tabs, channel-local search, broader general and Music search result families,
and authenticated exact-origin WEB browse/search continuations.

This is post-review compatibility work while Gate G3 remains blocked by the
external observations listed in `PHASE_3_EXIT_REVIEW.md`. It does not open
Phase 4 or change that gate decision.

## Wave 1 — PO-token boundary

Implementation status: complete.

- expose native Go `player`, `gvs`, and `subs` provider contexts;
- bound and validate provider count, metadata, token size, expiry, and cache;
- keep provider failures and token material out of public diagnostics;
- propagate cancellation and recover malformed or panicking providers;
- attach player tokens to Innertube integrity dimensions and GVS tokens to
  recovered media URLs and manifests;
- retain the existing no-provider behavior and make provider use explicit.

## Wave 2 — direct SABR/UMP

Implementation status: partial — bounded finite non-live VOD slice with credential-isolated POST transport, multiplexed UMP reconstruction (selected track only), buffered-range progression, transactional `NEXT_REQUEST_POLICY` playback-cookie propagation with per-downloader cancellation-safe backoff, transactional server-driven `SABR_REDIRECT` / `SABR_CONTEXT_UPDATE` / `SABR_CONTEXT_SENDING_POLICY` loops (canonical loop key separate from exact signed POST URL, ≤8 redirects, response-wide sending-policy op budget, bounded active/orphan IDs, sorted StreamerContext field 5/6 marshalling, response-transactional commit with retry-body isolation), `END_OF_TRACK` or strict duration-based completion (media replay/dedup on failed rounds is separate from control commit), crash-safe resumable `.part`/checkpoint persistence for committed segments (required bounded video id; strict single-object JSON; no signed URLs/credentials in checkpoints; fresh extraction URL/PO-token reacquisition on restart; pair sidecar completion is marker-then-publish-then-drop-checkpoint while standalone finals leave only media), safe filesystem publish, PO-token resolution at download time (not in extraction JSON or `playback_cookie`), and product dispatch. Live SABR and full client parity remain unsupported.

## Wave 3 — captions and authenticated clients

Implementation status: caption extraction, translation, protected-token
placement, native sidecar selection/download, bounded CLI listing,
post-download conversion to SRT, ASS, or WebVTT, bounded multi-track embedding,
and authenticated WEB player format recovery are complete. Broader
authenticated Innertube profiles remain pending.

- consume the `subs` PO-token context for protected caption requests;
- add bounded subtitle and automatic-caption extraction;
- add explicit authenticated Innertube profiles without crossing cookie or
  visitor identities between incompatible clients. The implemented WEB slice
  uses exact-origin SID hashes, a redirect-disabled request, and the operation
  cookie jar; anonymous Android/VR recovery remains cookie-isolated.

## Wave 4 — renderer breadth

Implementation status: exact public UCID, pinned Unicode-aware handle, and
bounded legacy `/user` and `/c` alias video/Shorts/streams/playlist plus
home/featured/community/releases/podcasts tabs are implemented. Bounded public
`/membership` tab routes for exact channel, handle, and legacy alias URLs are
implemented for video-only renderer extraction when the supplied session is
already authorized. A shared browse/search renderer walker covers video,
Shorts/reel, playlist, channel, lockup, hashtag, shelf, and Music list
renderers with consistent continuation handling. Dynamically advertised custom
tabs are accepted only when securely bound to the requested channel identity,
including resolved-UCID browseId checks and attributable selected endpoints.
Channel-local search, broader general search results, and broader YouTube Music
section search are implemented; registered Music browse families are consumed by
cookie-isolated `youtube_music_browse` (albums require a canonical Music
playlist identity), while unregistered Music browse prefixes and hashtag
tiles without a registered consumer are omitted so default playlist expansion
cannot fail. Conditional `/search` redirects preserve the validated query.
Authenticated exact-origin WEB browse/search
continuations reuse the SID no-redirect boundary without anonymous fallback or
WEB↔WEB_REMIX identity crossing; incomplete logged-in config fails closed.
Browse continuations rotate visitor data; general search reuses the initial
visitor. Bounded public and opt-in authenticated-WEB
comment slices cover top/new sorting, legacy and modern fields, click-tracked
reply continuations, nested subthreads, bounded retries, pinned duplicate
handling, visitor rotation, exact-origin signed continuations, and explicit
resource limits. Bare channel/handle/legacy-alias upload aggregation is
implemented. Estimated pre-fetch counts before retrieval remain pending.

The finite post-live DVR and opt-in active live-from-start paths are complete:
eligible adaptive tracks use bounded `X-Head-Seqnum`/`sq` reconstruction,
signed-URL refresh, concurrent A/V transfer, and normal merging.

- keep every compatibility claim tied to deterministic success and failure
  evidence in the parity manifest;
- derive attributable synthetic fixtures from the pinned reference.

All waves remain build-time and runtime Python-free. The pinned Python checkout
is a read-only behavioral reference and is never part of the product graph.
