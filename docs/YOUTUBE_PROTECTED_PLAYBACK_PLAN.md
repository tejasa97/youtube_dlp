# YouTube protected-playback continuation

Status: Wave 1 and the caption and bounded WEB format-recovery portions of
Wave 3, including bounded sidecar conversion, are implemented and locally
verified. Wave 2 and broader authenticated clients remain open. Wave 4 now
includes a bounded public video-search slice;
bounded public YouTube Music search, playlist tabs, and an opt-in public
comments slice; its other renderer breadth remains open.

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

Implementation status: pending.

- implement a bounded UMP parser and deterministic byte-stream fixtures;
- resolve SABR media, initialization, and metadata parts without Python;
- support cancellation, retries, range validation, and categorized failures;
- integrate the direct path ahead of native-client URL recovery where safe.

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
and playable YouTube Music search plus bounded, opt-in
public and authenticated-WEB comment slices cover
top/new sorting, legacy and modern fields, click-tracked reply continuations,
nested subthreads, bounded retries, pinned duplicate handling, visitor
rotation, exact-origin signed continuations, and explicit resource limits.
Estimated pre-fetch counts, bare channel routes, membership/custom tabs, and
non-playable search result breadth remain pending.

The finite post-live DVR and opt-in active live-from-start paths are complete:
eligible adaptive tracks use bounded `X-Head-Seqnum`/`sq` reconstruction,
signed-URL refresh, concurrent A/V transfer, and normal merging.

- expand remaining channel, tab, search, and comments renderers;
- derive attributable synthetic fixtures from the pinned reference;
- keep every compatibility claim tied to deterministic success and failure
  evidence in the parity manifest.

All waves remain build-time and runtime Python-free. The pinned Python checkout
is a read-only behavioral reference and is never part of the product graph.
