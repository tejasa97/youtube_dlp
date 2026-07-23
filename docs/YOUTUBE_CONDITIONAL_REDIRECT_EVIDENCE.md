# YouTube conditional regional channel redirect evidence

Status: compatible for bounded public channel, handle, and legacy-alias
routes.

## Behavior

YouTube may return a non-HTTP regional redirect in
`onResponseReceivedActions[].navigateAction` rather than the requested tab
content. Channel-family extractors now recognize that response before
selected-tab or renderer parsing and return a native URL result to the product
registry.

The response must name a bare `/channel/<UCID>`, `/@handle`, `/user/<alias>`,
or `/c/<alias>` destination on an exact standard YouTube host. An explicit
source tab is appended to that validated root; a bare source remains bare so
the destination performs normal all-uploads aggregation. Routing-only source
queries are not forwarded.

Parsing is bounded to 128 top-level response actions and 4,096 URL bytes.
Duplicate identical actions are accepted. Conflicting destinations, embedded
credentials, explicit ports, foreign or lookalike hosts, queries, fragments,
encoded paths, already-tabbed destinations, unsupported route families,
controls, invalid UTF-8, and self-redirects fail as invalid metadata. Context
cancellation remains terminal before any page read.

## Provenance

The behavior derives from the read-only reference
`yt-dlp/yt-dlp@aefce1eea4d0b6bab1ec2bd3beff09bff91a39c8`,
`YoutubeTabIE._real_extract` at
`yt_dlp/extractor/youtube/_tab.py:2294-2304`. The attributable synthetic
fixture record is in
`conformance/extractors/youtube_channel_redirect/PROVENANCE.md`.

Production, build, and tests do not import or execute Python or the reference
checkout.

## Automated evidence

- `internal/extractor.TestYouTubeConditionalChannelRedirectIntegratesEveryRouteFamily`
- `internal/extractor.TestYouTubeConditionalChannelRedirectCancellationBeforeRead`
- `internal/extractor.TestYouTubeConditionalChannelRedirectDestinationFamiliesAndDuplicates`
- `internal/extractor.TestYouTubeConditionalChannelRedirectRejectsHostileAmbiguousAndSelfTargets`
- `internal/extractor.FuzzYouTubeConditionalChannelRedirect`

## Known deviations

- There is no compatibility option to suppress this redirect independently.
- Conditional destinations outside the registered public channel route
  families are rejected rather than guessed.
- Region-dependent live-site behavior requires the separately controlled
  canary framework and is not inferred from deterministic fixtures.
