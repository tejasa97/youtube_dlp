# YouTube expanded public-tab evidence

Status: compatible for explicit bounded home, featured, community, releases,
and podcasts tabs on channel, Unicode handle, and legacy alias URL families.

## Behavior

The existing `youtube_channel_tab`, `youtube_handle_tab`, and
`youtube_alias_tab` extractors now share one tab-kind policy:

- `videos`, `shorts`, and `streams` admit video entries;
- `playlists`, `releases`, and `podcasts` admit playlist and podcast entries;
- `home`, `featured`, and `community` admit mixed video and playlist entries.

Home and featured tabs consume legacy renderers, rich-grid children, and
modern video/playlist lockups in occurrence order. Releases and podcasts
accept legacy playlist renderers plus playlist and podcast lockups while
rejecting video lockups.

Community posts follow the pinned extractor's ordering: attached video,
attached playlist, then distinct inline YouTube video links. Duplicate
attachment/inline video IDs are suppressed within each post; repeated media
across separate posts retains its occurrence order.
Malformed IDs, non-YouTube links, hostile hosts, and unsupported renderer
families are ignored. Action, endpoint, grid, section-list, and item-section
continuation containers remain lazy, reusable, bounded, cancellable, and
visitor-aware.

When an initial page exposes decisive selected-tab metadata, its identifier,
endpoint, or fallback title must match the requested tab. Conflicting or
mismatched identities fail closed. The title is consulted only when no
identifier or endpoint exists, matching the pinned reference and allowing a
legacy `/featured` tab titled “Home.”

## Provenance

The behavior and synthetic fixtures derive from the read-only reference
`yt-dlp/yt-dlp@aefce1eea4d0b6bab1ec2bd3beff09bff91a39c8`, principally
`YoutubeTabIE._rich_entries`, `_post_thread_entries`,
`_post_thread_continuation_entries`, `_extract_entries`, `_entries`,
`_extract_tab_id_and_name`, and selected-tab handling in
`yt_dlp/extractor/youtube/_tab.py`.

Fixture-level attribution is recorded in
`conformance/extractors/youtube_tab_breadth/PROVENANCE.md`. All identifiers,
payloads, visitor values, and URLs are synthetic. Production and build paths
remain Python-free.

## Automated evidence

- `internal/extractor.TestYouTubeHomeTabMixedRenderersAndContinuation`
- `internal/extractor.TestYouTubeCommunityTabAttachmentsInlineDedupAndContinuation`
- `internal/extractor.TestYouTubeReleasesTabPlaylistOnlyContinuation`
- `internal/extractor.TestYouTubeExpandedTabsIntegrateChannelAndAliasRoutes`
- `internal/extractor.TestYouTubeExpandedTabIdentityAndCommunityLinkPolicy`
- channel, handle, and alias routing/failure/cancellation tests
- `internal/extractor.FuzzYouTubeCommunityPostEntries`
- existing channel/handle parser and route fuzz targets, now covering every
  supported tab kind

## Known deviations

- Bare channel, handle, and legacy alias URLs, membership, arbitrary custom
  tabs, and channel search remain outside this explicit-tab slice.
- Community text, images, polls, post metadata, and non-media links are not
  emitted because yt-dlp's tab extractor yields media URL results from these
  post renderers.
- Rich child metadata remains delegated to the authoritative child extractor.
- Authenticated/private success and arbitrary renderer parity are not claimed.
