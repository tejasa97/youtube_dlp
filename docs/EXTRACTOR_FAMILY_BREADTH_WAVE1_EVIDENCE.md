# Extractor family breadth wave 1

Baseline: `yt-dlp/yt-dlp@aefce1eea4d0b6bab1ec2bd3beff09bff91a39c8`  
Go base: `origin/main` at `1cf4e80524d2f7dfed1db64cd3436dca2fd0e221`

This wave adds reusable extractor families that do not overlap the existing
Brightcove, Kaltura, JWPlatform/JWPlayer, Wistia, or SproutVideo backends.

## Frozen scope

| Family | Key | Kind | Counted | Exact URL shape |
|--------|-----|------|---------|-----------------|
| Cloudflare Stream | `cloudflarestream` | backend | yes | `cloudflarestream.com` / `videodelivery.net` / `bytehighway.net` / `customer-*.cloudflarestream.com` watch, iframe, embed JS, manifest |
| Cloudflare Stream | `hytale` | adapter | yes | `hytale.com` / `www.hytale.com` `/news/YYYY/MM/<slug>` |
| Arc Publishing | `arcpublishing` | backend | yes | opaque `arcpublishing:<org>:<uuid>` |
| Arc Publishing | `washingtonpost` | adapter | yes | `washingtonpost.com` `/video/` or `/posttv/` UUID paths |
| Arc Publishing | `adn` | adapter | yes | `adn.com` / `www.adn.com` pages with POWA `data-org`/`data-uuid` |
| Arc Publishing | `bostonglobe` | adapter | yes | `bostonglobe.com` / `www.bostonglobe.com` POWA embeds |
| Arc Publishing | `gray` | adapter | yes | `wabi.tv` / `www.wabi.tv` `/video/` paths with UUID |
| Arc Publishing | `clickondetroit` | adapter | yes | `clickondetroit.com` / `www.clickondetroit.com` POWA embeds |
| Anvato | `anvato` | backend | yes | opaque `anvato:<access_key_or_mcp>:<digits>` |
| Anvato | `fox9` | adapter | yes | `fox9.com` / `www.fox9.com` `/video/<digits>` |
| Anvato | `fox9_news` | adapter | yes | `fox9.com` / `www.fox9.com` `/news/<slug>` |
| ThePlatform | `theplatform` | backend | yes | `link.theplatform.com/s/...` and `player.theplatform.com/p/...` |
| ThePlatform | `theplatform_feed` | backend | yes | `feed.theplatform.com/f/<provider>/<feed>?byGuid=` or `byId=` |
| ThePlatform | `weathercom` | adapter | yes | `weather.com` `.../video/<slug>` |
| ThePlatform | `nbcolympics` | adapter | yes | `vplayer.nbcolympics.com/p/...` → ThePlatform player |

**Counted keys: 15.** Aliases (`www` hosts), opaque-scheme redirects that are not
independent extractors, and generic embed discovery are not counted.

## Deliberate hardening vs pinned reference

- HTTPS-first canonical URLs; reject userinfo, explicit ports, IP literals,
  NUL/encoded separators, and credential-bearing media URLs.
- Bounded bodies, strings, arrays, formats, captions, chapters, pages, and
  redirects. Context cancellation terminates every family before network work.
- Provider errors map to categorized extractor errors without returning bodies,
  tokens, cookies, signed queries, or arbitrary server text.
- Anvato ships only the access-key secrets required by counted adapters, not the
  full reference `_ANVACK_TABLE`. Auth material never appears in errors or info.
- ThePlatform Adobe Pass / DRM success paths are unsupported (`ErrAuthentication`
  or `ErrUnsupported`). SMIL geo/unavailable markers fail closed.
- Adapters declare exact hosts/paths and emit `URLResult` handoffs; they do not
  steal Brightcove/Kaltura/JW/Wistia/SproutVideo URLs.

## Provenance

| Family | Reference | Go entry points |
|--------|-----------|-----------------|
| Cloudflare Stream | `yt_dlp/extractor/cloudflarestream.py` `CloudflareStreamIE` | `cloudflarestream.go` |
| Hytale | `yt_dlp/extractor/hytale.py` `HytaleIE` | `hytale.go` |
| Arc Publishing | `yt_dlp/extractor/arcpublishing.py` `ArcPublishingIE` | `arcpublishing.go` |
| Washington Post | `yt_dlp/extractor/washingtonpost.py` `WashingtonPostIE` | `washingtonpost.go` |
| Anvato | `yt_dlp/extractor/anvato.py` `AnvatoIE` | `anvato.go` |
| FOX9 | `yt_dlp/extractor/fox9.py` `FOX9IE` / `FOX9NewsIE` | `fox9.go` |
| ThePlatform | `yt_dlp/extractor/theplatform.py` `ThePlatformIE` / `ThePlatformFeedIE` | `theplatform.go` |
| Weather Channel | `yt_dlp/extractor/theweatherchannel.py` `TheWeatherChannelIE` | `weathercom.go` |
| NBC Olympics player | `yt_dlp/extractor/nbc.py` Olympics player handoff | `nbcolympics.go` |
