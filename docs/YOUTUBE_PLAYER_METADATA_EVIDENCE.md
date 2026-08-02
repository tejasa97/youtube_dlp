# YouTube player-response metadata evidence

Status: compatible for the bounded single-video player-response metadata corpus
described below. This wave enriches every YouTube video using only data already
present in the fetched watch page (player response plus attributable OpenGraph
meta). No additional network requests are made and no downloader protocol or
format-selection behavior changes beyond the pinned `yt:stretch` stretched
ratio.

## User-visible behavior

Single-video extraction now emits, when attributable and valid:

- `channel` and `channel_url` derived from the player response channel ID;
- `uploader_id`/`uploader_url` derived from a strictly validated
  `microformat.ownerProfileUrl` handle (`https://www.youtube.com/@handle`,
  exact path, Unicode-aware handle grammar, no userinfo/ports/encoded
  paths/query/fragment);
- `upload_date`, plus `timestamp` only when the microformat `uploadDate`
  carries time and timezone (RFC 3339); a bare `YYYY-MM-DD` yields the date
  only, and malformed or over-budget values are omitted;
- `age_limit` (`18` when `isFamilySafe` is explicitly `false`, else `0`);
- partial `availability` (`private`, `needs_auth`, or `unlisted` only — the
  public/premium/subscriber-only states need watch-page badge data and remain
  absent until watch-page enrichment lands);
- `categories`, bounded `tags` (from `videoDetails.keywords`), and
  `average_rating` when finite;
- `playable_in_embed` from the playability status and `media_type`
  (`video`/`short`/`livestream` with livestream precedence);
- a complete `thumbnails` collection: player and microformat thumbnails, an
  attributable `og:image`/`twitter:image` (og:image wins, entities unescaped,
  hostile URLs rejected, scan bounded to the page head region), then the
  deterministic `i.ytimg.com` JPG/WebP ladder with the pinned preference
  formula and first-occurrence URL deduplication; `thumbnail` remains the best
  known original thumbnail;
- `stretched_ratio` on every format whose `vcodec` is not `none` when a valid
  `yt:stretch=W:H` keyword is present (first valid match wins, both dimensions
  positive).

Existing duration, live status, view count, and release timestamp behavior is
unchanged. Optional metadata failures omit the affected field rather than
failing extraction or emitting partial positive claims.

## Behavioral provenance

The contract is derived from the pinned read-only reference
`yt-dlp/yt-dlp@aefce1eea4d0b6bab1ec2bd3beff09bff91a39c8`, specifically
`YoutubeIE._real_extract` in `yt_dlp/extractor/youtube/_video.py`:

- metadata merge and microformat consumption: lines 3941-3954;
- keywords and `yt:stretch` handling: lines 4065-4081;
- thumbnail ladder, preferences, and deduplication: lines 4088-4121;
- channel/category/owner identity and info assembly: lines 4112-4183;
- `handle_from_url` handle grammar: `yt_dlp/extractor/youtube/_base.py:613-616`;
- uploader identity: `_video.py:4509-4511`;
- UTC upload date and attributable timestamp: `_video.py:4526-4537`;
- availability precedence: `_video.py:4555-4571`.

All fixture data is synthetic. No test or production binary imports or
executes Python or reads the reference checkout.

## Known deviations from the pinned reference

- The pinned `og:restrictions:age` fallback for `age_limit` is not
  implemented; only `microformat.isFamilySafe` drives the age limit.
- `uploader_id` requires an exact `/@handle` path; the pinned prefix match
  accepts trailing path/query noise.
- `tags` are omitted when empty instead of emitting an empty list.
- Numeric HTML entities in OpenGraph meta content are not unescaped (named
  entities only).
- The `channel` field currently derives from `videoDetails.author`; the pinned
  reference prefers the watch-page `videoSecondaryInfoRenderer` title, which
  arrives with watch-page enrichment.

## Deferred

Music auto-generated description fields (`album`, `artists`, `track`,
`release_date`, `release_year`), likes, subscriber counts, badges and
verification, watch-page chapters and heatmap, format-language and
format-quality enrichment, and storyboards.

## Automated evidence

- `internal/extractor.TestYouTubePlayerMetadataPinnedExtraction`
- `internal/extractor.TestYouTubePlayerMetadataUploadDates`
- `internal/extractor.TestYouTubePlayerMetadataOwnerProfile`
- `internal/extractor.TestYouTubePlayerMetadataAvailabilityAndAgeLimit`
- `internal/extractor.TestYouTubePlayerMetadataMediaType`
- `internal/extractor.TestYouTubePlayerMetadataThumbnails`
- `internal/extractor.TestYouTubePlayerMetadataThumbnailsMore`
- `internal/extractor.TestYouTubePlayerMetadataTagsCategoryRating`
- `internal/extractor.TestYouTubePlayerMetadataStretchedRatio`
- `internal/extractor.TestYouTubePlayerMetadataOGImage`
- `internal/extractor.TestYouTubePlayerMetadataJSONFieldSurvival`
- `internal/extractor.TestYouTubePlayerMetadataConcurrentDeterminism`
- `internal/extractor.FuzzYouTubePlayerMetadata`

The pinned corpus lives at `conformance/extractors/youtube_player_metadata/`
(`watch.html`, `expected.json`, `PROVENANCE.md`). The pilot corpus
(`conformance/extractors/youtube/expected.json`) now also reflects the
always-on fields (`channel`, `channel_url`, `age_limit`, `media_type`,
`thumbnails`).
