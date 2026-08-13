# SoundCloud extractor evidence

This lane implements a Python-free representative API and playlist extractor.
The shared registry and parity manifest remain owned by the primary integrator.

## Supported pilot behavior

- Strict matching for public SoundCloud track URLs, private `s-*` track links,
  public set URLs, direct API track/playlist IDs, bare public user profiles,
  and the pinned `/tracks`, `/albums`, `/sets`, `/reposts`, `/likes`,
  `/spotlight`, and `/comments` profile collections. Other profile resources
  and non-SoundCloud hosts are not claimed.
- Public `w.soundcloud.com`, `player.soundcloud.com`, `p.soundcloud.com`, and
  exact apex `soundcloud.com/player` URLs unwrap to a canonical native
  SoundCloud target without a network request. Declared iframe/embed/object,
  Twitter player, and JSON-LD `embedUrl` candidates use the same bounded
  parser; an outer `s-*` token overrides the inner token.
- The pinned legacy API user permalink
  `https://api.soundcloud.com/users/<positive-numeric-id>` resolves that exact
  canonical URL, requires the resolved identity to match, and lazily enumerates
  `users/<id>/tracks`. Its playlist ID is the resolved numeric ID and its title
  is the resolved username without a profile-tab suffix.
- Track stations: `soundcloud.com/stations/track/<artist>/<track>` resolved
  through the v2 resolve endpoint. The opaque station identifier
  `soundcloud:track-stations:<positive-numeric-id>` is validated before any API
  path is constructed. Station tracks are fetched from
  `stations/<station-id>/tracks` with linked partitioning and lazy ordered
  transparent entries. Playlist metadata: ID = numeric track ID, title =
  `Track station: <resolved title>`.
- Related-resource pages: `soundcloud.com/<artist>/<track>/recommended`,
  `/albums`, and `/sets`. The base track URL (without the relation suffix) is
  resolved, the track ID is validated, and relation-specific API
  routes are used: `tracks/<id>/related`, `tracks/<id>/albums`, and
  `tracks/<id>/playlists_without_albums`. Playlist metadata: ID = resolved
  numeric track ID, title = `<track title> (Recommended|Albums|Sets)`,
  webpage_url = original canonical relation URL. When the resolved track title
  is blank, the title falls back to the URL slug (`artist/track`), matching the
  reference `track.get('title') or slug` behavior. Remote `errors[].error_message`
  content is never exposed in public diagnostics; a generic
  `ErrUnavailable: SoundCloud related resource unavailable` is returned instead.
- Bounded client-ID discovery from the SoundCloud homepage and at most 64
  first-party `soundcloud.com`/`sndcdn.com` script candidates. The identifier is
  cached per extractor instance and refreshed once after API 401/403 responses.
- Bounded v2 API metadata requests and per-transcoding URL resolution, including
  progressive HTTP, native HLS, encrypted-HLS labeling, codec/extension/bitrate
  normalization, preview labeling, URL de-duplication, broken ABR rejection,
  and explicit DRM/block handling.
- Original downloadable files for tracks marked `downloadable` with downloads
  remaining. The optional API response preserves private tokens, 401/403 falls
  back to streaming formats, 429 propagates as a rate-limited network error,
  and a bounded
  credential-isolated HEAD chain validates the final URL, extension, and size.
- Response cardinalities are capped at 64 transcodings, 200 linked-partition
  entries per page, and 10,000 set entries; URLs, slugs, tokens, assets, and
  JSON bodies also have explicit limits.
- Deterministic normalized track metadata for identifiers, title/track,
  uploader, duration, timestamps, counts, license, genre, artwork, webpage URL,
  and audio-only formats.
- Ordered artwork/avatar thumbnail matrices for tracks and sets: `mini`,
  `tiny`, `small`, `badge`, `t67x67`, `large`, `t300x300`, `crop`,
  `t500x500`, and preferred `original`. Artwork wins over avatar; avatar
  `tiny` is 18x18; all non-original variants use JPEG. Original-extension
  checks are optional, credential-isolated, redirect-disabled, and nonfatal;
  the singular thumbnail resolves to the final preferred variant.
- Opt-in public track-comment retrieval through `--get-comments` or
  `--write-comments`, with `--soundcloud-comment-sort` and
  `--soundcloud-max-comments` controls. Retrieval remains deferred until media
  enrichment, preserves API order, normalizes author and time metadata, uses a
  bounded exact-track continuation policy, and requires credential-isolated,
  no-redirect requests.
- Ordered transparent URL entries for sets/API playlists. Tokenized sets with
  incomplete rows are hydrated through bounded v2 `/tracks` batches before
  entry construction. Returned rows are identity-validated and restored to
  source order, repeated source IDs remain repeated, and missing rows retain a
  direct v2 track fallback with the private-set token.
- Lazy, independently reusable linked-partition iterators for bare public user
  profiles and every pinned user tab, track stations, and related-resource
  pages. Their initial collection request uses the pinned `limit=200`,
  `linked_partitioning=1`, and `offset=0` contract; service-provided
  continuations do not have the local offset reintroduced. Paged collection API
  requests use the fixed `chrome-133` impersonation profile when the transport
  supports `ProfileTransport`, fall back once to the native transport when
  impersonation is unavailable, and retry only transient HTTP 502 responses
  with one initial request plus three retries (four total page attempts per
  collection page). Resolve, client-ID discovery, track metadata, transcoding,
  original-download, comment, and search requests keep their existing native or
  established transport policies.
- Route-aware continuation policy: every `next_href` must use HTTPS, the exact
  `api-v2.soundcloud.com` host, no userinfo, no explicit port, no fragment, no
  encoded separators (`%2f`, `%5c`, `%00`) or NULs, no literal `.` or `..` path
  segments, no trailing slash, and a decoded path that exactly equals the
  allowed path (no `path.Clean` normalization), preventing cross-user,
  cross-track, cross-station, and cross-relation transitions. Query parsing
  uses `url.ParseQuery` explicitly to reject malformed percent-escaping and
  invalid semicolon syntax. Query cardinality and per-value length are bounded;
  stale `client_id` keys are stripped. Repeated cursors terminate safely.
- Mixed track/playlist collection decoding: collection items are resolved using
  the reference `resolve_entry(e, e.get('track'), e.get('playlist'))` ordering.
  The direct collection item is classified by its explicit `kind` field and/or
  permalink URL kind: direct `playlist` objects produce set entries (or
  `/playlists/<id>` fallback when the permalink is absent or unusable); direct
  `track` objects produce track entries (or `/tracks/<id>` fallback). Unknown
  or contradictory kind/permalink combinations fail closed unless the permalink
  independently provides an unambiguous supported type. Nested track and playlist
  candidates follow. Playlist entries without a classifiable permalink fall
  back to the v2 API playlist URL.
- HTTP authentication/unavailability, malformed metadata/playlists/station
  identifiers, oversized bodies/pages, invalid continuations, related `errors`
  fields, and missing formats have categorized errors. Request/response bodies
  and secret tokens are absent from diagnostics. Cancellation is observable
  through `errors.Is` with `context.Canceled` or `context.DeadlineExceeded`.

## Automated evidence

- `internal/extractor.TestSoundCloudSuitableGuards`
- `internal/extractor.TestSoundCloudTrackMetadataAndTranscodingResolution`
- `internal/extractor.TestSoundCloudUserTrackPagesAreLazyOrderedAndReusable`
- `internal/extractor.TestSoundCloudAllProfileTabsUsePinnedEndpoints`
- `internal/extractor.TestSoundCloudProfileTabContinuationCannotPivot`
- `internal/extractor.TestSoundCloudAPIUserPermalinkIsLazyOrderedAndReusable`
- `internal/extractor.TestSoundCloudAPIUserPermalinkRejectsUnsafeRoutesWithoutRequests`
- `internal/extractor.TestSoundCloudAPIUserPermalinkRejectsMismatchedResolve`
- `internal/extractor.TestSoundCloudAPIUserPermalinkComparesNumericIdentity`
- `internal/extractor.TestSoundCloudAPIUserPermalinkCancellationAndContinuationIsolation`
- `internal/extractor.TestSoundCloudSetEntriesRemainOrderedTransparentURLs`
- `internal/extractor.TestSoundCloudCollectionStartsWithPinnedOffsetContract`
- `internal/extractor.TestSoundCloudCollectionContinuationDoesNotReintroduceOffset`
- `internal/extractor.TestSoundCloudCollectionPreservesServerContinuationOffset`
- `internal/extractor.TestSoundCloudCollectionStartURLIsDeterministicAndBounded`
- `internal/extractor.TestSoundCloudPrivateSetHydratesWebAndAPIURLs`
- `internal/extractor.TestSoundCloudPrivateSetTriggerAndMissingRowFallback`
- `internal/extractor.TestSoundCloudPrivateSetHydrationFailuresAreCategorizedAndSecretSafe`
- `internal/extractor.TestSoundCloudPrivateSetRejectsMalformedSourceBeforeBatch`
- `internal/extractor.TestSoundCloudPrivateSetBatchingOrderAndCancellation`
- `internal/extractor.FuzzSoundCloudPrivateSetBatchPlan`
- `internal/extractor.TestSoundCloudArtworkThumbnailMatrix`
- `internal/extractor.TestSoundCloudArtworkOriginalFallbackAndAvatarDimensions`
- `internal/extractor.TestSoundCloudArtworkNonmatchingAndUnsafeSources`
- `internal/extractor.TestSoundCloudArtworkCancellation`
- `internal/extractor.TestSoundCloudArtworkTrackAndPlaylistIntegration`
- `internal/extractor.FuzzSoundCloudArtworkPlan`
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
- `internal/extractor.TestSoundCloudDirectCollectionEntryClassification`
- `internal/extractor.TestSoundCloudMalformedDirectCandidateFallsThroughToNested`
- `internal/extractor.TestSoundCloudNestedTrackAndPlaylistEntries`
- `internal/extractor.TestSoundCloudRelatedErrorSecretSafety`
- `internal/extractor.TestSoundCloudContinuationExactPathComparison`
- `internal/extractor.TestSoundCloudContinuationMalformedQueryEscaping`
- `internal/extractor.TestSoundCloudRelatedSlugFallback`
- `internal/extractor.TestSoundCloudRelatedSlugFallbackAlbumsAndSets`
- `internal/extractor.TestSoundCloudPagedCollectionProfileSelection`
- `internal/extractor.TestSoundCloudPagedCollectionMissingProfileCapability`
- `internal/extractor.TestSoundCloudPagedCollectionUnavailableProfileFallsBackOnce`
- `internal/extractor.TestSoundCloudPagedCollection502Recovery`
- `internal/extractor.TestSoundCloudPagedCollection502RetriesExhausted`
- `internal/extractor.TestSoundCloudPagedCollectionNonRetryFailures`
- `internal/extractor.TestSoundCloudPagedCollectionCancellation`
- `internal/extractor.TestSoundCloudPagedCollectionIteratorSafety`
- `internal/extractor.TestSoundCloudPagedCollectionRegression`
- `internal/extractor.FuzzSoundCloudURLClassification`
- `internal/extractor.FuzzSoundCloudPageEntries`
- `internal/extractor.FuzzSoundCloudContinuationPolicy`
- `internal/extractor.TestSoundCloudEmbedRoutesAndCanonicalization`
- `internal/extractor.TestSoundCloudEmbedRejectsUnsafeAndAmbiguousURLs`
- `internal/extractor.TestGenericSoundCloudEmbedDiscoveryAndDeduplication`
- `internal/extractor.FuzzParseSoundCloudEmbedURL`
- `internal/extractor.TestSoundCloudTrackCommentsAreDeferredOrderedAndNormalized`
- `internal/extractor.TestSoundCloudTrackCommentSortLimitsAndDisabled`
- `internal/extractor.TestSoundCloudCommentIsolationFailuresAndCancellation`
- `internal/extractor.TestSoundCloudCommentContinuationPolicy`
- `internal/extractor.FuzzSoundCloudCommentContinuationPolicy`
- `internal/extractor.FuzzNormalizeSoundCloudComment`
- `internal/extractor.TestSoundCloudOriginalDownloadIsFirstAndBounded`
- `internal/extractor.TestSoundCloudOriginalDownloadFlagsAndDeduplication`
- `internal/extractor.TestSoundCloudOriginalDownloadAPIFailures`
- `internal/extractor.TestSoundCloudOriginalRedirectSecurityAndFailures`
- `internal/extractor.TestSoundCloudOriginalExtensionPrecedence`
- `internal/extractor.TestSoundCloudOriginalOptionalHeadFailuresKeepStreamingFormats`
- `internal/extractor.TestSoundCloudOriginalUnknownExtensionIsRetained`
- `internal/extractor.TestSoundCloudOriginalRelativeMultiHopRedirect`
- `internal/extractor.TestSoundCloudOriginalPreservesSignedPathEncoding`
- `internal/extractor.FuzzSoundCloudOriginalURL`
- `internal/extractor.FuzzSoundCloudOriginalExtension`
- `engine.TestProductRegistryReentersSoundCloudEmbedIntoMedia`
- `engine.TestProductSoundCloudCommentOptionsPropagateAndEnrich`
- `conformance/extractors/soundcloud/PROVENANCE.md`
- `conformance/extractors/soundcloud/comments/PROVENANCE.md`
- `conformance/extractors/soundcloud/download/PROVENANCE.md`
- `conformance/extractors/soundcloud_embed/PROVENANCE.md`

## Integration hook

Register `extractor.NewSoundCloudEmbed()` and `extractor.NewSoundCloud()` before
`extractor.NewGeneric()` in the product registry. They can follow the other
platform-specific extractors; strict URL guards avoid overlap with YouTube,
Vimeo, Twitch, and fixture URLs.
Capability status should be raised only after the primary integrator adds this
registry evidence and the complete test suite passes.

## Known deviations

The pilot does not yet implement OAuth/cookie login or premium subscription
formats. Track comments are supported, while the distinct `/comments` user tab
continues to enumerate attributable media entries rather than comment bodies.
Arbitrary script-based player discovery and additional user tabs remain out of
scope. Only the declared synthetic corpus is compatibility evidence. SoundCloud
can change its web client-ID layout; failure remains explicit and bounded rather
than relying on a pinned runtime credential.

Production and tests use no Python executable, package, library, or reference
checkout. The pinned reference is used only to attribute fixture semantics.
