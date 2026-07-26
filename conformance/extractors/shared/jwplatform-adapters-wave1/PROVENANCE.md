# JW Platform adapters wave 1 provenance

Reference: `yt-dlp/yt-dlp@aefce1eea4d0b6bab1ec2bd3beff09bff91a39c8`

Synthetic fixtures only. No copyrighted pages, credentials, cookies, or signed playback URLs.

| Adapter | Reference module/class | Selectors / JSON paths / defaults | Go hardening |
| --- | --- | --- | --- |
| Bundesliga | `yt_dlp/extractor/bundesliga.py` / `BundesligaIE` | `vid` query parameter is the JW Platform media id; no page fetch | Strict hosted URL policy; validated 8-character id |
| Business Insider | `yt_dlp/extractor/businessinsider.py` / `BusinessInsiderIE` | Ordered `data-media-id`, `jwplayer_`, inline id, and `jwplatform.com/players/` patterns | HTTPS canonical page; bounded page; subdomain label validation |
| DBTV | `yt_dlp/extractor/dbtv.py` / `DBTVIE` | 11-character ids hand off to YouTube; 8-character ids hand off to JW Platform; no page fetch | Strict hosted URL policy; transparent URL results |
| Hollywood Reporter | `yt_dlp/extractor/hollywoodreporter.py` / `HollywoodReporterIE` | `vlanding-video-card__link` attributes `data-video-showcase-type` and `data-video-showcase-trigger` | Unsupported showcase types never echoed in errors; JW and YouTube branches |
| Iltalehti | `yt_dlp/extractor/iltalehti.py` / `IltalehtiIE` | Balanced `window.App` JSON; `main_media` then `body` jwplayer ids in upstream order | Playlist bound `128`; balanced JSON extraction; reusable entry iterator |
| Le Figaro Video Embed | `yt_dlp/extractor/lefigaro.py` / `LeFigaroVideoEmbedIE` | Balanced `__NEXT_DATA__` → `playerData.videoId` | Bounded metadata strings; HTTPS poster validation |
| Mirror.co.uk | `yt_dlp/extractor/mirrorcouk.py` / `MirrorCoUKIE` | HTML-unescaped `json-placeholder` `videoData.videoId` | Balanced JSON extraction instead of nested-regex matching |
| Outside TV | `yt_dlp/extractor/outsidetv.py` / `OutsideTVIE` | Final 8-character play URL segment is the media id; no page fetch | Strict hosted URL policy |
| The Intercept | `yt_dlp/extractor/theintercept.py` / `TheInterceptIE` | Balanced `initialStoreTree` post lookup by slug → `fov_videoid` | Bare `theintercept.com` host only; bounded title; ISO8601 timestamp |

Fixtures live under `internal/extractor/testdata/jwplatform_adapters_wave1/`.
