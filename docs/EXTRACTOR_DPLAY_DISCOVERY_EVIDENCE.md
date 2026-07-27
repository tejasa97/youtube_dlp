# Discovery / DPlay extractor evidence

## Provenance

The implementation is derived from yt-dlp commit `aefce1eea4d0b6bab1ec2bd3beff09bff91a39c8`, principally `yt_dlp/extractor/dplay.py` (`DPlayBaseIE`, `DiscoveryPlusBaseIE`, and concrete classes) and `yt_dlp/extractor/tele5.py` (`Tele5IE`). No Python code or runtime dependency is included in this repository.

Fixtures and tests use synthetic media/API hosts only. Token values, cookies, account credentials, and signed media URLs are intentionally absent.

## Configuration matrix

| Key | Accepted public host/path family | Immutable API origin / realm | Client convention |
|---|---|---|---|
| AmHistoryChannel | `ahctv.com/video/<show>/<video>` | `us1-prod-direct.ahctv.com` / `go` | `ahc` web |
| AnimalPlanet | `animalplanet.com/video/...` | `us1-prod-direct.animalplanet.com` / `go` | `apl` web |
| CookingChannel | `watch.cookingchanneltv.com/video/...` | `us1-prod-direct.watch.cookingchanneltv.com` / `go` | `cook` web |
| DPlay | `dplay.<country>`, `discoveryplus.<country>`, `<country>.dplay.com` two-slug videos | country API / `dplay<country>` | legacy playback |
| DestinationAmerica | `destinationamerica.com/video/...` | `us1-prod-direct.destinationamerica.com` / `go` | `dam` web |
| DiscoveryLife | `discoverylife.com/video/...` | `us1-prod-direct.discoverylife.com` / `go` | `dlf` web |
| DiscoveryNetworksDe | `dmax.de` or `tlc.de` programme/show/sendungen video routes | `eu1-prod.disco-api.com` / host-derived | Hyoga |
| DiscoveryPlus | `discoveryplus.com[/country]/video[/sport|olympics]/...` excluding Italy | US or EU Discovery Plus origin / `go` or `dplay` | country web |
| DiscoveryPlusIndia | `discoveryplus.in/video(s)/...` | `ap2-prod-direct.discoveryplus.in` / `dplusindia` | India web |
| DiscoveryPlusIndiaShow | `discoveryplus.in/show/<slug>` | `ap2-prod-direct.discoveryplus.in` / `dplusindia` | show route |
| DiscoveryPlusItaly | `discoveryplus.com/it/video/...` | `eu1-prod-direct.discoveryplus.com` / `dplay` | `dplus_it` web |
| DiscoveryPlusItalyShow | `discoveryplus.it/programmi/<slug>` | `disco-api.discoveryplus.it` / `dplayit` | show route |
| FoodNetwork | `watch.foodnetwork.com/video/...` | `us1-prod-direct.watch.foodnetwork.com` / `go` | `food` web |
| GoDiscovery | `discovery.com` or `go.discovery.com/video/...` | `us1-prod-direct.go.discovery.com` / `go` | `dsc` web |
| HGTVDe | `de.hgtv.com/sendungen/...` | `eu1-prod.disco-api.com` / `hgtv` | Hyoga |
| HGTVUsa | `watch.hgtv.com/video/...` | `us1-prod-direct.watch.hgtv.com` / `go` | `hgtv` web |
| InvestigationDiscovery | `investigationdiscovery.com/video/...` | `us1-prod-direct.investigationdiscovery.com` / `go` | `ids` web |
| ScienceChannel | `sciencechannel.com/video/...` | `us1-prod-direct.sciencechannel.com` / `go` | `sci` web |
| TLC | `tlc.com` or `go.tlc.com/video/...` | `us1-prod-direct.tlc.com` / `go` | `tlc` web |
| TravelChannel | `watch.travelchannel.com/video/...` | `us1-prod-direct.watch.travelchannel.com` / `go` | `trav` web |
| Tele5 | `tele5.de/<parent>/<slug>[/<video>]` | `eu1-prod.disco-api.com` / `dmaxde` | Hyoga |

## Security evidence

`DiscoveryDPlay` accepts only strict HTTP(S) public URLs: no userinfo, ports, fragments, encoded separators/NULs, traversal, or lookalike domains. API origins and realms come exclusively from adapter configuration. The `st` cookie is read only for the exact configured API origin; otherwise a bounded device ID and bounded token response are used.

Discovery metadata and playback requests require `ScopedAuthorizationNoRedirectTransport`. This strips ambient cookies and credentials, keeps only the extraction-scoped bearer header, and refuses redirects. Media and thumbnail URLs are only returned as validated metadata; the bearer value is not placed in format headers or returned errors.

`TestDiscoveryDPlayScopedCredentialsAndMetadata` asserts the cookie-token path
and that the bearer is applied solely to the two configured API calls.
`TestDiscoveryCredentialIsolationRequestsAreBare` proves token, public CMS, and
manifest requests carry no Authorization, Proxy-Authorization, Cookie, or
ambient Referer. Routing positives and adapter-specific negatives are covered
by `TestDiscoveryDPlayRoutesAllConcreteAdapters`,
`TestDiscoveryEveryAdapterRejectsLookalikeHost`, and
`FuzzDiscoveryDPlayRouting`.

## Automated compatibility evidence

- `TestDiscoveryAllCommittedFixturesAreExercised` loads every pinned synthetic
  fixture and asserts HLS/DASH rendition and subtitle semantics.
- `TestDiscoveryManifestBoundsErrorsAndCancellation`,
  `TestDiscoveryMixedPlaybackFormatsSubtitlesAndFallback`,
  `TestDiscoveryHLSMediaPlaylistIsAFormat`, and
  `TestDiscoveryRejectsEmptyHLSMediaPlaylist` cover master/media playlists,
  empty, malformed, oversized, deduplicated, text-only, bounded, and cancelled
  manifests. `FuzzDiscoveryManifestPolicy` preserves the format bounds.
- `TestProductDiscoveryHLSAndDASHDownloadDispatch` downloads actual synthetic
  HLS fragments and DASH initialization/media segments through Discovery
  extraction.
- `TestDiscoveryTele5CMSOpaqueVideoIdentity` and
  `TestProductTele5CMSOpaqueReentryPreservesPublicIdentity` cover
  CMS-to-opaque-child-to-video recursion, public Referer, and webpage identity.
- `TestDiscoveryGermanCMSSuccessAndFallback` covers Loma enrichment, genre
  categories, not-found fallback, and malformed UID rejection.
- `TestDiscoveryShowPaginationSeasonsReuseAndFailures` covers both India and
  Italy, multiple seasons, reusable iteration, repeated/empty pages,
  inconsistent totals, season limits, and cancellation.
- `TestDiscoveryStructuredErrorMatrix` covers HTTP 401/403/404/410/429/451/5xx,
  geo and missing-package codes, malformed/trailing JSON, and body-read
  failures. `TestDiscoveryJSONBoundsNilResponsesAndCancellation` covers nil
  responses, oversized token JSON, malformed playback, and authentication
  cancellation. `TestProductCategorizesDiscoveryFailures` exercises those
  categories through the product operation. `TestDiscoveryMissingCapabilitiesConcurrencyAndSecretSafety`
  covers transport capability failure, concurrent extraction, and redacted
  errors.
- `FuzzDiscoveryTokenContentAndErrorPolicy` and `FuzzDiscoveryCMSPolicy` cover
  bounded token/content/error, Tele5, German, and show-schema invariants.

## Known limitations

The backend implements the shared single-video content/playback protocol plus
bounded, lazy Discovery Plus show pagination, Tele5 Aurora CMS lookup, and
German Loma CMS lookup. Playback and CMS behavior is synthetic-fixture backed;
there is no live subscription, entitlement, cookie, or geo validation claim.
