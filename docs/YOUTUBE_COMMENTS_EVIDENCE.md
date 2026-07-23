# YouTube comments evidence

Status: a bounded, opt-in public-comments slice is implemented for YouTube
watch videos. It is native Go and has no runtime or build-time Python
dependency.

## User contract

Comments are disabled by default. Enable them with either yt-dlp-compatible
alias:

```sh
ytdlp-go --write-comments --skip-download --print-json \
  'https://www.youtube.com/watch?v=VIDEO_ID'
```

`--get-comments` is an alias for `--write-comments`;
`--no-write-comments` and `--no-get-comments` disable retrieval. The
Go-specific controls are:

```text
--youtube-comment-sort new|top
--youtube-max-comments TOTAL[,PARENTS[,REPLIES[,PER_THREAD[,DEPTH]]]]
```

The default sort is `new`. Zero-valued omitted limits normalize to 100 total,
100 parents, 100 replies, 20 replies per thread, and depth 2. Each count is
bounded to 10,000, depth is bounded to 8, and extraction makes at most 100
comment-page requests. Reply continuations and nested `subThreads` are followed
only while the configured depth and reply budgets allow them.

The public Go API exposes the same policy through
`Request.YouTubeComments`. Retrieval is deliberately deferred until normal
metadata matching and archive decisions have accepted the item. A rejected
item therefore does not make comment API calls.

When enabled and successful, the extractor adds `comments` and the actual
number retrieved as `comment_count`. Each accepted comment has `id`, `text`,
and `parent` (`root` for a top-level comment), with bounded optional author,
time-text, like-count, uploader/verified, creator-heart, and pinned fields.
When YouTube reports comments disabled, both fields are JSON `null`.

## Deterministic evidence

The checked-in synthetic corpus covers:

- initial continuation discovery and the pinned forced-continuation fallback;
- `top` and `new` sort selection;
- visitor-data rotation between `/youtubei/v1/next` requests;
- reload and append continuation actions;
- legacy `commentRenderer` and modern entity-backed `commentViewModel` fields;
- inline replies, reply continuation requests, nested subthreads, click
  tracking, pinned-comment duplicate handling, and
  parent/reply/per-thread/depth/total limits;
- up to three attempts for transient transport/server failures and structurally
  incomplete comment responses;
- disabled comments, authentication and rate-limit categorization,
  cancellation, malformed JSON, oversized text, and parser fuzzing; and
- deferred product integration, public-option preflight, and CLI option
  parsing.

The primary automated evidence is:

- `internal/extractor.TestYouTubeCommentsDefaultNewSortVisitorRotationAndFields`
- `internal/extractor.TestYouTubeCommentsTopSortAndBounds`
- `internal/extractor.TestYouTubeCommentsWrappedSortReplyContinuationAndMultipleActions`
- `internal/extractor.TestYouTubeCommentsPinnedDuplicateDoesNotStopTraversal`
- `internal/extractor.TestYouTubeCommentsReplyContinuationIsDepthFirstForTotalLimit`
- `internal/extractor.TestYouTubeCommentsDisabledAndForcedContinuation`
- `internal/extractor.TestYouTubeCommentFailuresCancellationAndLimits`
- `internal/extractor.TestYouTubeCommentRetriesTransientAndIncompleteResponses`
- `internal/extractor.TestYouTubeCommentsCapActualHTTPAttempts`
- `internal/extractor.TestParseYouTubeCommentPageEnforcesStructuralBudgets`
- `internal/extractor.TestYouTubeModernCommentSanitizesIdentityAndThumbnail`
- `internal/extractor.TestParseYouTubeCommentPageRejectsMalformedAndOversizedText`
- `internal/extractor.FuzzParseYouTubeCommentPage`

Additional product and CLI tests are recorded in
`conformance/parity_manifest.yaml`. Fixture derivation and exact upstream
locations are recorded in
`conformance/extractors/youtube/PROVENANCE.md`.

## Intentional limits and deviations

This evidence does not claim full upstream YouTube-comment parity:

- authenticated comments and signed-in Innertube comment clients are not
  supported;
- the approximate header count is not exposed before retrieval; after a
  complete bounded retrieval, `comment_count` is the number actually emitted;
- relative time text is retained as `_time_text`, but an estimated Unix
  `timestamp` is not synthesized;
- upstream `--ignore-errors` recovery policy is not reproduced; and
- renderer compatibility is limited to the deterministic legacy and modern
  shapes named above.

These boundaries keep the first slice deterministic and resource-bounded.
They are follow-up work, not implied compatibility.
