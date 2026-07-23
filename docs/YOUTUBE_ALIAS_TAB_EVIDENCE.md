# YouTube legacy alias-tab evidence

Status: compatible for bounded public `/user` and `/c` tab and bare-root
routes.

## Behavior

The `youtube_alias_tab` extractor handles exact `youtube.com` and
`www.youtube.com` URLs shaped as:

- `/user/<alias>/{videos,shorts,streams,playlists,home,featured,community,releases,podcasts}`
- `/c/<alias>/{videos,shorts,streams,playlists,home,featured,community,releases,podcasts}`
- bare `/user/<alias>` and `/c/<alias>` upload roots

Aliases retain their case and valid Unicode spelling. The route classifier
rejects ambiguous encoded paths, controls, oversized aliases, alternate
hosts, userinfo, explicit ports, fragments, and extra path components. Query
parameters are accepted for routing but removed from the canonical page
request.

Initial pages and bounded browse continuations reuse the existing ordered
YouTube tab renderer model. Playlist identity prefers a valid metadata UCID
and otherwise uses a stable typed alias identity. Requested-tab mismatches
fail closed when the page exposes decisive selected-tab metadata. A bare root
with no advertised upload tabs and a valid metadata UCID tries the equivalent
synthesized uploads playlist.

## Provenance

The contract derives from `YoutubeTabIE._VALID_URL`, `_URL_RE`,
`_real_extract`, and `_extract_metadata_from_tabs` in the read-only reference
`yt-dlp/yt-dlp` pinned at
`aefce1eea4d0b6bab1ec2bd3beff09bff91a39c8`.

All pages, renderer trees, continuations, visitor values, and aliases used by
tests are synthetic. Production and tests do not import or execute Python and
do not depend on the reference checkout.

## Automated evidence

- `internal/extractor.TestYouTubeAliasTabUserVideosCanonicalContinuationAndUCID`
- `internal/extractor.TestYouTubeAliasTabUnicodeCPlaylistsFallbackAndRenderers`
- `internal/extractor.TestYouTubeAliasTabTargetPolicy`
- `internal/extractor.TestYouTubeAliasTabCanonicalURLPercentSafety`
- `internal/extractor.TestYouTubeAliasTabPercentAliasFetchIsEncodedOnce`
- `internal/extractor.TestYouTubeAliasTabSelectedIdentityAndMetadataFailures`
- `internal/extractor.TestYouTubeAliasTabTraversalBounds`
- `internal/extractor.TestYouTubeAliasTabCategorizedFailuresAndCancellation`
- `internal/extractor.TestYouTubeAliasTabContinuationRateLimitAndReusableRace`
- `internal/extractor.FuzzYouTubeAliasTabTarget`
- expanded mixed/community/release evidence in
  `docs/YOUTUBE_EXPANDED_TABS_EVIDENCE.md`
- `pkg/ytdlp.TestProductRegistryIncludesIntegratedExtractors`

## Known deviations

- Membership, arbitrary custom tabs, and channel search remain outside this
  bounded extractor.
- Conditional redirects, renamed aliases, authenticated/private tabs, and
  arbitrary renderer parity are not claimed.
- Alias spelling is preserved rather than guessed or normalized beyond URL
  parsing and the documented security policy.
