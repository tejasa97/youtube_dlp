# Extractor breadth priority-100 — remaining-scope ledger

Frozen before bulk implementation on branch `codex/extractor-breadth-priority-100`.

**Program baseline:** `172a718c5f7ab660836ef52967858ac2f817c5e9`  
**Current main:** `848f96492e9814cd7d9b2ee9c3e911342b25e38e` (includes PR #87 Wave 1)  
**Reference pin:** `yt-dlp/yt-dlp@aefce1eea4d0b6bab1ec2bd3beff09bff91a39c8`

## Attributable new keys already merged since baseline

Wave 1 (`#87`) — **15 counted keys** (aliases / generic embeds excluded):

| Family | Keys |
|--------|------|
| Cloudflare Stream | `cloudflarestream`, `hytale` |
| Arc Publishing | `arcpublishing`, `washingtonpost`, `adn`, `bostonglobe`, `gray`, `clickondetroit` |
| Anvato | `anvato`, `fox9`, `fox9_news` |
| ThePlatform | `theplatform`, `theplatform_feed`, `weathercom`, `nbcolympics` |

No other non-YouTube extractor *keys* were added between baseline and current main (Bluesky/Flickr/Imgur and shared Brightcove/Kaltura/JW/Wistia/SproutVideo backends were already present at baseline).

**Cumulative counted keys entering this PR: 15.**  
**Gap to minimum 50: ≥35.** Stretch 75: ≥60.

## Cumulative reusable families (real shared backends)

Already present (count toward ≥8):

1. Brightcove  
2. Kaltura  
3. JW Platform  
4. Wistia  
5. SproutVideo  
6. Cloudflare Stream (Wave 1)  
7. Arc Publishing (Wave 1)  
8. Anvato (Wave 1)  
9. ThePlatform (Wave 1)

**Family target ≥8 is already met.** This PR adds a 10th: **Podcast / feeder APIs** (shared episode/show normalization used by Acast/Simplecast/Megaphone/Art19/Libsyn/Spreaker).

## Wave-1 attributable success URL shapes (approximate inventory)

Conservative distinct success shapes already evidenced in Wave 1 tests/fixtures: **≈28**  
(CF hex + JWT + embed hosts; Arc `arcpublishing:` + WaPo UUID + 4 POWA hosts; Anvato scheme + FOX9 video/news; ThePlatform link/player/feed + WeatherCom + NBC Olympics).

**Gap to ≥100 shapes: ≈72+** to be covered by this PR’s Suitable/success matrices.

## Playlist / feed behaviors

Wave 1 contributed limited playlist evidence (Hytale StaticEntries, Arc multi-POWA, ThePlatform feed).  
This PR must add lazy/ordered playlist or feed behaviors on podcast channels/shows, NetApp collections, TVA Nouvelles articles, and Simplecast/Spreaker/Art19 shows toward **≥20 cumulative** where services support them.

## Frozen remaining scope (this PR)

### Batch A — Brightcove exact-host adapters

| Key | Reference | Host / path | Media | Playlist |
|-----|-----------|-------------|-------|----------|
| `pgatour` | `pgatour.py` | `pgatour.com/video/...` | BC player URLResult | no |
| `ninenews` | `ninenews.py` | `9news.com.au/...` | `__INITIAL_STATE__` → BC | no |
| `ninenow` | `ninenow.py` | `9now.com.au/...` | page JSON → BC | no |
| `netapp` | `netapp.py` NetAppVideoIE | `media.netapp.com/video-detail/<uuid>` | API → BC | no |
| `netapp_collection` | NetAppCollectionIE | `media.netapp.com/collection/<uuid>` | API → BC entries | yes (static/lazy bounded) |
| `amcnetworks` | `amcnetworks.py` | amc/bbcamerica/ifc/wetv/sundancetv shows/movies | page → BC | no |
| `craftsy` | `craftsy.py` | `craftsy.com/class/<slug>` | class API → BC lessons | yes |
| `tvo` | `tvo.py` | `tvo.org/video/...` | page → BC | no |
| `tva` | `tva.py` | `tvaplus.ca/...-<id>` | page → BC | no |
| `tvanouvelles` | `tvanouvelles.py` | `tvanouvelles.ca/videos/<id>` | direct BC | no |
| `tvanouvelles_article` | TVANouvellesArticleIE | `tvanouvelles.ca/...` (non-video) | data-video-id playlist | yes |

### Batch B — Kaltura + JW adapters

| Key | Reference | Notes |
|-----|-----------|-------|
| `unitednationswebtv` | `unitednations.py` | webtv.un.org → `kaltura:` |
| `azmedien` | `azmedien.py` | telezueri/telebaern/telem1/tvo-online → kaltura (fragment `#video=` allowed only as `video=<id>`) |
| `heise` | `heise.py` | heise.de → kaltura (or YT excluded; kaltura path only) |
| `inc` | `inc.py` | inc.com → kaltura |
| `spiegel` | `spiegel.py` | spiegel.de + manager-magazin.de → `jwplatform:` |
| `onefootball` | `onefootball.py` | onefootball.com → JW manifest/media |

### Batch C — Arc Publishing POWA host expansion

Exact-host POWA adapters (same `arcPowaSite` pattern; org from reference tests):

| Key | Host | Org |
|-----|------|-----|
| `actionnewsjax` | actionnewsjax.com | cmg |
| `elcomercio` | elcomercio.pe | elcomercio |
| `lateja` | lateja.cr | gruponacion |
| `fifthdomain` | fifthdomain.com | mco |
| `vlno` | vl.no | mentormedier |
| `fourteennews` | 14news.com | raycom |
| `globeandmail` | theglobeandmail.com | tgam |
| `pilotonline` | pilotonline.com | tronc |
| `uppermichigansource` | uppermichigansource.com | gray |

### Batch D — Podcast / feeder family (new shared backend)

Shared helpers for HTTPS media URL policy, episode metadata bounds, and show playlist iteration.

| Key | Reference | Playlist |
|-----|-----------|----------|
| `acast` | ACastIE | no |
| `acast_channel` | ACastChannelIE | yes |
| `simplecast` | SimplecastIE | no |
| `simplecast_episode` | SimplecastEpisodeIE | no |
| `simplecast_podcast` | SimplecastPodcastIE | yes |
| `megaphone` | MegaphoneIE | no |
| `art19` | Art19IE | no |
| `art19_show` | Art19ShowIE | yes |
| `libsyn` | LibsynIE | no |
| `spreaker` | SpreakerIE | no |
| `spreaker_show` | SpreakerShowIE | yes |

### Batch E — Completed expansions (reference-backed)

| Key | Family | Playlist |
|-----|--------|----------|
| `nowness` | NOWNESS story → Brightcove/Vimeo | no |
| `nowness_playlist` | NOWNESS playlist API | yes (lazy) |
| `nowness_series` | NOWNESS series API | yes (lazy) |
| `dacast` | Dacast VOD HLS | no |
| `dacast_playlist` | Dacast playlist → VOD URLResults | yes (lazy) |

### Batch F — Still deferred (if time / evidence supports; no dishonest counting)

| Key | Family | Notes |
|-----|--------|-------|
| `vidsio` | SproutVideo | VidsIoIE host adapter |
| `panopto` / `panopto_list` | Panopto | new shared family if parsers stay bounded |
| `mediastream` | Mediastream | exact embed hosts only |
| Additional Anvato/ThePlatform/NBC adapters | existing | only with reference + fixtures |

## Explicit exclusions

- YouTube / youtubeump / youtubepot files  
- Hostname aliases counted as distinct keys (`www`, mobile, embed-only aliases)  
- Routing-only stubs without success/re-entry evidence  
- DRM / login-gated Brightcove paths advertised as success  
- Arbitrary-domain generic podcast RSS without exact host policy  
- Concurrent README / SUPPORTED_SITES / parity_manifest edits until code stable  

## Count plan (honest)

| Metric | Entering (wave 1) | This PR | Cumulative (automated) |
|--------|-------------------|---------|------------------------|
| Distinct keys | 15 | +37 podcast/Arc/BC/JW +5 nowness/dacast | ≥50 (stretch 75 not claimed) |
| Success URL shapes | enumerated in inventory | enumerated in inventory | **≥100** via `TestBreadthPriority100AuditableInventory` |
| Shared families | 9 | +podcast, nowness, dacast | ≥8 (met) |
| Playlist/feed behaviors | wave-1 Arc/Hytale + this PR lazy playlists | see inventory playlist list | **≥20** via same test |

Authoritative inventories are the automated tests, not this table.
