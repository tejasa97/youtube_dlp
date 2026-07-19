# Supported extractors

ytdlp-go currently registers 28 representative native extractors. This is a
conformance catalog, not a claim of the thousands of sites supported by
upstream yt-dlp.

A listed extractor has deterministic routing and evidence for its declared
corpus. It may not cover every URL shape, playlist, account state, region,
live-state transition, anti-bot response, or subsequent service change.

| Extractor | Representative URL family | Principal risk coverage |
| --- | --- | --- |
| generic | Direct HTTP/HTTPS media | simple/direct |
| youtube | youtube.com/watch and declared playlist corpus | playlist/API, manifest-heavy, JavaScript challenge |
| vimeo | vimeo.com videos | manifest-heavy |
| twitch | twitch.tv channels | live, manifest-heavy |
| soundcloud | soundcloud.com tracks, sets, and user-track pages | playlist/API |
| streamable | streamable.com public, embed, and short-link URLs | shared backend, simple/direct |
| peertube | conservative PeerTube instance routes and peertube: opaque URLs | shared backend, live, manifest-heavy |
| internetarchive | archive.org item pages | playlist/API |
| tiktok | tiktok.com public video pages | anti-bot/impersonated |
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
or production signing material.
