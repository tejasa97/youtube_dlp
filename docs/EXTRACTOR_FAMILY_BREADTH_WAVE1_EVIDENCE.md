# Extractor family breadth wave 1

Baseline: `yt-dlp/yt-dlp@aefce1eea4d0b6bab1ec2bd3beff09bff91a39c8`
Go base: `origin/main` at `1cf4e80524d2f7dfed1db64cd3436dca2fd0e221`

This wave adds reusable extractor families that do not overlap the existing
Brightcove, Kaltura, JWPlatform/JWPlayer, Wistia, or SproutVideo backends.

## Frozen scope

| Family | Key | Kind | Counted | Exact URL shape | Success evidence |
|--------|-----|------|---------|-----------------|------------------|
| Cloudflare Stream | `cloudflarestream` | backend | yes | CF Stream / videodelivery / bytehighway / customer-* | hex + signed JWT fixture extraction |
| Cloudflare Stream | `hytale` | adapter | yes | `hytale.com/news/YYYY/MM/<slug>` | page → StaticEntries → CF Stream re-entry formats |
| Arc Publishing | `arcpublishing` | backend | yes | `arcpublishing:<org>:<uuid>` | ANS `findByUuid` fixture formats |
| Arc Publishing | `washingtonpost` | adapter | yes | WaPo `/video|posttv/` UUID | URLResult → Arc re-entry formats |
| Arc Publishing | `adn` | adapter | yes | `adn.com` video-path POWA | POWA fixture → Arc re-entry |
| Arc Publishing | `bostonglobe` | adapter | yes | `bostonglobe.com` video-path POWA | POWA fixture → Arc re-entry |
| Arc Publishing | `gray` | adapter | yes | `wabi.tv` video-path POWA | POWA fixture → Arc re-entry |
| Arc Publishing | `clickondetroit` | adapter | yes | `clickondetroit.com` video-path POWA | POWA fixture → Arc re-entry |
| Anvato | `anvato` | backend | yes | `anvato:<access_key_or_mcp>:<digits>` | MCP fixture + fixed auth vector |
| Anvato | `fox9` | adapter | yes | `fox9.com/video/<digits>` | URLResult → Anvato re-entry formats |
| Anvato | `fox9_news` | adapter | yes | `fox9.com/news/<slug>` | page → FOX9 → Anvato chain |
| ThePlatform | `theplatform` | backend | yes | `link|player.theplatform.com` | SMIL+preview fixtures |
| ThePlatform | `theplatform_feed` | backend | yes | `feed.theplatform.com/...byGuid|byId` | feed JSON (+ SMIL expansion) |
| ThePlatform | `weathercom` | adapter | yes | `weather.com/.../video/<slug>` | redux-dal fixture formats |
| ThePlatform | `nbcolympics` | adapter | yes | `vplayer.nbcolympics.com/p/...` | URLResult → ThePlatform re-entry |

**Counted keys: 15.** Every counted key has deterministic Suitable coverage plus
fixture-backed successful extraction or adapter→backend re-entry evidence.
Aliases (`www` hosts) and generic embed discovery are not counted. Hytale
playlists use ordered `StaticEntries` (not a lazy reusable source).

## Deliberate hardening vs pinned reference

- Wave-1 families use `strictValidHostedHTTPURL` / `strictHostedURLFormat` /
  `hostedRejectUnsafeURL`: no ports, IP literals, localhost-like hosts,
  fragments, backslash/separator tricks, literal or escaped dot segments, or
  non-canonical paths. Signed query strings remain allowed. Legacy Brightcove /
  Kaltura / JW / Wistia / SproutVideo keep prior `validHostedHTTPURL` semantics.
- Cloudflare Stream keeps the signed JWT in delivery URLs and only replaces
  metadata `id`/`title` with JWT `sub`, matching the pinned reference.
- Anvato `X-Anvato-Adst-Auth` reproduces the pinned short-key XOR behavior from
  `aes_encrypt(..., AUTH_KEY)` with the 8-byte anvplayer key; fixed vector
  `APVytK5DkP4=` for `server_time=1700000000` / FOX9 access key / video `8032455`.
- ThePlatform SMIL parsing is strict and rejects trailing XML. Feed SMIL
  content URLs are expanded, never advertised as direct downloads.
- ThePlatform / WeatherCom enforce global output cardinality
  (`thePlatformMaxFormats` / captions / feed content) and return
  `ErrInvalidMetadata` on overflow instead of silent truncation.
- WeatherCom fails closed on ThePlatform/security/metadata errors.
- Provider errors never include bodies, tokens, cookies, or signed queries.

## Negative / resource-limit evidence (test-backed)

| Area | Covered by |
|------|------------|
| Strict URL route/media rejects (dot segments, escapes, fragments, localhost, non-canonical paths) | `TestWave1StrictURLPolicyMediaAndRoutes` |
| Legacy URL semantics unchanged | `TestStrictHostedURLPolicyDoesNotChangeLegacySemantics` |
| SMIL format boundary + overflow → `ErrInvalidMetadata` | `TestThePlatformCardinalityFailClosed` |
| SMIL caption boundary + overflow → `ErrInvalidMetadata` | `TestThePlatformCardinalityFailClosed` |
| Feed SMIL expansion overflow → `ErrInvalidMetadata` | `TestThePlatformCardinalityFailClosed` |
| Feed content-entry overflow → `ErrInvalidMetadata` | `TestThePlatformCardinalityFailClosed` |
| WeatherCom SMIL-expanded format overflow → `ErrInvalidMetadata` | `TestThePlatformCardinalityFailClosed` |
| Per-family cancel/auth/malformed/truncated/oversized/secret-safe | family `*Negative*` tests |

## Provenance

| Family | Reference | Go entry points |
|--------|-----------|-----------------|
| Cloudflare Stream | `yt_dlp/extractor/cloudflarestream.py` | `cloudflarestream.go` |
| Hytale | `yt_dlp/extractor/hytale.py` | `hytale.go` |
| Arc Publishing | `yt_dlp/extractor/arcpublishing.py` | `arcpublishing.go` |
| Washington Post | `yt_dlp/extractor/washingtonpost.py` | `arcpublishing_adapters.go` |
| Anvato | `yt_dlp/extractor/anvato.py` | `anvato.go` |
| FOX9 | `yt_dlp/extractor/fox9.py` | `fox9.go` |
| ThePlatform | `yt_dlp/extractor/theplatform.py` | `theplatform.go` |
| Weather Channel | `yt_dlp/extractor/theweatherchannel.py` | `weathercom.go` |
| NBC Olympics player | `yt_dlp/extractor/nbc.py` | `weathercom.go` |
