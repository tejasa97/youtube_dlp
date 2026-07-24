# Supported extractors

ytdlp-go currently registers 30 representative native extractors. This is a
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
| vimeo | vimeo.com videos with bounded public text tracks | manifest-heavy |
| twitch | twitch.tv channels | live, manifest-heavy |
| soundcloud | soundcloud.com tracks, sets, user-track pages, and bounded public search | playlist/API |
| streamable | streamable.com public, embed, and short-link URLs | shared backend, simple/direct |
| peertube | conservative PeerTube instance routes and peertube: opaque URLs | shared backend, live, manifest-heavy |
| internetarchive | archive.org item pages | playlist/API |
| tiktok | tiktok.com public video pages with bounded webpage captions | anti-bot/impersonated |
| synthetic_auth | auth-fixture.invalid deterministic test service | authenticated behavior only; not a public service |
| region_svt | svtplay.se video pages | regional, live |
| brightcove | players.brightcove.net embeds | shared backend, manifest-heavy |
| kaltura | kaltura: opaque URLs | shared backend |
| jwplatform | cdn.jwplayer.com players | shared backend |
| wistia | wistia: opaque URLs and declared embeds | shared backend, playlist/API |
| sproutvideo | videos.sproutvideo.com embeds | shared backend |
| dailymotion | dailymotion.com videos | playlist/API |
| reddit | reddit.com post pages | playlist/API |
| twitter | x.com and declared Twitter status URLs | playlist/API |
| bandcamp | artist Bandcamp track pages | playlist/API |
| mixcloud | mixcloud.com cloudcast pages | playlist/API |
| rumble | rumble.com declared embed/video pages | playlist/API, live |
| bilibili | bilibili.com video pages | playlist/API, manifest-heavy |
| instagram | instagram.com posts | playlist/API, anti-bot/impersonated |
| kick | kick.com channels | live, anti-bot/impersonated, manifest-heavy |
| bbciplayer | bbc.co.uk iPlayer episodes | playlist/API, manifest-heavy, regional |
| ard | ardmediathek.de player and collection pages | playlist/API, manifest-heavy, regional |
| nrk | nrk.no pages and nrk: opaque URLs | playlist/API, manifest-heavy, regional |
| bluesky | bsky.app, www.bsky.app, main.bsky.dev post URLs and at:// URIs (public posts only) | playlist/API, manifest-heavy, regional |
| imgur | imgur.com and i.imgur.com public videos, animated images, galleries, and albums | simple/direct, playlist/API |

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
  `/channel/<UCID>/{videos,shorts,streams,playlists,home,featured,community,releases,podcasts}`,
  including bounded lazy continuation paging;
- explicit public Unicode-aware handle tabs at
  `/@handle/{videos,shorts,streams,playlists,home,featured,community,releases,podcasts}`,
  including bounded lazy continuation;
- explicit public legacy alias tabs at
  `/user/<alias>/<tab>` and `/c/<alias>/<tab>` for the same explicit tab set,
  including bounded Unicode aliases and lazy continuation;
- bare `/channel/<UCID>`, `/@handle`, `/user/<alias>`, and `/c/<alias>` roots,
  aggregated lazily in videos, streams, then Shorts order without including
  home-page shelves, including bounded conditional regional-channel routing;
- bounded public video searches using `ytsearch:`, `ytsearchN:`,
  `ytsearchall:` (capped at 50), and exact `/results` or `/search` URLs;
- bounded playable YouTube Music searches at `music.youtube.com/search`,
  including pinned `#songs` and `#videos` sections;
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
  the Android / Android VR format-recovery clients;
- bounded finite reconstruction of retained post-live adaptive audio/video
  sequences, followed by the normal ffmpeg merge path; and
- opt-in bounded active `--live-from-start` reconstruction with signed-URL
  refresh, concurrent adaptive audio/video transfer, and final merging; and
- a protected-playback token provider boundary that requests PO tokens from a
  pluggable director for GVS, player, and subtitle contexts according to the
  explicit provider, fetch-mode, and client policy.

The following limitations are intentional and remain:

- no general channel discovery or arbitrary tab enumeration beyond the
  explicit tabs and bounded bare-root upload aggregation above;
- general search does not cover channel/playlist/hashtag results,
  authenticated search, or arbitrary filter/sort parity; Music search excludes
  albums, artists, playlists, podcasts, arbitrary filters, and
  authenticated/premium success;
- live-from-start and finite `post_live` DVR reconstruction use the documented
  segment/poll bounds and do not support external-downloader delegation or
  process-restart resume;
- authenticated Innertube coverage remains limited: a logged-in watch page can
  recover URL-bearing formats through one exact-origin WEB player request when
  valid YouTube SID cookies and bounded WEB configuration are present. Opt-in
  comments use the same account-bound, redirect-disabled WEB session for every
  root, reply, sort, and retry continuation. Authenticated browse/search/Music
  clients, broader player-client rotation, and direct SABR/UMP are not
  supported;
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
`conformance/extractors/youtube_music_search/`, and the bounded evidence listed in
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
