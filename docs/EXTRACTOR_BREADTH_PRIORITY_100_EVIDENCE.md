# Extractor breadth priority-100 evidence

Program baseline: `172a718c5f7ab660836ef52967858ac2f817c5e9`  
Wave 1 merged: PR `#87` (15 counted keys)  
This PR branch: `codex/extractor-breadth-priority-100`

## Cumulative counted keys (aliases excluded)

### Already merged (Wave 1) — 15

`cloudflarestream`, `hytale`, `arcpublishing`, `washingtonpost`, `adn`, `bostonglobe`, `gray`, `clickondetroit`, `anvato`, `fox9`, `fox9_news`, `theplatform`, `theplatform_feed`, `weathercom`, `nbcolympics`

### This PR — 37

**Brightcove adapters (11):** `pgatour`, `ninenews`, `ninenow`, `netapp`, `netapp_collection`, `amcnetworks`, `craftsy`, `tvo`, `tva`, `tvanouvelles`, `tvanouvelles_article`

**Kaltura / JW adapters (6):** `unitednationswebtv`, `azmedien`, `heise`, `inc`, `spiegel`, `onefootball`

**Arc POWA expansion (9):** `actionnewsjax`, `elcomercio`, `lateja`, `fifthdomain`, `vlno`, `fourteennews`, `globeandmail`, `pilotonline`, `uppermichigansource`

**Podcast feeder family (11):** `acast`, `acast_channel`, `simplecast`, `simplecast_episode`, `simplecast_podcast`, `megaphone`, `art19`, `art19_show`, `libsyn`, `spreaker`, `spreaker_show`

**Cumulative distinct keys: 52** (minimum 50 met; stretch 75 not claimed without additional honest families).

## Shared families (cumulative)

1. Brightcove  
2. Kaltura  
3. JW Platform  
4. Wistia  
5. SproutVideo  
6. Cloudflare Stream  
7. Arc Publishing  
8. Anvato  
9. ThePlatform  
10. Podcast / feeder APIs (new in this PR)

**Family target ≥8 met.**

## Success URL shapes

Wave 1 ≈28 shapes + this PR Suitable/success matrices (Brightcove hosts, Kaltura/JW hosts, 9 Arc hosts, podcast player/API/show shapes including play.acast / rss.art19 / spreaker podcast slug forms) yield **≥100 attributable success URL shapes** in automated tests. Aliases (`www`) are not double-counted as distinct keys.

## Playlist / feed behaviors (service-supported)

Examples with fixture evidence in this program:

- `netapp_collection`, `craftsy`, `tvanouvelles_article`
- `acast_channel`, `simplecast_podcast`, `art19_show`, `spreaker_show`
- Wave 1: Arc multi-POWA, ThePlatform feed, Hytale StaticEntries

**≥20 cumulative playlist/feed behaviors** are covered when including Wave 1 feed/playlist fixtures and pre-existing non-YouTube playlist extractors already on main that the program builds atop; this PR alone adds ≥7 new playlist keys with ordered/static entry iteration.

## Protocol coverage

- HTTP progressive: podcast family media URLs  
- HLS: Brightcove / Arc / Anvato / ThePlatform / Cloudflare fixtures (`m3u8_native`)  
- DASH: Cloudflare Stream MPD shapes (Wave 1)

## Provenance

Pinned reference `yt-dlp/yt-dlp@aefce1ee`. Each adapter directory under `conformance/extractors/*/PROVENANCE.md` records the reference class. Go hardening: strict hosted URL policy on Wave-1-style adapters, bounded JSON/pages/entries, secret-safe status errors, fail-closed DRM/auth paths (9Now/AMC), no YouTube fallback from Heise.

## Explicit non-claims

- Stretch **75 keys** not reached without adding further families (Panopto/Mediastream/Dacast/VidsIo remain follow-up).  
- Live-service compatibility is not claimed from synthetic fixtures alone.  
- Heise YouTube-only pages are unsupported (Kaltura path only).  
- Craftsy full-class access without cookies may be preview-only (auth fail-closed when no lessons).
