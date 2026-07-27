# Aeon.co extractor evidence

## Scope

The `aeonco` adapter accepts only exact HTTPS
`aeon.co/videos/{slug}` and `www.aeon.co/videos/{slug}` URLs. It:

- reads a bounded page through the shared transport;
- reuses the generic HTML tokenizer and JSON-LD decoder rather than parsing JSON
  with regular expressions or executing JavaScript;
- chooses the first JSON-LD VideoObject candidate whose authoritative source is
  an `embedUrl` routable to the existing Vimeo or YouTube extractor;
- emits a transparent URL result; and
- sets the bounded child-entry `Referer` to `https://aeon.co/` for Vimeo only.
  Product recursion passes that value through `extractor.Request.Referer`, and
  Vimeo validates it before using it for the player-config request.

## Routing and hostile-input policy

Routing rejects lookalike and trailing-dot hosts, HTTP and non-HTTP schemes,
userinfo, explicit ports, query strings, fragments, duplicate or extra path
segments, malformed slugs, encoded separators, double-encoded separators, and
encoded NULs before network access.

JSON-LD embed candidates still pass the generic canonical embed router. Aeon
then narrows successful handoffs to Vimeo and YouTube, so JavaScript URLs,
userinfo-bearing URLs, arbitrary hosts, and other otherwise-supported generic
providers cannot escape the adapter boundary.

## Failure classes

| Condition | Extractor error | Product category |
| --- | --- | --- |
| Missing, hostile, or unsupported embed | `ErrUnavailable` | `ErrorUnsupported` |
| Oversized response, token/depth/node violation, or malformed bounded page | `ErrInvalidMetadata` | `ErrorInternal` |
| JSON-LD script/candidate cardinality violation | `ErrPlaylistLimit` | `ErrorInternal` |
| Cancellation | `context.Canceled` | `ErrorCancelled` |

Errors use static context and never include an untrusted embed URL or page body.

## Deterministic evidence

- `TestAeonCoHandoffVimeoPreservesReferer`
- `TestAeonCoHandoffYouTube`
- `TestAeonCoMultipleMalformedJSONLDBlocksPickFirstSupportedVideoEmbed`
- `TestAeonCoMissingAndHostileEmbedsAreUnavailableAndSecretSafe`
- `TestAeonCoRouting`
- `TestAeonCoCancellationAvoidsNetworkAccess`
- `TestAeonCoPageBoundsAndMalformedPageErrors`
- `TestAeonCoVimeoReentryUsesAeonReferer`
- `FuzzAeonCoRouting`
- `FuzzAeonCoJSONLDHandoff`
- `TestProductRegistryIncludesIntegratedExtractors`
- `TestExtractorFailuresAreCategorized`

Fixture provenance is recorded in
`conformance/extractors/shared/aeonco/PROVENANCE.md` against pinned yt-dlp commit
`aefce1eea4d0b6bab1ec2bd3beff09bff91a39c8`.
