# YouTube watch-enrichment evidence

Status: compatible for the bounded single-video watch-page metadata corpus
described below. This wave extends the single-video extraction with
watch-page (`ytInitialData`) fields via a reusable bounded parser and a shared
availability normalizer. No new network requests are introduced.

## User-visible behavior

When a video's watch page carries attributable watch-page metadata, the
extraction now also emits:

- `chapters` (first non-empty of: structured player overlay, engagement-panel
  macro markers, description-based fallback); normalized to sorted,
  duration-clamped entries with a cap of `youtubeMaxChapterItems`, the
  trailing entry closed against the video duration, and the next entry's
  start time;
- `heatmap` from `frameworkUpdates.entityBatchUpdate.mutations`
  (`MARKER_TYPE_HEATMAP`); bounded and out-of-range entries are omitted;
- `like_count` and `dislike_count` from the legacy button toggle, the modern
  segmented like/dislike view-model, and the accessibility-label regex
  fallback;
- `comment_count` (approximate) from the entry-point header;
- `channel_follower_count` from the subscriber-count text (grouped
  integers and `k`/`m`/`b`/`kk` suffixes with allowlisted trailing nouns);
- `channel_is_verified` from the owner channel's Verified badge;
- `concurrent_view_count` from the live view-count renderer;
- `series`, `season_number`, `episode_number`, `location` from
  `superTitleLink`;
- `upload_date` fallback from the watch-page `dateText` (only when the
  player response carried no upload date at all);
- `availability` completed to `public`, `premium`, or `subscriber_only` via
  the shared lower-level precedence normalizer.

## Behavioral provenance

The bounded watch-page traversal and the per-renderer adapters follow the
pinned reference (`yt-dlp/yt-dlp@aefce1eea4d0b6bab1ec2bd3beff09bff91a39c8`):

- watch-page `ytInitialData` traversal bounds (`youtubeMaxJSONDepth`,
  `youtubeMaxJSONNodes`): `_video.py:2293-2308`;
- structured chapters and engagement-panel macro markers:
  `_video.py:2329-2344`;
- description-based chapters fallback: `_video.py:2350-2353`;
- heatmap bounds and `MARKER_TYPE_HEATMAP` filtering: `_video.py:2357`;
- approximate comment count from the entry-point header:
  `_video.py:4370-4373`;
- like and dislike count sources and accessibility-label regex:
  `_video.py:4428-4452`;
- subscriber count parsing and channel metadata badges:
  `_video.py:4468-4480` and `_video.py:4557-4571`;
- concurrent live viewers and `superTitleLink` series/season/episode/
  location: `_video.py:4480-4488` and `_video.py:4455-4466`;
- watch-page `dateText` upload-date fallback: `_video.py:4526-4537`;
- availability precedence (`private` > `premium` > `subscriber_only` >
  `needs_auth` > `unlisted` > `public`): `_video.py:4555-4571`.

## Design notes

- The shared lower-level precedence normalizer
  (`youtubeAvailabilityPrecedence`) feeds both the renderer-specific adapter
  (`youtubeRendererAvailability`) and the watch-page adapter
  (`youtubeMergedAvailability`). The renderer and watch-page signals never
  short-circuit one another; the merged claim is order-independent.
- The comment-count contract preserves the approximate watch-page count
  after deferred comment retrieval, matching the pinned reference. When the
  watch page carried no approximate count, the bounded retrieved count is
  retained as a documented fallback.
- Optional metadata failures omit the affected field. Structural limits
  prevent partial chapters, partial heatmaps, and partial availability
  claims: any failure mid-traversal either produces a complete and well-formed
  set or none at all.

## Coverage

- `conformance/extractors/youtube_watch_metadata/PROVENANCE.md` documents the
  fixture.
- `internal/extractor/youtube_watch_metadata_test.go` exercises pinned
  extraction, structured chapters, description-based chapters fallback, like
  and dislike counts, heatmap bounds, availability precedence, and the
  comment-count contract.

## Explicit deferrals

- Live-chat subtitle injection and the `youtube_live_chat` protocol remain
  pending.
- Authenticated clients beyond existing boundaries remain pending.
- Unknown renderer families are not claimed.
- Music auto-generated description metadata remains pending (deferred from
  the player-response wave).
