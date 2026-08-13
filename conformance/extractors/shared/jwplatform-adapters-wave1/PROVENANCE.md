# JW Platform adapter provenance

Reference: `yt-dlp/yt-dlp@aefce1eea4d0b6bab1ec2bd3beff09bff91a39c8`

Synthetic fixtures only. No copyrighted pages, credentials, cookies, or signed playback URLs.

| Adapter | Reference module/class | Selectors / JSON paths / defaults | Go hardening |
| --- | --- | --- | --- |
| Bundesliga | `yt_dlp/extractor/bundesliga.py` / `BundesligaIE` | `vid` query parameter is the JW Platform media id; no page fetch | Strict hosted URL policy; validated 8-character id |
| Business Insider | `yt_dlp/extractor/businessinsider.py` / `BusinessInsiderIE` | Ordered `data-media-id`, `jwplayer_`, inline id, and `jwplatform.com/players/` patterns | HTTPS canonical page; bounded page; subdomain label validation |
| DBTV | `yt_dlp/extractor/dbtv.py` / `DBTVIE` | 11-character ids hand off to YouTube; 8-character ids hand off to JW Platform; no page fetch | Strict hosted URL policy; transparent URL results |
| Hollywood Reporter | `yt_dlp/extractor/hollywoodreporter.py` / `HollywoodReporterIE` | `vlanding-video-card__link` attributes `data-video-showcase-type` and `data-video-showcase-trigger` | Unsupported showcase types never echoed in errors; JW and YouTube branches; synthetic `fixture0001` YouTube id |
| Iltalehti | `yt_dlp/extractor/iltalehti.py` / `IltalehtiIE` | Balanced `window.App` with `js_to_json` transform; `main_media` then `body` jwplayer ids | Playlist bound 128; relaxed JS parsing without execution; comments after trailing commas/between comma and delimiter supported |
| Le Figaro Video Embed | `yt_dlp/extractor/lefigaro.py` / `LeFigaroVideoEmbedIE` | Balanced `__NEXT_DATA__` → `playerData.videoId` | Bounded metadata strings; transparent handoff; HTTPS-only poster |
| Mirror.co.uk | `yt_dlp/extractor/mirrorcouk.py` / `MirrorCoUKIE` | HTML-unescaped `json-placeholder` `videoData.videoId` | `display_id` omitted; JW media id preserved for handoff/re-entry |
| Outside TV | `yt_dlp/extractor/outsidetv.py` / `OutsideTVIE` | Final 8-character play URL segment is the media id; no page fetch | Strict hosted URL policy |
| The Intercept | `yt_dlp/extractor/theintercept.py` / `TheInterceptIE` | Balanced `initialStoreTree` post lookup by slug → `fov_videoid` | Single-pass map iteration; duplicate slug rejection; bare host only; transparent handoff with id/title/timestamp preserved through JW recursion |

All nine adapters share the validated transparent `jwPlatformURLEntry`
helper (defined alongside `jwPlatformURLResult` in
`internal/extractor/kaltura_jw_adapters.go`) so media-id validation and
transparency semantics are checked once.

Fixtures live under `internal/extractor/testdata/jwplatform_adapters_wave1/`.

## Deliberate deviations

- DBTV, Mirror.co.uk, and The Intercept `display_id` — `extractor.Entry`
  does not yet expose a dedicated `display_id` field, so the URL-derived
  display id is folded into `id` when present.
- The Intercept `description` and `comment_count` — not on
  `extractor.Entry`; recording them would require widening the schema.
- Le Figaro `description` — not on `extractor.Entry`; producer title and
  HTTPS poster are preserved.
