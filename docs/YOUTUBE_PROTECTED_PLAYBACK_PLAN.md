# YouTube protected-playback continuation

Status: Wave 1 and the caption portion of Wave 3 are implemented and locally
verified. Wave 2 and authenticated clients remain open. Wave 4 now includes a
bounded public video-search slice; its other renderer breadth remains open.

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
placement, and native sidecar selection/download are complete. Authenticated
Innertube profiles remain pending.

- consume the `subs` PO-token context for protected caption requests;
- add bounded subtitle and automatic-caption extraction;
- add explicit authenticated Innertube profiles without crossing cookie or
  visitor identities between incompatible clients.

## Wave 4 — renderer breadth

Implementation status: exact public UCID video/Shorts/streams tabs and bounded
public video search are implemented. Handle/home/community/playlist/release
tabs, broader search results, comments, and live-from-start remain pending.

- expand channel, tab, search, comments, and live-from-start renderers;
- derive attributable synthetic fixtures from the pinned reference;
- keep every compatibility claim tied to deterministic success and failure
  evidence in the parity manifest.

All waves remain build-time and runtime Python-free. The pinned Python checkout
is a read-only behavioral reference and is never part of the product graph.
