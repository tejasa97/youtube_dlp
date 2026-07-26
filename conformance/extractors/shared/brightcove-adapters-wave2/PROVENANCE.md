# Brightcove adapters wave 2 provenance

Reference: `yt-dlp/yt-dlp@aefce1eea4d0b6bab1ec2bd3beff09bff91a39c8`

Synthetic fixtures only. No copyrighted pages, credentials, cookies, or signed playback URLs.

| Adapter | Reference module/class | Selectors / JSON paths / defaults | Go hardening |
| --- | --- | --- | --- |
| Formula1 | `yt_dlp/extractor/formula1.py` / `Formula1IE` | Final numeric path segment is Brightcove video id; account `6057949432001`; player `S1WMrhjlh`; no page fetch | Strict hosted URL policy; HTTPS Brightcove handoff |
| EuropeanTour | `yt_dlp/extractor/europeantour.py` / `EuropeanTourIE` | `brightcove-player video-id` + `"ACCOUNT_ID"`; default account `5136026580001`; default player | Bounded page; validated account/video ids |
| MaoriTV | `yt_dlp/extractor/maoritv.py` / `MaoriTVIE` | `data-main-video-id`; account `1614493167001`; player `HJlhIQhQf` | Bounded page; digit-only video id |
| TheStar | `yt_dlp/extractor/thestar.py` / `TheStarIE` | `mainartBrightcoveVideoId`; account `794267642001`; default player | Bounded page; digit-only video id |
| TheSun | `yt_dlp/extractor/thesun.py` / `TheSunIE` | Ordered `<video data-video-id-pending>`; `data-account` default `5067014667001`; `og:title` playlist title | Rejects malformed ids; playlist bound `256`; independent iterator reuse |
| Wimbledon | `yt_dlp/extractor/wimbledon.py` / `WimbledonIE` | Metadata API `wim_v1_<id>_en`; account `3506358525001`; title + duration overlay | Bounded JSON; credential-isolated API transport; description omitted (Entry contract) |
| USAToday | `yt_dlp/extractor/usatoday.py` / `USATodayIE` | `ajax=true` page fetch; `ui-video-data` JSON; `brightcoveaccount` default `29906170001`; `brightcoveid` fallback `brightcove_id` | Bracket-balanced JSON extraction; thumbnail HTTPS validation; description omitted (Entry contract) |
| Sky News AU | `yt_dlp/extractor/skynewsau.py` / `SkyNewsAUIE` | `embedcode="account-video"`; News API `content.api.news`; caption + `date.created` | API key never echoed in errors; credential-isolated API transport |

Fixtures live under `internal/extractor/testdata/brightcove_adapters_wave2/`.
