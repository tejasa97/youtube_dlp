# Supported extractors

ytdlp-go currently registers 28 representative native extractors. This is a
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
| generic | Direct HTTP/HTTPS media and bounded native-provider HTML embeds | simple/direct, shared backend |
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

## YouTube support boundaries

The YouTube extractor's scope matches the functionality completed in the
protected-playback workstream. The following are supported:

- watch URLs (`youtube.com/watch?v=...`) and youtu.be short links;
- embed URLs (`youtube.com/embed/...`);
- Shorts (`youtube.com/shorts/...`);
- playlists (`youtube.com/playlist?list=...`) including modern
  `lockupViewModel` playlist renderers and continuation paging;
- explicit public channel tabs at
  `/channel/<UCID>/{videos,shorts,streams,playlists}`, including bounded lazy
  continuation paging;
- explicit public ASCII-handle tabs at
  `/@handle/{videos,shorts,streams,playlists}`, including bounded lazy
  continuation;
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
  the Android / Android VR format-recovery clients; and
- a protected-playback token provider boundary that requests PO tokens from a
  pluggable director for GVS, player, and subtitle contexts according to the
  explicit provider, fetch-mode, and client policy.

The following limitations are intentional and remain:

- no general channel discovery or arbitrary tab enumeration: Unicode/full
  handles, channel home, community, and release tabs, plus `/user` and `/c`
  tabs, are not extracted as playlists;
- general search does not cover channel/playlist/hashtag results,
  authenticated search, or arbitrary filter/sort parity; Music search excludes
  albums, artists, playlists, podcasts, arbitrary filters, and
  authenticated/premium success;
- no live-from-start parity (post-live DVR segments and live rewinds are not
  reconstructed to the original stream start);
- authenticated Innertube coverage remains limited: `LOGIN_REQUIRED`
  playability surfaces an authentication error rather than a signed-in
  recovery path, and authenticated comments are not supported;
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
