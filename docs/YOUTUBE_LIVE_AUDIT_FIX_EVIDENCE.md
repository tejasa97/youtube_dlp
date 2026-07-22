# YouTube live-audit fix evidence

Date: 2026-07-22  
Host: macOS/arm64, Docker Desktop Linux/arm64  
Reference: read-only `yt-dlp/yt-dlp@aefce1eea4d0b6bab1ec2bd3beff09bff91a39c8`

No test or production path invoked Python or the reference checkout. Public
network checks were non-gating and used anonymous requests.

## Corrected behavior

| Area | Automated evidence | Anonymous live result |
| --- | --- | --- |
| Modern playlists | legacy and `lockupViewModel` fixtures, de-duplication, modern continuation seeds and parser fuzzing | pinned single-video playlist returned one `aqz-KE-bpKQ` entry; pinned empty playlist returned zero |
| Playback offsets | typed URL parser tests and fuzzing | Big Buck Bunny with `t=1s&end=9` returned `start_time=1`, `end_time=9`, and 25 formats |
| Error categories | wrapped-sentinel and client category tests | missing cookie file and unmatched format both returned exit 2 / `invalid_input` |
| Channel live aliases | route, active fixture, offline, malformed, hostile route, and fuzz tests | `@LofiGirl/live` entered the resolved video extractor; that active stream then reached the known EJS-helper timeout described below |
| Runtime image | pinned Alpine build, non-root/read-only probes | UID 65532 image had ffmpeg/ffprobe, no Python, and merged formats `299+140` into a two-stream MP4 |

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

The removed historical `BaW_jenozKc` test video and unavailable NASA live
recording remain upstream content changes, not regressions in this patch set.
