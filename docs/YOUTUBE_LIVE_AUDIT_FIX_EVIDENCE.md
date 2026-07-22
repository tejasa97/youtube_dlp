# YouTube live-audit fix evidence

Date: 2026-07-22
Host: macOS/arm64, Docker Desktop Linux/arm64
Reference: read-only `yt-dlp/yt-dlp@aefce1eea4d0b6bab1ec2bd3beff09bff91a39c8`

No test or production path invoked Python or the reference checkout. Public
network checks were non-gating and used anonymous requests.

## Corrected behavior

| Area | Automated evidence | Anonymous live result |
| --- | --- | --- |
| Modern playlists | legacy and `lockupViewModel` fixtures, duplicate-occurrence preservation, direct and executor-wrapped continuation seeds, continuation bounds, and parser fuzzing | pinned single-video playlist returned one `aqz-KE-bpKQ` entry; pinned empty playlist returned zero |
| Playback offsets | typed URL parser tests and fuzzing | Big Buck Bunny with `t=1s&end=9` returned `start_time=1`, `end_time=9`, and 25 formats |
| Error categories | wrapped-sentinel and client category tests | missing cookie file and unmatched format both returned exit 2 / `invalid_input` |
| Channel live aliases | route, active fixture, offline, malformed, hostile route, and fuzz tests | `@LofiGirl/live` entered the resolved video extractor; that active stream then reached the known EJS-helper timeout described below |
| Runtime image | digest-pinned Alpine stages and checksum-verified, LGPL-only FFmpeg 6.1.2 and LAME 3.100 source builds; exact sources and licenses ship in the image | UID 65532 image had ffmpeg/ffprobe, no Python, merged formats `299+140` into a two-stream MP4, and retained MP3 extraction |

## Regression matrix

- Shorts `18NGQq7p3LY`: 20 formats; progressive format 18 produced a 228,218-byte H.264/AAC MP4.
- Adaptive Shorts formats `299+140`: produced a 1,945,748-byte H.264/AAC MP4 on the host and in the runtime container.
- Big Buck Bunny `YE7VzlLtp-4`: 25 formats, two manual subtitle languages, and 157 automatic-caption language entries.
- One English JSON3 automatic-caption URL returned HTTP 200, 4,010 bytes, and 31 events. The signed URL was not retained.
- Private content returned the authentication category; the pinned age-gated case returned authentication with the expected sign-in reason.
- Cancellation during a rate-limited Big Buck Bunny download returned exit 130 / `cancelled` and published no final output.
- The strict scratch image ran `--version` and anonymously extracted the Shorts metadata with 20 formats.

The earlier size-limit, path-confinement, archive, overwrite, audio extraction,
and remux probes remain covered by the unchanged downloader/postprocessor tests
and by the pre-fix live audit. Downloaded media and signed URLs were kept only
in temporary directories and were not committed.

## Local verification gate

The following completed successfully without GitHub Actions:

- `go mod tidy` with no `go.mod`/`go.sum` drift;
- `go test ./...`;
- `go test -race ./...`;
- `go vet ./...`;
- five-second fuzz runs for `FuzzParseYouTubePlaylistData`,
  `FuzzParseYouTubeTarget`, and `FuzzYouTubeChannelLiveAlias`;
- no-cgo builds of the main executable and JavaScript helper for linux/amd64,
  linux/arm64, darwin/amd64, darwin/arm64, and windows/amd64;
- `go run ./cmd/paritycheck` (55 capabilities, zero temporary fallbacks);
- `.github/python-free.Dockerfile` build plus scratch runtime probes;
- `.github/runtime.Dockerfile` build plus Python absence, ffmpeg/ffprobe,
  non-root, read-only-root, writable-volume, and adaptive-merge probes.

## Known service-dependent deviation

Some concurrent YouTube probes and the active `@LofiGirl/live` stream required
a current player challenge that exceeded the isolated EJS helper's execution
deadline. Sequential Shorts and Big Buck Bunny retries succeeded. The channel
alias itself resolved into the existing video path; the remaining failure is a
protected-player solver/timing limitation, not a route-parser failure. It stays
explicitly categorized as unsupported and is not represented as full live
download parity.

The EJS solver now uses a two-phase preprocess/solve split with:

- A **scoped trusted wall-time allowance** (60 s) for EJS preprocessing only;
  untrusted protocol requests remain bounded at the original 30 s hard max.
  The `Trusted` flag is in-process only (`json:"-"`) and never serialized.
- A **bounded LRU cache** (max 8 entries) that persists at the client level
  across separate `Run` calls, so distinct downloads sharing the same player
  script skip redundant preprocessing.
- **Singleflight coordination** so concurrent requests for the same uncached
  player coalesce into exactly one preprocessing execution.

### Proof levels

| Level | Evidence | Status |
| --- | --- | --- |
| Automated proof | `TestRepresentativeWorkloadUnderOldLimit` (proves 55 s > 30 s old limit, Trusted gate works), `TestSupervisorTrustedWallTimeCrossesProcessBoundary` (end-to-end: 45 s request succeeds across supervisor→helper pipe), `TestSupervisorRejectsUntrustedExtendedWallTime` (untrusted 45 s rejected), `TestSingleflightCoalescesPreprocessing` (exactly 1 preprocess under 8 concurrent goroutines), `TestSingleflightFollowerCancellation` (follower cancels in ~200 ms, not 55 s), `TestLargeGeneratedPlayerWorkload` (~150 KB deterministic workload completes), `TestTrustedWallTimeAllowance` (protocol boundary), `TestClientConcurrentRunAndClose` (no race/panic under concurrent Run+Close) | Passing |
| Live validation | Protected-video canary extraction against live YouTube | Pending (service-dependent, non-authoritative) |
| Remaining deviation | No sanitized real 1-2 MB player fixture committed (proprietary); generated ~150 KB workload exercises meriyah parse path but does not empirically reproduce the original 30 s timeout (that required a real 1-2 MB player under the old single-phase architecture); the fix is proven structurally via protocol validation gating | Documented |

Live canary validation remains service-dependent and non-authoritative.

The removed historical `BaW_jenozKc` test video and unavailable NASA live
recording remain upstream content changes, not regressions in this patch set.
