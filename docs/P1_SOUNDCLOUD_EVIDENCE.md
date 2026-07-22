# Phase 1 SoundCloud extractor pilot

This lane implements a Python-free representative API and playlist extractor.
The shared registry and parity manifest remain owned by the primary integrator.

## Supported pilot behavior

- Strict matching for public SoundCloud track URLs, private `s-*` track links,
  public set URLs, direct API track/playlist IDs, and public user `/tracks`
  collections. Ambiguous profile resources and non-SoundCloud hosts are not
  claimed.
- Track stations: `soundcloud.com/stations/track/<artist>/<track>` resolved
  through the v2 resolve endpoint. The opaque station identifier
  `soundcloud:track-stations:<positive-numeric-id>` is validated before any API
  path is constructed. Station tracks are fetched from
  `stations/<station-id>/tracks` with linked partitioning and lazy ordered
  transparent entries. Playlist metadata: ID = numeric track ID, title =
  `Track station: <resolved title>`.
- Related-resource pages: `soundcloud.com/<artist>/<track>/recommended`,
  `/albums`, and `/sets`. The base track URL (without the relation suffix) is
  resolved, the track ID and title are validated, and relation-specific API
  routes are used: `tracks/<id>/related`, `tracks/<id>/albums`, and
  `tracks/<id>/playlists_without_albums`. Playlist metadata: ID = resolved
  numeric track ID, title = `<track title> (Recommended|Albums|Sets)`,
  webpage_url = original canonical relation URL.
- Bounded client-ID discovery from the SoundCloud homepage and at most 64
  first-party `soundcloud.com`/`sndcdn.com` script candidates. The identifier is
  cached per extractor instance and refreshed once after API 401/403 responses.
- Bounded v2 API metadata requests and per-transcoding URL resolution, including
  progressive HTTP, native HLS, encrypted-HLS labeling, codec/extension/bitrate
  normalization, preview labeling, URL de-duplication, broken ABR rejection,
  and explicit DRM/block handling.
- Response cardinalities are capped at 64 transcodings, 200 linked-partition
  entries per page, and 10,000 set entries; URLs, slugs, tokens, assets, and
  JSON bodies also have explicit limits.
- Deterministic normalized track metadata for identifiers, title/track,
  uploader, duration, timestamps, counts, license, genre, artwork, webpage URL,
  and audio-only formats.
- Ordered transparent URL entries for sets/API playlists. Missing permalink
  URLs fall back to direct v2 track URLs and preserve a private set token.
- Lazy, independently reusable linked-partition iterators for public user tracks,
  track stations, and related-resource pages.
- Route-aware continuation policy: every `next_href` must use HTTPS, the exact
  `api-v2.soundcloud.com` host, no userinfo or explicit port, no encoded
  separators or NULs, a path that exactly matches the original playlist family
  (preventing cross-user, cross-track, cross-station, and cross-relation
  transitions), bounded query values, and stale client IDs are stripped.
  Repeated cursors terminate safely.
- Mixed track/playlist collection decoding: collection items are resolved as
  inline track, nested track, then playlist, matching the reference
  `resolve_entry` ordering. Playlist entries without a classifiable permalink
  fall back to the v2 API playlist URL.
- HTTP authentication/unavailability, malformed metadata/playlists/station
  identifiers, oversized bodies/pages, invalid continuations, related `errors`
  fields, and missing formats have categorized errors. Request/response bodies
  and secret tokens are absent from diagnostics. Cancellation is observable
  through `errors.Is` with `context.Canceled` or `context.DeadlineExceeded`.

## Automated evidence

- `internal/extractor.TestSoundCloudSuitableGuards`
- `internal/extractor.TestSoundCloudTrackMetadataAndTranscodingResolution`
- `internal/extractor.TestSoundCloudUserTrackPagesAreLazyOrderedAndReusable`
- `internal/extractor.TestSoundCloudSetEntriesRemainOrderedTransparentURLs`
- `internal/extractor.TestSoundCloudCancellationInterruptsLazyPage`
- `internal/extractor.TestSoundCloudCategorizedFailuresAndSecretRedaction`
- `internal/extractor.TestSoundCloudRejectsUntrustedContinuationAndAsset`
- `internal/extractor.TestSoundCloudStationResolveAndPlaylistMetadata`
- `internal/extractor.TestSoundCloudStationLazyMultiPageOrdering`
- `internal/extractor.TestSoundCloudRecommendedTrackEntries`
- `internal/extractor.TestSoundCloudAlbumsPlaylistEntries`
- `internal/extractor.TestSoundCloudSetsPlaylistEntries`
- `internal/extractor.TestSoundCloudMixedCollectionDecoding`
- `internal/extractor.TestSoundCloudRepeatedCursorHandling`
- `internal/extractor.TestSoundCloudOversizedPageRejection`
- `internal/extractor.TestSoundCloudMalformedStationIdentifier`
- `internal/extractor.TestSoundCloudMalformedResolvedTrack`
- `internal/extractor.TestSoundCloudCancellationDuringStationPage`
- `internal/extractor.TestSoundCloudCategorizedStationFailures`
- `internal/extractor.TestSoundCloudSecretRedactionInStationErrors`
- `internal/extractor.TestSoundCloudContinuationPolicyAcceptsValidCursors`
- `internal/extractor.TestSoundCloudContinuationQueryBounds`
- `internal/extractor.FuzzSoundCloudURLClassification`
- `internal/extractor.FuzzSoundCloudPageEntries`
- `internal/extractor.FuzzSoundCloudContinuationPolicy`
- `conformance/extractors/soundcloud/PROVENANCE.md`

## Integration hook

Register `extractor.NewSoundCloud()` before `extractor.NewGeneric()` in the
product registry. It can follow the other platform-specific extractors; its
strict URL guards avoid overlap with YouTube, Vimeo, Twitch, and fixture URLs.
Capability status should be raised only after the primary integrator adds this
registry evidence and the complete test suite passes.

## Known deviations

The pilot does not yet implement OAuth/cookie login, original downloadable-file
resolution, premium subscription formats, comments, search, arbitrary user
resource tabs, offset pagination compatibility, full artwork-size expansion, or
batch hydration of incomplete private-set tracks. SoundCloud embed support,
general SoundCloud search pseudo-URLs, and arbitrary user tabs are also out of
scope. Only the declared synthetic corpus is compatibility evidence. SoundCloud
can change its web client-ID layout; failure remains explicit and bounded rather
than relying on a pinned runtime credential.

Production and tests use no Python executable, package, library, or reference
checkout. The pinned reference is used only to attribute fixture semantics.
