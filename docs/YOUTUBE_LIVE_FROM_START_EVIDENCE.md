# YouTube live-from-start evidence

Status: compatible for the bounded public adaptive-live corpus described
below. The feature is opt-in through `--live-from-start`; current-edge
downloading remains the default and `--no-live-from-start` disables an
inherited opt-in.

## Behavior

For active public YouTube streams, eligible adaptive formats with a positive
`targetDurationSec` are reconstructed from their first retained sequence.
The native downloader probes `X-Head-Seqnum`, downloads each newly observed
`sq` through the inclusive head exactly once, polls on a bounded interval, and
refreshes expiring signed URLs by re-extracting the same `(itag, client)`
identity. Selected video and audio tracks run concurrently and are merged
through the existing ffmpeg pipeline after both streams end.

An active-to-ended refresh performs one final active-semantics probe through
head `H`. A fresh extraction that is already post-live continues to use the
separate finite DVR rule documented in
[YouTube post-live DVR evidence](YOUTUBE_POST_LIVE_DVR_EVIDENCE.md).
Extraction, JSON printing, and `--skip-download` never probe a media URL.

Signed query bytes and selected headers are preserved on media requests.
Events and returned diagnostics strip the complete media query. Existing
outputs fail before a probe, cancellation removes unpublished state, and
publication is atomic.

## Provenance

The contract derives from `YoutubeIE._needs_live_processing`,
`_prepare_live_from_start_formats`, and `_live_adaptive_fragments` in the
read-only reference `yt-dlp/yt-dlp` pinned at
`aefce1eea4d0b6bab1ec2bd3beff09bff91a39c8`.

All player responses, signing parameters, headers, timelines, and media used
by tests are synthetic. Production and tests do not import or execute Python
and do not depend on the reference checkout.

## Automated evidence

- `internal/extractor.TestYouTubeLiveFromStartEligibilityAndMetadataOnlyExtraction`
- `internal/extractor.TestYouTubeLiveFromStartRequiresEligibleAdaptiveFormats`
- `internal/protocol/youtubelive.TestLiveDownloadRefreshesAndFinalProbesEndedStream`
- `internal/protocol/youtubelive.TestLiveDownloadAggressivelyRefreshesAfterMisses`
- `internal/protocol/youtubelive.TestLiveMalformedProbeIsCategorizedAndRedacted`
- `internal/protocol/youtubelive.TestLiveCancellationCleansTemporaryArtifacts`
- `internal/protocol/youtubelive.TestLive120HourClamp`
- `internal/protocol/youtubelive.TestLiveEventSinkFailureCleansAndCompletionCannotVeto`
- `internal/protocol/youtubelive.FuzzLiveBeginSequence`
- `pkg/ytdlp.TestYouTubeLiveFromStartDownloadsTracksConcurrentlyAndMerges`
- `pkg/ytdlp.TestYouTubeLiveFromStartPublicRequestReachesExtractor`
- `pkg/ytdlp.TestYouTubeLivePublicBoundsFailAtRequestValidation`
- `pkg/ytdlp.TestYouTubeLiveRefreshCoordinatorCachesAndMatchesExactIdentity`
- `pkg/ytdlp.TestYouTubeLiveRefreshCoordinatorReportsEndedStatus`
- `pkg/ytdlp.TestYouTubeLiveRefreshCoordinatorEndsWithoutMatchingFinalFormat`
- `internal/cli.TestRunAcceptsLiveFromStartAndLastNegativeFlagWins`

## Known deviations

- The native implementation caps a session at 10,000 media segments by
  default and exposes additional bounded poll/no-progress controls to Go
  callers. It fails explicitly instead of running without a resource ceiling.
- Missing-head polling includes a cancellation-aware delay rather than the
  reference's immediate retry loop.
- Cancellation removes unpublished media; it does not publish the partial
  artifact produced before cancellation.
- Generated sequences cannot be delegated to an external downloader and do
  not have a process-restart resume contract.
- Audio and video tracks download concurrently, but segments within one track
  are fetched serially.
- Direct SABR/UMP and broad authenticated Innertube coverage remain pending.
