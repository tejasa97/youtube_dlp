# Niconico public ecosystem evidence

The pinned upstream source is `yt-dlp` commit
`aefce1eea4d0b6bab1ec2bd3beff09bff91a39c8`, especially
`yt_dlp/extractor/niconico.py`. The native implementation is deliberately a
partial anonymous public closure.

## Promoted matrix

| Upstream family | Native key | Retained contract |
| --- | --- | --- |
| `NiconicoIE` | `niconico` | exact `nicovideo.jp` watch/shorts routes, bounded `v3_guest` metadata, pinned access-rights output pairing, bounded HLS-master audio/video formats, signed manifest URL preservation, metadata and validated thumbnails |
| `NiconicoPlaylistIE` | `niconico_playlist` | exact Niconico/`nico.ms` mylist routes, lazy reusable page API, source-row pagination bounds, child re-entry |
| `NiconicoSeriesIE` | `niconico_series` | exact Niconico/`nico.ms` series routes, lazy reusable page API, source-row pagination bounds, child re-entry |
| `NiconicoUserIE` | `niconico_user` | exact public user video routes, bounded page API and child re-entry |
| `NicovideoSearchIE` | `niconico_search` | bounded `nicosearch:<term>` pseudo-search HTML pages |
| `NicovideoSearchURLIE` | `niconico_search_url` | bounded `/search/<term>` HTML pages with one-value known filters |
| `NicovideoTagURLIE` | `niconico_tag` | bounded `/tag/<term>` HTML pages with one-value known filters |

Every claimed API/page request requires `CredentialIsolatedNoRedirectTransport`.
The guest watch request uses the pinned `AAAAAAAAAA_<unix milliseconds>` action
track shape; the access-rights POST uses the returned `watchTrackId` exactly.
The product marks Niconico media formats with both `_credential_isolated` and
`_niconico_scoped`; the product HLS wrapper validates every manifest and segment
URL against the exact attributable HTTPS host `delivery.domand.nicovideo.jp`
before the shared HLS downloader sees it. Signed content and media URLs are
stored exactly as received.

The watch extractor maps HTTP and envelope failures into secret-safe typed
errors for authentication, unavailable, premium, membership, PPV, sensitive,
scheduled, geo, rate-limit, server, and redirect outcomes. It never includes
access-right keys, action-track IDs, signed URLs, cookies, search terms, or
response bodies in an error.

The local product suite exercises the registered registry through the public
`Client.Run` lifecycle, including exact best-video and best-audio HLS bytes, pinned
audio/video IDs and metadata, access-rights output ordering, ambient four-header
stripping at API/access-rights/manifest/segment/thumbnail hops, signed query
preservation, hostile-host and redirect refusal, and failure
cleanup/cancellation while a segment request is entered. The authoritative
product evidence is limited to these public `Client.Run` tests:

- `engine.TestProductNiconicoRegisteredWatchHLSDownloadIsolatedAndSigned`
- `engine.TestProductNiconicoRegisteredBestAudioDownloadIsExactAndIsolated`
- `engine.TestProductNiconicoRegisteredSearchChildReentryIsolation`
- `engine.TestProductNiconicoPlaylistChildReentryUsesRegisteredKeys`
- `engine.TestProductNiconicoHLSFailureAndCancellationLeaveNoArtifacts`

The table-driven playlist
test executes mylist,
series, and user-video roots with valid `sm10`/`sm11` rows on source page 1
and `sm12` on source page 2; each child is re-entered through the registered
`niconico` extractor, access-rights POST, scoped manifest/segment path, and
output writer twice through the same configured client and proves identical
child IDs and exact bytes with fresh bounded page fetches. The corresponding
search product test executes the retained search URL, tag URL, and
`nicosearch` pseudo-search roots twice through the same configured client and
proves the same re-entry and page-boundary properties.

## Explicit deferrals

- `NiconicoHistoryIE`: `requires_authentication_or_antibot`; the pinned
  history/likes API requires authenticated cookies, and there is no registered
  native mapping or product evidence for it.
- `NiconicoLiveIE`: `requires_authentication_or_antibot`; the pinned live path
  requires websocket seat acquisition, stream cookies, heartbeat/reconnect, and
  live lifecycle behavior, with no registered native mapping or product evidence.
- `NicovideoSearchDateIE`: `requires_new_backend`; the pinned implementation
  recursively splits wall-clock date intervals, and there is no registered exact
  class mapping, fixed bounded native contract, or product evidence.
- Comments, premium/member/PPV, sensitive-account, geo-restricted, and other
  unavailable playback variants are deferred.

The deterministic corpus is synthetic and contains no production account data,
cookies, access keys, signed URLs, or live captures. The authoritative class
by class review is regenerated into
`conformance/extractors/upstream_master_checklist.csv` from the pinned checkout.

The native route is intentionally stricter than upstream at the media boundary:
only exact HTTPS `delivery.domand.nicovideo.jp` master, rendition, and segment
URLs are admitted, redirects are rejected, and malformed or unrecognized HLS
master entries fail closed. This is a safety boundary and a declared partial
route, not a claim that every upstream DMS host or response variant is covered.
