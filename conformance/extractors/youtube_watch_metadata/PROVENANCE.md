# YouTube watch-page metadata fixture

This corpus is a synthetic, offline fixture for single-video watch-page
metadata enrichment (watch-enrichment wave). It exercises the bounded
`ytInitialData` parser and the shared availability normalizer introduced
alongside it. The fixture supplies only structural data needed for the new
normalized fields: structured and description-based chapters, the bounded
heatmap, like/dislike counts, the approximate watch-page comment count,
subscriber count, channel verification, concurrent live viewers,
series/season/episode/location, the watch-page date fallback, and the
public/premium/subscriber-only availability states.

The watch page intentionally avoids the engagement-panel `macroMarkersListRenderer`
and renderer-family sources outside the corpus; those branches are covered by
the dedicated chapter and bounds tests, not by this expected document.

The field semantics follow the pinned reference
`yt-dlp/yt-dlp@aefce1eea4d0b6bab1ec2bd3beff09bff91a39c8`:

- watch-page `ytInitialData` traversal bounds:
  `yt_dlp/extractor/youtube/_video.py:2293-2308`;
- structured chapters from the player overlay
  (`chapteredPlayerBarRenderer.chapters`) and engagement panels
  (`macroMarkersListRenderer.contents`): `yt_dlp/extractor/youtube/_video.py:2329-2344`;
- description-based chapters fallback (`MM:SS Title` markers):
  `yt_dlp/extractor/youtube/_video.py:2350-2353`;
- heatmap from `frameworkUpdates.entityBatchUpdate.mutations`
  (`MARKER_TYPE_HEATMAP`, bounded): `yt_dlp/extractor/youtube/_video.py:2357`;
- approximate comment count from the entry-point header:
  `yt_dlp/extractor/youtube/_video.py:4370-4373`;
- like and dislike counts (legacy and modern view-models, accessibility
  labels): `yt_dlp/extractor/youtube/_video.py:4428-4452`;
- subscriber count from `videoSecondaryInfoRenderer.owner.videoOwnerRenderer`:
  `yt_dlp/extractor/youtube/_video.py:4468-4480`;
- verification and channel metadata badges (owner channel badges):
  `yt_dlp/extractor/youtube/_video.py:4557-4571`;
- concurrent live viewers from the live view-count renderer:
  `yt_dlp/extractor/youtube/_video.py:4480-4488`;
- `superTitleLink` series/season/episode/location:
  `yt_dlp/extractor/youtube/_video.py:4455-4466`;
- upload-date fallback from `videoPrimaryInfoRenderer.dateText`:
  `yt_dlp/extractor/youtube/_video.py:4526-4537`;
- availability precedence (`private` > `premium` > `subscriber_only` >
  `needs_auth` > `unlisted` > `public`), shared between the renderer and the
  watch-page adapters: `yt_dlp/extractor/youtube/_video.py:4555-4571`.

All identifiers, metadata, dates, subscriber text, and counts are artificial;
no production response, cookie, token, signed URL, or account data is
retained. The video ID `fixture0003` is reserved for this surface and never
passes through the 11-character pilot validator as a live URL. The channel ID
`UCfixture000000000000000` satisfies the exact public UCID grammar so
`channel_url` emission is exercised in the same way as the player-metadata
fixture.

The expected document is intentionally checked in so field presence,
chapters/heatmap ordering, and the precedence-driven availability claim remain
reviewable. The watch page contains a plain-URL player response with no
signature or `n` challenges, so the pinned extraction performs exactly one
transport read.

## Design notes

- The shared lower-level precedence normalizer
  (`youtubeAvailabilityPrecedence`) feeds both the renderer-specific adapter
  (`youtubeRendererAvailability`) and the watch-page adapter
  (`youtubeMergedAvailability`). The watch-page signal is always merged with
  the player signal so an attributable watch-page badge cannot be silently
  lost.
- The comment-count contract preserves the approximate watch-page count
  after deferred comment retrieval, matching the pinned reference. When the
  watch page carried no approximate count, the bounded retrieved count is
  retained as a documented fallback.
- Optional metadata failures omit the affected field. Structural limits
  prevent partial chapters, partial heatmaps, and partial availability
  claims: any failure mid-traversal either produces a complete and well-formed
  set or none at all.
