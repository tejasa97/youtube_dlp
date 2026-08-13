# YouTube Innertube client breadth evidence

Pinned reference: yt-dlp `aefce1eea4d0b6bab1ec2bd3beff09bff91a39c8`
(`yt_dlp/extractor/youtube/_base.py:INNERTUBE_CLIENTS`,
`_video.py:_DEFAULT_CLIENTS` / `_DEFAULT_AUTHED_CLIENTS` /
`_DEFAULT_PREMIUM_CLIENTS`).

SABR references in this document describe isolation invariants for the retained
experimental implementation. They do not make SABR a parity target or supported product capability.

## Profiles

Anonymous format recovery (cookie-isolated POST, deterministic order;
broader than `_DEFAULT_CLIENTS` android_vr/web_safari alone — includes the
historical android/ios/mweb recovery clients already pinned in this port):

1. `android` — ANDROID / 3 / 21.26.364
2. `android_vr` — ANDROID_VR / 28 / 1.65.10
3. `web_safari` — WEB / 1 / 2.20260708.00.00 (Safari UA)
4. `ios` — IOS / 5 / 21.26.4
5. `mweb` — MWEB / 2 / 2.20260708.05.00

Authenticated recovery (exact `www.youtube.com` origin + SID hash, no
anonymous downgrade, first successful format-bearing candidate wins — no
cross-player format/SABR merge):

1. Webpage WEB (ytcfg-bound) — **deliberate hardening:** prefer the already
   fetched account-bound WEB `/player` before additional Innertube clients
   when logged-in ytcfg + SID cookies are valid.
2. Then, matching reference defaults exactly:
   - Premium (`_DEFAULT_PREMIUM_CLIENTS`): `tv_downgraded`, `web_creator`
   - Authenticated non-Premium (`_DEFAULT_AUTHED_CLIENTS`): `tv_downgraded`,
     `web_safari`
   - `web_creator` is appended on the non-Premium path **only** when an
     attributable age-gate / age-verification playability signal is present
     (`desktopLegacyAgeGateReason` or status/reason markers matching
     yt-dlp `_is_agegated`). Ordinary authenticated no-format /
     `LOGIN_REQUIRED` responses do **not** add `web_creator`.
3. Exact pinned identities:
   - `tv_downgraded` — TVHTML5 / 7 / 5.20260707 (`REQUIRE_AUTH`)
   - `web_safari` (auth path) — WEB / 1 / 2.20260708.00.00 with SID boundary
     (**deliberate hardening** vs reference cookie-only `SUPPORTS_COOKIES`)
   - `web_creator` — WEB_CREATOR / 62 / 1.20260708.06.00 (`REQUIRE_AUTH`
     only; **not** `REQUIRE_PREMIUM`)

### GVS PO-token policy (web_creator / web_safari)

Pinned `_base.py`: `web_creator` and WEB-family GVS policies set
`required=True` with `not_required_for_premium=True`. Premium only changes
whether a GVS PO token is required — it does not gate client eligibility.
Authenticated recovery enforces that policy fail-closed: when a GVS token is
required and missing/rejected, that candidate is discarded with an explicit
`GVS PO token required for <client>` error (formats are not silently stripped
to itag-18 on the auth path).

### Origin / API host / SID

- API host remains `https://www.youtube.com/youtubei/v1/player` for all video
  recovery clients in this port.
- Origin / `X-Origin` / SAPISIDHASH audience are fixed to
  `https://www.youtube.com` (**deliberate hardening**): reference may download
  `web_creator` ytcfg from `studio.youtube.com`, but this port does not switch
  the SID/API origin to Studio for video format recovery.
- Music `WEB_REMIX` / `music.youtube.com` origins remain rejected from video
  recovery.

`WEB_REMIX` remains Music-only and is rejected from video recovery profiles.

## Isolation / rejection

- No cookie copying across incompatible profiles
- Authenticated path never falls back to anonymous Android/iOS/mweb
- SABR inventories stay first-successful-candidate (no cross-player merge);
  selected-client SABR metadata binds that client's name/id/version/UA
- Client attempts capped at `MaxYouTubeClientAttempts` (8)
- Cancellation between attempts is preserved
- Failure diagnostics redact credentials and signed URLs

Adversarial coverage: `internal/extractor/youtube_client_test.go`.
