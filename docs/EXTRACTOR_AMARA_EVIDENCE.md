# Amara extractor evidence

Reference baseline: `yt-dlp/yt-dlp@aefce1eea4d0b6bab1ec2bd3beff09bff91a39c8`

## Claimed corpus

- `https://amara.org/videos/<id>` with no trailing path or `/info` plus safe slug
  components only
- `https://amara.org/<language>/videos/<id>` with the same optional `/info`
  suffix rules
- HTTP(S) page inputs are accepted for routing; canonical `webpage_url` metadata
  is always HTTPS
- Public JSON API: `https://amara.org/api/videos/<id>/?format=json`

## Supported behavior

- Title, description, thumbnail, duration, timestamp, and webpage URL from the
  bounded API response
- First validated HTTP(S) media URL from `all_urls`, skipping invalid entries
- Published subtitle languages only, emitting JSON/SRT/VTT in deterministic
  order by appending `format=<type>` to each `subtitles_uri`
- Transparent YouTube and Vimeo handoffs using existing extractor keys while
  preserving Amara metadata through product-level parent overlay merge
- Final merged metadata keeps the downstream YouTube/Vimeo video ID; the Amara
  page ID is not placed in the transparent entry overlay
- Direct validated HTTP(S) media formats when the media URL is not YouTube or
  Vimeo, with protocol matching the URL scheme and bounded extension fallback

## Bounds and rejection policy

- Shared strict URL policy for page routing and outbound media/subtitle URLs
- Rejects userinfo, explicit ports, fragments, lookalike hosts, malformed IDs,
  oversized URLs, encoded separators/NULs, ambiguous language/video paths, and
  arbitrary trailing path segments beyond `/info/<safe-slug>`
- Caps languages slice size, `all_urls`, aggregate subtitle entries (256 total),
  per-language published subtitle duplicates (32), and per-field string sizes
  (title, description, thumbnail, created, media URLs, language codes,
  subtitles_uri)
- Strict JSON nesting depth limit (32) enforced before typed decode on unknown
  fields; bounded production decoder shared with fuzz target; trailing-value
  rejection and 16 MiB response cap
- HTTP status is classified before body consumption so oversized 401/429 bodies
  remain authentication/rate-limit errors rather than JSON-size failures
- HTTP 401/403 authentication, 404/410 unavailable, 429 rate limited, and 5xx
  network failures are categorized without echoing response bodies

## Evidence

- `internal/extractor.TestAmaraRouting`
- `internal/extractor.TestAmaraYouTubeHandoffAndReentry`
- `internal/extractor.TestAmaraVimeoHandoffAndReentry`
- `internal/extractor.TestAmaraDirectMediaExtraction`
- `internal/extractor.TestAmaraDirectHTTPMedia`
- `internal/extractor.TestAmaraSkipsInvalidAllURLsBeforeValid`
- `internal/extractor.TestAmaraOverlongExtensionFallsBack`
- `internal/extractor.TestAmaraPublishedSubtitleLanguages`
- `internal/extractor.TestAmaraDuplicatePublishedLanguagesAggregate`
- `internal/extractor.TestAmaraExcessiveSubtitleEntries`
- `internal/extractor.TestAmaraAggregateSubtitleOverflow`
- `internal/extractor.TestAmaraSubtitleURLConstruction`
- `internal/extractor.TestAmaraSubtitleBaseURIValidation`
- `internal/extractor.TestAmaraEmptyAndHostileMediaURLs`
- `internal/extractor.TestAmaraRejectsUnsafeHostedMediaURLs`
- `internal/extractor.TestAmaraSkipsUnsafeHostedMediaURL`
- `internal/extractor.TestAmaraHTTPStatusCategorization`
- `internal/extractor.TestAmaraOversizedAuthAndRateLimitBodies`
- `internal/extractor.TestAmaraSecretSafeErrors`
- `internal/extractor.TestAmaraCancellation`
- `internal/extractor.TestAmaraMalformedAndOversizedJSON`
- `internal/extractor.TestAmaraDeepJSONNestingRejected`
- `internal/extractor.TestAmaraExcessiveLanguagesAndLongStrings`
- `internal/extractor.TestAmaraStringLengthPolicy`
- `internal/extractor.TestAmaraRegistryOrderingAndIntegration`
- `internal/extractor.TestAmaraConcurrentExtractionSafety`
- `internal/extractor.FuzzParseAmaraURL`
- `internal/extractor.FuzzDecodeAmaraVideoResponse`
- `pkg/ytdlp.TestOperationReentersAmaraYouTubeHandoff`
- `pkg/ytdlp.TestOperationReentersAmaraVimeoHandoff`
- `pkg/ytdlp.TestOperationAmaraHandoffDoesNotLeakMetadataBetweenCalls`
- `pkg/ytdlp.TestOperationAmaraHandoffsAreConcurrentSafe`
- `pkg/ytdlp.TestOperationAmaraParentMetadataDoesNotLeakIntoPlaylistEntries`
- `pkg/ytdlp.TestOperationAmaraNestedTransparentPreservesChildID`
- `pkg/ytdlp.TestOperationMergesTransparentParentInfoFromURLResult`
- `pkg/ytdlp.TestProductRegistryIncludesIntegratedExtractors`
- `conformance/extractors/shared/amara/PROVENANCE.md`

## Known deviations from pinned yt-dlp

- Amara page routing uses the repository strict URL policy rather than the
  looser Python `_VALID_URL` alone; lookalike hosts, ambiguous paths, and
  non-`/info` trailing segments are rejected more aggressively
- Transparent parent metadata is carried as an explicit bounded re-entry value
  in `pkg/ytdlp/client.go` instead of Python's in-process
  `_type=url_transparent` merge during `_real_extract`; child extractor IDs are
  preserved in the final merged info dict
- Direct-media extraction uses bounded extension fallback and HTTP(S) validation
  policy; unsigned or non-canonical media URLs that Python might accept can be
  rejected
- Account pages, uploads, editing flows, and non-video Amara routes are out of
  scope
