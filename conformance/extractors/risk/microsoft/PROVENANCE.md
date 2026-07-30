# Microsoft public media family risk fixtures

Synthetic API, webpage, and manifest fixtures authored on 2026-07-30 from
`yt_dlp/extractor/microsoftembed.py` at pinned upstream commit
`aefce1eea4d0b6bab1ec2bd3beff09bff91a39c8`. Fixture URLs use the same
attributable hosts the pinned reference exercises (Akamai CMS, Medius event
CDNs, learn.microsoft.com, build.microsoft.com) with synthetic embed IDs,
UUIDs, slugs, captions, and manifest paths so no production or user-derived
data is referenced.

## Supported public family

- `microsoft_embed` exact HTTPS `www.microsoft.com/<locale>/videoplayer/embed/{id}`
  routes via the pinned Akamai video CMS JSON API. ISM, HLS, DASH, and direct
  HTTP(S) source streams are exposed through native product paths; captions
  surface as VTT sidecars and thumbnails retain server-supplied dimensions.
- `microsoft_medius` exact HTTPS `medius.microsoft.com/Embed/video-nc/{uuid}`
  and `Embed/VideoDetails/{uuid}` three-segment routes, plus the pinned
  `Embed/Video?id={uuid}` query-string route. The webpage fetch canonicalizes
  to `Embed/video-nc/{uuid}` regardless of which route matched. ISM manifest
  downloads are exposed through the native ISM pipeline.
- `microsoft_learn_playlist` exact HTTPS
  `learn.microsoft.com/<locale>/(shows|events)/{slug}` playlist routes with
  a lazy reusable EntrySequence against the pinned content browser API.
  `$skip` advances by the number of server url rows (matching the pinned
  behavior). Episodes (`shows`) and sessions (`events`) are emitted as
  transparent URL results with stable deduplication, page and total-entry
  bounds, deterministic ordering, and cancellation safety.
- `microsoft_learn_episode` exact HTTPS
  `learn.microsoft.com/<locale>/shows/{slug}/{id}` routes via the pinned
  `api/video/public/v1/entries/{entryId}` JSON API. ISM, HLS, DASH, and the
  pinned direct HTTP low/medium/high and audioUrl formats are exposed.
- `microsoft_learn_session` exact HTTPS
  `learn.microsoft.com/<locale>/events/{slug}/{id}` routes via pinned
  `externalVideoUrl` discovery; the resolved URL must itself satisfy the
  exact `microsoft_medius` route policy. Transparent reentry preserves the
  pinned Referer.
- `microsoft_build` exact HTTPS
  `build.microsoft.com/<locale>/sessions` and
  `build.microsoft.com/<locale>/sessions/<uuid>` routes via the pinned
  `api-v2.build.microsoft.com/api/session/all/en-US` JSON API. The optional
  `?source=sessions` query is permitted; other query keys, duplicates, and
  malformed encoding are rejected. UUID matches return a single transparent
  reentry into `microsoft_medius` whose onDemand URL must satisfy the exact
  Medius route policy.

## Host attribution

Returned manifests, direct media, captions, and thumbnails are accepted only
when the host matches one of the pinned production host families. Product
tests rewrite requests for those production URLs through an in-memory
RoundTripper; production URL validation has no fixture-mode exception.

| Role | Attributable host families |
| --- | --- |
| Manifest | `prod-video-cms-rt-microsoft-com.akamaized.net`, `mediusimg.event.microsoft.com`, `mediusdownload.event.microsoft.com`, `learn.microsoft.com` |
| Direct media | `prod-video-cms-rt-microsoft-com.akamaized.net`, `mediusdownload.event.microsoft.com`, `learn.microsoft.com` |
| Captions | `mediusimg.event.microsoft.com`, `learn.microsoft.com` |
| Thumbnails | `img-prod-cms-rt-microsoft-com.akamaized.net`, `mediusimg.event.microsoft.com`, `learn.microsoft.com` |

## Known deviations

- Authenticated, DRM-protected, signed-cookie Medius playback, the legacy
  alternative embedded iframes, regional restrictions, Akamai CDN hostname
  rotation, and any host that is not listed above remain out of scope.
- Microsoft Build consumes the pinned single bounded session-catalog response;
  oversized catalogs and invalid or duplicate rows are rejected or skipped
  deterministically.
- All manifest, thumbnail, caption, and media URLs are credential-isolated
  and no-redirect. Operation cookie jars and Authorization headers never
  reach the Microsoft or Medius APIs.
- The pinned Python extractor's per-track non-English ISM
  `language_preference=-10` adjustment is not represented because this Go
  extractor returns one credential-isolated manifest format and native ISM
  track selection occurs later. No per-language ISM preference is claimed.
