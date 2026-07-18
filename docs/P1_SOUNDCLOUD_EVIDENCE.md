# Phase 1 SoundCloud extractor pilot

This lane implements a Python-free representative API and playlist extractor.
The shared registry and parity manifest remain owned by the primary integrator.

## Supported pilot behavior

- Strict matching for public SoundCloud track URLs, private `s-*` track links,
  public set URLs, direct API track/playlist IDs, and public user `/tracks`
  collections. Ambiguous profile resources and non-SoundCloud hosts are not
  claimed.
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
- Lazy, independently reusable linked-partition iterators for public user tracks.
  `next_href` is restricted to HTTPS `api-v2.soundcloud.com/users/...`, stale
  client IDs are removed, repeated cursors are bounded by the common sequence,
  and transport cancellation is propagated.
- HTTP authentication/unavailability, malformed metadata/playlists, oversized
  bodies, invalid continuations, and missing formats have categorized errors.
  Request/response bodies and secret tokens are absent from diagnostics.

## Automated evidence

- `internal/extractor.TestSoundCloudSuitableGuards`
- `internal/extractor.TestSoundCloudTrackMetadataAndTranscodingResolution`
- `internal/extractor.TestSoundCloudUserTrackPagesAreLazyOrderedAndReusable`
- `internal/extractor.TestSoundCloudSetEntriesRemainOrderedTransparentURLs`
- `internal/extractor.TestSoundCloudCancellationInterruptsLazyPage`
- `internal/extractor.TestSoundCloudCategorizedFailuresAndSecretRedaction`
- `internal/extractor.TestSoundCloudRejectsUntrustedContinuationAndAsset`
- `internal/extractor.FuzzSoundCloudURLClassification`
- `internal/extractor.FuzzSoundCloudPageEntries`
- `conformance/extractors/soundcloud/PROVENANCE.md`

## Integration hook

Register `extractor.NewSoundCloud()` before `extractor.NewGeneric()` in the
product registry. It can follow the other platform-specific extractors; its
strict URL guards avoid overlap with YouTube, Vimeo, Twitch, and fixture URLs.
Capability status should be raised only after the primary integrator adds this
registry evidence and the complete test suite passes.

## Known deviations

The pilot does not yet implement OAuth/cookie login, original downloadable-file
resolution, premium subscription formats, comments, search, stations, related
tracks, arbitrary user resource tabs, offset pagination compatibility, full
artwork-size expansion, or batch hydration of incomplete private-set tracks.
Only the declared synthetic corpus is compatibility evidence. SoundCloud can
change its web client-ID layout; failure remains explicit and bounded rather
than relying on a pinned runtime credential.

Production and tests use no Python executable, package, library, or reference
checkout. The pinned reference is used only to attribute fixture semantics.
