# YouTube bare-channel upload aggregation evidence

Status: compatible for bounded public bare channel, Unicode-handle, and legacy
alias roots.

## Behavior

The registered tab extractors now claim these exact root forms:

- `/channel/<UCID>`
- `/@handle`
- `/user/<alias>`
- `/c/<alias>`

A root request discovers the channel's upload-bearing tabs from its videos
page and returns one lazy playlist ordered as videos, streams, then Shorts,
matching the pinned `YoutubeTabIE` policy. Each advertised tab retains its
existing renderer, cursor, visitor-rotation, retry, error, and cancellation
behavior. The initial videos page is reused without a second fetch; streams
and Shorts pages are loaded only when iteration reaches them. Independent
iterators own independent continuation and extra-tab state.

If a channel has streams or Shorts but no videos tab, those advertised tabs
are still returned and home-page shelf entries are excluded. If no upload tab
is advertised and a valid UCID is present, the extractor tries the equivalent
`UU<channel-suffix>` uploads playlist. A missing derived playlist produces a
valid empty channel playlist. A missing `/videos` page falls back once to the
exact root for tab discovery. The combined iterator enforces the shared
100,000-entry bound across all tabs.

Root URLs preserve validated channel, handle, or alias spelling, remove
routing-only queries, and use metadata UCIDs when available. All existing
scheme, host, userinfo, port, fragment, encoded-separator, NUL, Unicode, and
length policies remain in force.

## Provenance

The behavior derives from the read-only reference
`yt-dlp/yt-dlp@aefce1eea4d0b6bab1ec2bd3beff09bff91a39c8`,
`YoutubeTabIE._real_extract` in
`yt_dlp/extractor/youtube/_tab.py:2267-2350`. Fixture-level derivation is
recorded in
`conformance/extractors/youtube_channel_root/PROVENANCE.md`.

All pages, IDs, visitor values, API keys, and cursors are synthetic. The
reference checkout is never imported or executed by production, build, or
test paths.

## Automated evidence

- `internal/extractor.TestYouTubeBareRootsAggregateVideosStreamsAndShortsLazily`
- `internal/extractor.TestYouTubeBareRootNoVideosExcludesHomeShelvesAndEmptyRoot`
- `internal/extractor.TestYouTubeBareRootFallsBackToTopicUploadsPlaylist`
- `internal/extractor.TestYouTubeBareTopicFallbackPreservesCancellation`
- `internal/extractor.TestYouTubeBareRootFallsBackAfterMissingVideosPage`
- `internal/extractor.TestYouTubeBareRootSelectedTabValidationBoundsAndCancellation`
- `internal/extractor.TestYouTubeBareRootNetworkAndAlertCategories`
- `internal/extractor.FuzzYouTubeBareUploadTabs`
- existing channel, handle, and alias route fuzz targets
- `pkg/ytdlp.TestProductRegistryIncludesIntegratedExtractors`

## Known deviations

- Members-only uploads, arbitrary custom tabs, conditional regional channel
  redirects, and authenticated/private success remain outside this slice.
- Rich child metadata is delegated to each authoritative video extractor.
