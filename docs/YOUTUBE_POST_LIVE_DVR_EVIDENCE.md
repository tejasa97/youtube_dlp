# YouTube post-live DVR evidence

Status: compatible for the bounded finite post-live adaptive corpus described
below. Active `--live-from-start` polling is implemented separately and
documented in [YouTube live-from-start evidence](YOUTUBE_LIVE_FROM_START_EVIDENCE.md).

## User-visible behavior

When YouTube marks a video `post_live` and exposes adaptive audio/video formats
with a positive `targetDurationSec`, ytdlp-go reconstructs each track from
YouTube's retained sequence start, then uses the normal ffmpeg merge path.
Extraction and `--skip-download` remain metadata-only: the signed media URL is
probed only when a selected format is downloaded.

The finite downloader requests the bare adaptive URL once, parses
`X-Head-Seqnum`, and downloads `sq=0` through `sq=H-2` for head value `H`.
Existing signed query bytes and selected HTTP headers are preserved while any
pre-existing `sq` key is replaced. The newest two sequences are excluded as
potentially incomplete.

If the broadcast start is more than 120 hours old, the beginning is clamped to
YouTube's retained window using `targetDurationSec`. The product segment limit
still applies and fails explicitly rather than silently truncating a longer
plan. Post-live HLS is suppressed; incomplete DASH manifests remain a bounded
fallback for recordings of at most two hours. Incomplete direct formats are
retained with the pinned `-10` preference penalty, so defaults favor finite
DVR tracks while explicit format-ID selection remains authoritative.

## Behavioral provenance

The contract is derived from the pinned read-only reference
`yt-dlp/yt-dlp@aefce1eea4d0b6bab1ec2bd3beff09bff91a39c8`, specifically
`YoutubeIE._needs_live_processing`, `_prepare_live_from_start_formats`,
`_live_adaptive_fragments`, the `targetDurationSec` format handling, and live
metadata assembly in `yt_dlp/extractor/youtube/_video.py`.

All media, headers, signed-query tokens, timestamps, and player responses used
by tests are synthetic. No test or production binary imports or executes
Python or reads the reference checkout.

## Automated evidence

- `internal/extractor.TestYouTubePostLiveAdaptiveFormatsUseFiniteDVRProtocol`
- `internal/extractor.TestYouTubePostLiveMetadataFallsBackAcrossPlayerResponses`
- `internal/extractor.TestParseYouTubeLiveTimestampBounds`
- `internal/format.TestBestSelectsFirstDownloadableFormat`
- `internal/format.TestDefaultPrefersAdaptivePairThenCombined`
- `internal/format.TestPreferenceRanksDefaultsButNotExplicitFormatIDs`
- `internal/protocol/youtubelive.TestDownloadFinitePostLiveTailAndPreservesSignedQuery`
- `internal/protocol/youtubelive.TestBuildPlanReplacesSQAndExcludesFinalTwoSequences`
- `internal/protocol/youtubelive.TestBuildPlanAppliesPinned120HourClampBeforeLimit`
- `internal/protocol/youtubelive.TestProbeEmitsClampWarning`
- `internal/protocol/youtubelive.TestProbeHeaderFailuresAndRetry`
- `internal/protocol/youtubelive.TestSignedURLsAreRedactedFromTransportFailures`
- `internal/protocol/youtubelive.TestDownloadFailureCleansTemporaryStateAndPreservesDestination`
- `internal/protocol/youtubelive.TestDownloadRetriesSegmentsAndEnforcesSizeLimit`
- `internal/protocol/youtubelive.TestDownloadCancellationStopsWorkAndCleansState`
- `internal/protocol/youtubelive.TestExistingDestinationFailsBeforeNetwork`
- `internal/protocol/youtubelive.TestCompletedEventCannotVetoPublishedArtifact`
- `internal/protocol/youtubelive.FuzzParseHeadSequence`
- `internal/protocol/youtubelive.FuzzSequenceURLConstruction`
- `pkg/ytdlp.TestYouTubePostLiveAdaptiveTracksDownloadAndMerge`
- `pkg/ytdlp.TestYouTubePostLiveRejectsExternalDownloaderAndCategorizesFailures`
- `pkg/ytdlp.TestOperationPostLivePreferenceKeepsExplicitDirectFormatAuthoritative`

## Known deviations

- Active-stream rewind, URL refresh, and active-to-ended polling use the
  separate live-from-start path; this document covers only finite post-live
  reconstruction.
- The native product ceiling is 10,000 segments, below the theoretical
  120-hour sequence count for short target durations.
- Generated sequences cannot be delegated to an external downloader.
- There is no process-restart resume contract for this generated protocol.
- Direct SABR/UMP and authenticated Innertube clients remain pending.
