# YouTube Innertube client breadth evidence

Pinned reference: yt-dlp `aefce1eea4d0b6bab1ec2bd3beff09bff91a39c8`
(`yt_dlp/extractor/youtube/_base.py:INNERTUBE_CLIENTS`,
`_video.py:_DEFAULT_CLIENTS` / `_DEFAULT_AUTHED_CLIENTS` /
`_DEFAULT_PREMIUM_CLIENTS`).

## Profiles

Anonymous format recovery (cookie-isolated POST, deterministic order):

1. `android` — ANDROID / 3 / 21.26.364
2. `android_vr` — ANDROID_VR / 28 / 1.65.10
3. `web_safari` — WEB / 1 / 2.20260708.00.00 (Safari UA)
4. `ios` — IOS / 5 / 21.26.4
5. `mweb` — MWEB / 2 / 2.20260708.05.00

Authenticated recovery (exact-origin SID, no anonymous downgrade):

1. Webpage WEB (ytcfg-bound)
2. `tv_downgraded` — TVHTML5 / 7 / 5.20260707
3. `web_creator` — WEB_CREATOR / 62 / 1.20260708.06.00 only when an attributable
   Premium logo/tooltip signal is present in `ytInitialData`

`WEB_REMIX` remains Music-only and is rejected from video recovery profiles.

## Isolation / rejection

- No cookie copying across incompatible profiles
- Authenticated path never falls back to anonymous Android/iOS/mweb
- SABR inventories stay first-successful-candidate (no cross-player merge)
- Client attempts capped at `MaxYouTubeClientAttempts` (8)
- Cancellation between attempts is preserved
- Failure diagnostics redact credentials and signed URLs

Adversarial coverage: `internal/extractor/youtube_client_test.go`.
