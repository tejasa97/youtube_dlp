# Supported extractors

## PRX

PRX supports exact HTTPS numeric `stories`, `series`, and `accounts` routes on
`prx.org`, `beta.prx.org`, and `listen.prx.org`. Story audio, series story
playlists, and account series-then-story playlists are backed by the PRX CMS
API with bounded lazy pagination. The pinned opaque search keys
`prxstories:<query>` and `prxseries:<query>` use the bounded, credential-isolated
CMS search endpoints and emit canonical story or series URL results. This claim
is fixture-backed only; no live PRX API behavior is asserted.

## Amara

Amara supports public `amara.org` video pages with optional language prefixes
and trailing `/info` path components with safe slug segments only. Metadata,
published subtitles (with aggregate and per-language bounds), direct HTTP(S)
media, and transparent YouTube/Vimeo handoffs are obtained from the bounded
`https://amara.org/api/videos/<id>/?format=json` endpoint. Transparent
handoffs preserve Amara title, description, thumbnail, duration, timestamp,
subtitles, and webpage metadata while keeping the downstream YouTube/Vimeo
video ID in the final result.

## Microsoft public media

Six exact keys cover the fixture-backed public family: `microsoft_embed`,
`microsoft_medius`, `microsoft_learn_playlist`, `microsoft_learn_episode`,
`microsoft_learn_session`, and `microsoft_build`. Coverage is restricted to
the documented HTTPS Microsoft, Medius, Learn, and Build routes, anonymous
bounded APIs, attributable media hosts, native ISM/HLS/DASH/direct downloads,
and validated Learn/Build transparent re-entry into Medius. Authenticated,
DRM-protected, signed-cookie, and live-production interoperability are not
claimed.

## TED public media

Four exact keys cover the fixture-backed anonymous public family:
`ted_talk`, `ted_series`, `ted_playlist`, and `ted_embed`. Main routes accept
strict `ted.com`/`www.ted.com` talk, series, and playlist paths; both
`embed.ted.com` and `embed-ssl.ted.com` transparently re-enter the matching
canonical `www.ted.com` route. Talks expose pinned Next metadata, attributable
HTTPS direct/HLS/audio formats, HLS subtitles, thumbnails, chapters, and
signed-query preservation. Series season fragments and playlist child IDs are
bounded and reusable. Login/private, unavailable, DRM, geo, and arbitrary
external media handoffs remain deferred.

## Niconico public ecosystem

The partial `niconico` family retains exact fixture-backed anonymous public
watch/shorts, mylist, series, user, pseudo-search, search URL, and tag URL
routes. Watch/shorts uses bounded `v3_guest` metadata, the pinned guest and
access-rights action-track shapes, and a bounded access-rights HLS master parse;
the signed `contentUrl` is preserved exactly and generic native HLS downloads
its validated manifest and fragments through a credential-isolated no-redirect
transport. Collections use reusable lazy API pagination and route children back
through the registered `niconico` key.

History, websocket live, date-recursive search, comments, premium/member/PPV,
sensitive-account, geo-restricted, and other unproven/unavailable playback
surfaces are explicitly deferred. See
`docs/EXTRACTOR_NICONICO_EVIDENCE.md` and the Niconico fixture provenance.

ytdlp-go currently registers 61 representative native extractors. This is a
conformance catalog, not a claim of the thousands of sites supported by
upstream yt-dlp.

A listed extractor has deterministic routing and evidence for its declared
corpus. It may not cover every URL shape, playlist, account state, region,
live-state transition, anti-bot response, or subsequent service change.

When an extractor exposes `subtitles` or `automatic_captions`, the common
language/format selector can write native subtitle sidecars, including with
`--skip-download`, and can embed compatible text tracks with `--embed-subs`.
Availability still depends on the extractor's declared corpus and the remote
service response.

| Extractor | Representative URL family | Principal risk coverage |
| --- | --- | --- |
| generic | Direct HTTP/HTTPS media, bounded native-provider embeds, and JSON-LD/Twitter/OpenGraph media | simple/direct, shared backend |
| youtube | youtube.com/watch and youtu.be, /embed, /shorts, /playlist, and channel live alias URLs | playlist/API, manifest-heavy, JavaScript challenge |
| vimeo | vimeo.com public videos and contextual child URLs; authenticated direct unlisted-share URLs; bounded public text tracks, channels, user profiles, groups, and numeric or safe-slug public album/showcase playlists | authenticated, playlist/API, manifest-heavy; fixture-backed profile-block status handling |
| twitch family | `twitch_stream`, `twitch_vod`, `twitch_clips`, `twitch_collection`, `twitch_videos`, `twitch_videos_clips`, and `twitch_videos_collections` on Twitch public routes | live, playlist/API, manifest-heavy; anonymous only |
| soundcloud | soundcloud.com tracks with original downloads, opt-in public comments, and artwork/avatar thumbnail matrices; sets with tokenized private-set hydration; bare profiles; all pinned public profile tabs; legacy API user playlists; player/embed URLs; and bounded public search | playlist/API |
| applepodcasts | podcasts.apple.com public episode pages with an explicit numeric episode query ID | simple/direct |
| streamable | streamable.com public, embed, and short-link URLs | shared backend, simple/direct |
| aeonco | exact HTTPS aeon.co and www.aeon.co `/videos/{slug}` pages with bounded JSON-LD Vimeo or YouTube handoffs | shared backend |
| amara | amara.org public video pages with optional language prefix | shared backend, playlist/API |
| peertube (+ account/channel/playlist) | conservative PeerTube instance routes and peertube: opaque video URLs | shared backend, playlist/API, live, manifest-heavy |
| internetarchive | archive.org item pages | playlist/API |
| tiktok | tiktok.com public video pages, vm/vt/t short links, and bounded webpage captions | anti-bot/impersonated |
| synthetic_auth | auth-fixture.invalid deterministic test service | authenticated behavior only; not a public service |
| region_svt | svtplay.se videos/series and svt.se article playlists | playlist/API, regional, live |
| brightcove | players.brightcove.net embeds | shared backend, manifest-heavy |
| kaltura | kaltura: opaque URLs | shared backend |
| jwplatform | cdn.jwplayer.com players | shared backend |
| wistia | wistia: opaque URLs and declared embeds | shared backend, playlist/API |
| sproutvideo | videos.sproutvideo.com embeds | shared backend |
| dailymotion | dailymotion.com videos, `/playlist/{id}`, public search, and user/channel uploads | playlist/API |
| reddit | reddit.com post pages | playlist/API |
| twitter | x.com and declared Twitter status URLs | playlist/API |
| bandcamp | artist Bandcamp track/album pages, artist discography playlists, and Bandcamp Weekly radio episodes | playlist/API |
| mixcloud | mixcloud.com cloudcast pages | playlist/API |
| rumble | rumble.com declared embed/video pages | playlist/API, live |
| bilibili | public bilibili.com video, Player, Dynamic, Bangumi, collections, series, categories, audio, and BiliIntl routes; 13 promoted | playlist/API, manifest-heavy |
| niconico family | nicovideo.jp watch/shorts, mylists, series, users, bounded search and tag routes | playlist/API, manifest-heavy; anonymous-only partial claim |
| instagram | instagram.com posts | playlist/API, anti-bot/impersonated |
| kick | kick.com channels | live, anti-bot/impersonated, manifest-heavy |
| bbciplayer | bbc.co.uk iPlayer episodes | playlist/API, manifest-heavy, regional |
| bbc_co_uk_article | bbc.co.uk programme articles | playlist/API, regional |
| bbc_co_uk_playlist | bbc.co.uk programmes listings | playlist/API, regional |
| bbc_co_uk_iplayer_episodes | bbc.co.uk iPlayer episodes listings | playlist/API, regional |
| bbc_co_uk_iplayer_group | bbc.co.uk iPlayer group listings | playlist/API, regional |
| ard | ardmediathek.de player pages | playlist/API, manifest-heavy, regional |
| ard_mediathek_collection | ardmediathek.de public sendung, serie, sammlung, season, OV, and AD collection pages | playlist/API, regional |
| ard_audiothek (+ playlist) | ardaudiothek.de and ardsounds.de public episode and bounded show playlist pages | playlist/API, simple/direct, regional |
| nrk family | tv/radio/nrksuper programme and live routes, series/season catalogs, podcast UUIDs, Skole mediaId lookup, nrk.no article playlists, and nrk: opaque playback | playlist/API, manifest-heavy, regional |
| rai family | RaiPlay/RaiPlay Sound VOD, live and program routes; legacy Rai, Rai News, Rai Cultura, and Rai Südtirol public pages | playlist/API, manifest-heavy, regional, live, audio; bounded unencrypted Rai F4M/HDS VOD |
| nhk family | NHK World VOD/program; NHK for School bangumi/subject/program-list; Radiru on-demand/news/live (`--nhk-area`) | playlist/API, manifest-heavy, regional, live, audio |
| bluesky | bsky.app, www.bsky.app, main.bsky.dev post URLs and at:// URIs (public posts only) | playlist/API, manifest-heavy, regional |
| imgur | imgur.com and i.imgur.com public videos, animated images, galleries, and albums | simple/direct, playlist/API |
| flickr | flickr.com public video pages | simple/direct, playlist/API |
| Discovery / DPlay family | Discovery, DPlay, Discovery+, Discovery+ India/Italy shows, AHC, Animal Planet, Cooking Channel, Destination America, Discovery Life, Food Network, HGTV, Investigation Discovery, Science Channel, TLC, Travel Channel, Tele5, DMAX/TLC Germany | playlist/API, manifest-heavy, regional/authenticated API |
| Microsoft public media family | microsoft.com videoplayer embeds; medius.microsoft.com Embed routes; learn.microsoft.com shows/events playlists and child pages; build.microsoft.com sessions | playlist/API, manifest-heavy, simple/direct |
| TED public media family | ted.com/www.ted.com talks, series, playlists, and embed/embed-ssl canonical routes | simple/direct, playlist/API, manifest-heavy |
| VK public ecosystem family | `vk.com`, `m.vk.com`, `new.vk.com`, `vkvideo.ru`, `vksport.vkvideo.ru` public video/clip/embed, user/group, and wall routes; `vkplay.live`, `live.vkplay.ru`, and `live.vkvideo.ru` recordings and signed live HLS | playlist/API, simple/direct, live, manifest-heavy |

## Shared-family breadth (Wave 1 + priority-100)

In addition to the representative catalog above, the product registry
registers shared-backend families and exact-host adapters with fixture-backed
evidence. Counts and host policies are recorded in
`docs/EXTRACTOR_FAMILY_BREADTH_WAVE1_EVIDENCE.md` and
`docs/EXTRACTOR_BREADTH_PRIORITY_100_EVIDENCE.md`.

| Family / key group | Representative URL family | Principal risk coverage |
| --- | --- | --- |
| cloudflarestream (+ hytale) | Cloudflare Stream / videodelivery / customer-* embeds; hytale.com news | shared backend, manifest-heavy |
| arcpublishing (+ newsroom adapters) | `arcpublishing:org:uuid` and exact POWA hosts | shared backend, manifest-heavy |
| anvato (+ fox9) | `anvato:` MCP URLs; fox9.com video/news | shared backend, authenticated |
| theplatform (+ weathercom / nbcolympics) | link/player/feed.theplatform.com; weather.com; vplayer.nbcolympics.com | shared backend, playlist/API, manifest-heavy |
| brightcove adapters | pgatour, 9news/9now, NetApp, AMC Networks, Craftsy, TVO, TVA+, TVA Nouvelles; Formula 1, European Tour, MaoriTV, The Star, The Sun, Wimbledon, USA Today, Sky News AU | shared backend |
| kaltura / jwplatform adapters | UN WebTV, AZ Medien, Inc, Heise; Spiegel, OneFootball | shared backend |
| podcast feeders | Acast, Simplecast, Megaphone, Art19, Libsyn, Spreaker episode/show URLs | shared backend, playlist/API |
| nowness (+ playlist/series) | nowness.com / cn.nowness.com story, playlist, series | playlist/API, Brightcove/Vimeo handoff |
| dacast (+ playlist) | iframe.dacast.com vod/playlist embeds | shared backend, playlist/API, HLS |
| panopto (+ playlist) | `*.panopto.com` / `*.panopto.eu` Viewer/Embed `id`/`pid` (same-tenant playlist binding) | shared backend, playlist/API |
| teachingchannel / nowcanal / democracynow / buzzfeed | JW / Brightcove / page-JSON / bucket playlist | shared backend, playlist/API |
| mediastream (+ winsports) | mdstrm.com embed/live-stream; winsports.co → mediastream | shared backend, live |
| abcotvs (+ clips) | ABC OTV station hosts + clips.abcotvs.com | shared backend |
| vidsio | `*.vids.io/videos/...` → sproutvideo | shared backend |
| laracasts (+ series) | laracasts.com series episode/series → vimeo | shared backend, playlist/API |

These entries are deterministic-corpus compatible only. They do not claim live
service coverage beyond synthetic fixtures and documented handoff behavior.

## Vimeo authenticated unlisted-video boundary

Direct HTTPS `{numeric-id}/{10-lowercase-hex-hash}` share URLs use an existing
`vimeo` session cookie to mint an authenticated viewer JWT and resolve
metadata, player formats, and an available logged-in source/original format.
Query strings and fragments are accepted but stripped before requests. The
cookie is confined to the exact no-redirect viewer endpoint, the JWT to
`api.vimeo.com`, and config/CDN hosts receive neither. Interactive login,
password submission, non-share private URL discovery, DRM, and live archives
remain outside this bounded support claim.

## Discovery / DPlay support boundaries

The Discovery family uses exact-host adapters over one bounded content,
playback, and token backend. Both legacy keyed playback and v3 playback lists
are supported. HLS master and playable media playlists, one honest DASH MPD
format, manifest subtitles, direct HTTP formats, and product download dispatch
have deterministic coverage. India and Italy show routes use reusable, lazy,
multi-season pagination with response-identity, page, season, entry, and
cancellation bounds. Empty pages advance normally, repeated non-empty responses
fail closed, and ordered episode occurrences are preserved across season
filters. DPlay and Discovery Plus India download Referers propagate to
manifests, fragments, and direct media without API bearer headers.

Tele5 Aurora CMS recursion keeps its validated public URL as both API Referer
and final `webpage_url`; opaque child URLs never appear in returned metadata.
German DMAX/TLC routes use bounded Loma CMS enrichment with a tested not-found
fallback; HGTV Germany uses the direct Hyoga playback flow. Subscription
entitlement, live account state, geo availability, and future service schema
changes are not claimed by synthetic fixtures.

## Bluesky support boundaries

The Bluesky/AT Protocol extractor is intentionally scoped to the
unauthenticated `public.api.bsky.app` XRPC surface for public posts. The
following are supported:

- `bsky.app`, `www.bsky.app`, and `main.bsky.dev` post URL families of the
  form `/profile/<handle-or-DID>/post/<post-id>`;
- `at://` URIs of the form `at://<handle-or-DID>/app.bsky.feed.post/<post-id>`
  including both DNS-style handles and `did:plc`/`did:web` authors;
- the public `app.bsky.feed.getPostThread` XRPC with the deterministic
  `uri`, `depth=0`, `parentHeight=0` query and `Accept: application/json`;
- PDS resolution from `plc.directory` (did:plc) and `.well-known/did.json`
  (did:web) for the first exact-type `AtprotoPersonalDataServer` endpoint
  with a non-IP, non-local, HTTPS hostname form, plus a deterministic fallback to
  `https://bsky.social` on transient or non-fatal resolution failure;
- three documented embed shapes -- `app.bsky.embed.video.view`,
  `app.bsky.embed.recordWithMedia.view`, and one bounded
  `app.bsky.embed.record.view` (covering the `record` and `value`
  alternates) with depth and dedup guards;
- the HLS playlist format (`format_id=hls`, `protocol=m3u8_native`,
  `ext=mp4`) plus the optional preferred direct blob format at
  `<trusted-pds>/xrpc/com.atproto.sync.getBlob?did=...&cid=...` when the
  author DID and video CID are available;
- ordered, deduped, bounded labels/tags, age 18 for sexual/porn/graphic
  labels, and normalized timestamps, upload date, and counts;
- transparent external URL routing via the standard `url_result` path;
- deterministic categorization for 401/403, 404/410, 429/5xx, malformed
  JSON, oversize JSON, cancellation, and the public errors above; and
- synthesized fixtures and fuzz coverage for routing, URL/AT URI bounds,
  record shapes, blob URL hardening, DID doc trust, and secret safety.

The following limitations are intentional and remain:

- no login, authenticated sessions, or private repositories;
- no profile, feed, or arbitrary-record enumeration (only
  `app.bsky.feed.post` records via the public post thread endpoint);
- restricted did:web support to syntactically public HTTPS hostnames (no IP
  literals, no loopback, no `.local`/`.internal` suffixes, no userinfo,
  no port, no encoded separator/NULs);
- one bounded nested embed level with deterministic dedup of duplicate
  playlists and CIDs;
- HLS is delegated to the existing m3u8 downloader (no inline
  segment fetching or signing);
- no record-level webhook, notification, list, or starter-pack
  enumeration; and
- fixtures and conformance evidence establish the public-post contract
  and do not by themselves promote this extractor to G3/G4 readiness or
  full upstream parity.

## YouTube support boundaries

The YouTube extractor's scope matches the functionality completed in the
protected-playback workstream. The following are supported:

- watch URLs (`youtube.com/watch?v=...`) and youtu.be short links;
- embed URLs (`youtube.com/embed/...`);
- Shorts (`youtube.com/shorts/...`);
- playlists (`youtube.com/playlist?list=...`) including modern
  `lockupViewModel` playlist renderers and continuation paging;
- explicit public channel tabs at
  `/channel/<UCID>/{videos,shorts,streams,playlists,home,featured,community,releases,podcasts,membership}`,
  including bounded lazy continuation paging;
- channel-advertised custom tabs under the same channel/handle/alias identity
  when YouTube lists a securely bound `tabRenderer`/`expandableTabRenderer`
  endpoint (cross-host, identity-swap, and encoded-separator endpoints are
  rejected), with stable tab id/title/approx-count metadata when present;
- channel-local search at `/channel/<UCID>/search?query=...`,
  `/@handle/search?query=...`, and legacy `/user`/`/c` equivalents;
- explicit public Unicode-aware handle tabs at
  `/@handle/{videos,shorts,streams,playlists,home,featured,community,releases,podcasts,membership}`,
  including bounded lazy continuation;
- explicit public legacy alias tabs at
  `/user/<alias>/<tab>` and `/c/<alias>/<tab>` for the same explicit tab set,
  including bounded Unicode aliases and lazy continuation;
- bare `/channel/<UCID>`, `/@handle`, `/user/<alias>`, and `/c/<alias>` roots,
  aggregated lazily in videos, streams, then Shorts order without including
  home-page shelves, including bounded conditional regional-channel routing;
- bounded public searches using `ytsearch:` / `ytsearchN:` / `ytsearchall:`
  (capped at 50; video-filtered) and exact `/results` or `/search` URLs that
  emit video, Shorts, playlist, channel, and shelf URL results plus supported
  filter/sort `sp` values (accepted after `ParseQuery` decoding; unknown or
  double-encoded `sp` values are rejected);
- bounded YouTube Music searches at `music.youtube.com/search`, including
  pinned `#songs`, `#videos`, `#albums`, `#artists`, `#community+playlists`,
  and `#featured+playlists` sections with songs/videos as watch URLs and
  albums/artists/playlists/podcasts as typed URL results for registered
  playlist, channel, and Music browse routes;
- bounded YouTube Music browse at exact `music.youtube.com/browse/{id}` for
  registered album (`MPRE`), artist (`UC` / `MPLA`+UCID), playlist (`VL`), and
  podcast (`MPSP`) families via cookie-isolated WEB_REMIX page GET and
  continuations (isolation required before any network call; anonymous public
  success; logged-in/premium/WEB-identity pages fail closed; albums require a
  canonical `music.youtube.com/playlist?list=` identity from microformat or
  WEB_REMIX resolve+browse or fail closed);
- authenticated exact-origin WEB browse/search continuations when a logged-in
  page and redirect-disabled cookie transport are present (no anonymous
  fallback after authenticated state; incomplete logged-in config fails closed;
  browse visitor rotation across continuation pages; search reuses the initial
  visitor; WEB and WEB_REMIX identities stay isolated);
- channel live aliases (`@handle/live`, `/channel/<id>/live`, `/user/<name>/live`,
  `/c/<name>/live`) routed into the resolved live video;
- manual and automatic captions exposed as `subtitles` and
  `automatic_captions`; automatic captions are translated across every
  language YouTube advertises, while translated manual captions are
  generated only when the caller explicitly opts in;
- bounded language/format selection and native subtitle sidecar downloads,
  including subtitle-only operation with `--skip-download`;
- post-download conversion of written subtitle sidecars to SRT, ASS, or WebVTT
  with `--convert-subs`;
- bounded multi-track subtitle embedding in MP4/MOV/M4A/WebM/MKV/MKA outputs
  with `--embed-subs`;
- deterministic `--list-subs` output for available automatic and manual
  caption languages, names, and formats without writing files;
- bounded, opt-in public video comments with `--write-comments` or
  `--get-comments`, `top`/`new` ordering, actual retrieved `comment_count`,
  visitor rotation, legacy and modern comment fields, click-tracked root and
  reply continuations, nested subthreads, transient/incomplete-response
  retries, pinned duplicate handling, and explicit
  total/parent/reply/per-thread/depth limits;
- adaptive video and audio formats recovered from the WEB player response and
  bounded anonymous Innertube clients (`android`, `android_vr`, `web_safari`,
  `ios`, `mweb`) plus authenticated `tv_downgraded` / `web_safari`
  defaults and premium `tv_downgraded` / `web_creator` (non-Premium
  `web_creator` only when age-gated; Premium changes GVS PO-token
  requirements only);
- bounded finite reconstruction of retained post-live adaptive audio/video
  sequences, followed by the normal ffmpeg merge path; and
- opt-in bounded active `--live-from-start` reconstruction with signed-URL
  refresh, concurrent adaptive audio/video transfer, and final merging; and
- a protected-playback token provider boundary that requests PO tokens from a
  pluggable director for GVS, player, and subtitle contexts according to the
  explicit provider, fetch-mode, and client policy.

The following limitations are intentional and remain:

- membership tab extraction is bounded to the explicit `/membership` routes
  above and returns member videos only when the supplied transport/session is
  already authorized; it does not add authentication flags, purchase flows, or
  entitlement acquisition;
- custom tabs are accepted only when advertised by the requested channel page
  and securely bound to that channel identity; arbitrary YouTube endpoints and
  cross-channel pivots are rejected;
- Music search does not claim authenticated/premium WEB_REMIX success, and
  invented section filters beyond the pinned upstream section map are rejected;
- Music browse does not claim authenticated/premium WEB_REMIX success; logged-in
  Music pages, WEB client identity on Music browse, missing cookie isolation,
  album identity pivots, and albums without a canonical Music playlist identity
  fail closed;
- hashtag URL results are emitted through the registered `youtube_hashtag`
  extractor for validated `/hashtag/<tag>` routes; shelf URL results remain
  emitted for flat listing without a dedicated shelf route;
- live-from-start and finite `post_live` DVR reconstruction use the documented
  segment/poll bounds and do not support external-downloader delegation or
  process-restart resume;
- authenticated Innertube coverage is bounded: a logged-in watch page can
  recover URL-bearing formats through the webpage WEB player and then
  `tv_downgraded` / authenticated `web_safari` (exact `www.youtube.com`
  origin + SID; no anonymous downgrade; first successful candidate wins),
  with `web_creator` on Premium defaults or when age-gated playability is
  attributable (`web_creator` GVS tokens required unless Premium). Opt-in
  comments and browse/search continuations use the same account-bound,
  redirect-disabled WEB session. The retained bounded finite-VOD SABR
  implementation is experimental and outside the supported compatibility
  corpus and roadmap; live/post-live SABR and `STREAM_PROTECTION_STATUS`
  remain fail-closed non-goals;
- comment extraction does not synthesize estimated timestamps or expose
  YouTube's approximate count before retrieval, and supports only the
  explicitly tested legacy and modern renderer families;
- some protected active streams may still hit the documented EJS-helper
  timeout while the player challenge is being solved;
- when a caller separately selects an adaptive video stream and an adaptive
  audio stream, they must be merged with ffmpeg (or an equivalent muxer);
  downloads that pick a single muxed format do not require ffmpeg.

This is not a claim of full yt-dlp or full YouTube parity. Coverage is
limited to the deterministic corpus checked into
`conformance/extractors/youtube/`,
`conformance/extractors/youtube_channel/`,
`conformance/extractors/youtube_handle_tab/`,
`conformance/extractors/youtube_alias_tab/`,
`conformance/extractors/youtube_search/`,
`conformance/extractors/youtube_music_search/`,
`conformance/extractors/youtube_renderer/`,
`conformance/extractors/youtube_channel_search/`, and the bounded evidence listed in
`conformance/parity_manifest.yaml`.

## Protocol coverage

Selected formats may use:

- direct HTTP or HTTPS;
- HLS VOD and declared live behavior;
- DASH segment templates, lists, timelines, and declared live behavior;
- ISM/Smooth Streaming fragments; and
- an explicitly selected shell-free external downloader.

Multi-track media may require ffmpeg. DRM decryption is not implemented.

## Deterministic evidence versus live canaries

Compatibility status comes only from checked-in deterministic fixtures and
automated evidence named by conformance/parity_manifest.yaml. Live canaries are
opt-in interoperability checks. They may detect service drift but cannot
promote or preserve a compatibility claim.

Fixtures use synthetic or reserved identifiers, generated media, attributable
schema-derived expectations, and no real account credentials. Provenance is
stored beside each corpus.

## Reporting a site problem

Before reporting a failure:

1. reproduce with the current source revision;
2. include ytdlp-go --version and the extractor name;
3. use --skip-download --print-json when that reproduces the issue safely;
4. remove cookies, authorization values, signed query parameters, personal
   data, and private media details; and
5. distinguish an unsupported URL shape from a regression in a listed corpus.

Security-sensitive failures must be reported privately under
[SECURITY.md](../SECURITY.md). Do not attach browser profiles, cookies, tokens,
or production signing material. See [Support](../SUPPORT.md) for the complete
public-report checklist and scope boundaries.
