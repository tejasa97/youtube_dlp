# YouTube media-section downloads evidence

Status: implemented in PR4 (generic media-section downloads and --download-sections).

## Reference

Pinned Python reference: `yt-dlp/yt-dlp` at commit `aefce1eea4d0b6bab1ec2bd3beff09bff91a39c8`.

- `yt_dlp/YoutubeDL.py:2869-2870` — derived duration `round(section_end - section_start, 3)`.
- `yt_dlp/YoutubeDL.py:3104-3132` — --download-sections range parsing and section composition.
- `yt_dlp/downloader/external.py:435,453-456` — FFmpegFD applies `-ss`/`-t`.
- `yt_dlp/downloader/external.py:3486` — abort when the selected downloader cannot partially download.
- `yt_dlp/extractor/youtube/_clip.py:1-80` — extractor-driven section_start/section_end contract (PR5 consumer).

## Design

PR4 is a generic product/media-layer feature. It does not add extractor clip routing (that is PR5).

- `internal/compat/sections` — bounded generic section planner.
  - Accepts only `*START-END`, `*START-inf`, `*from-url`; rejects all other values explicitly.
  - Bounds: specification count, byte size, timestamp length, and range count caps.
  - Rejects NaN, infinity (except literal open-ended inf), negative starts, empty ranges,
    end <= start, and excessive precision/length.
- `internal/media/ffmpeg/section_download.go` — shell-free supervised ffmpeg section operation.
  - Mirrors DownloadHLS safety/atomic/cancellation/header-allowlist patterns.
  - ffmpeg receives the selected URL with `-ss`/`-t`; separate A/V inputs are mapped with `-map`.
  - Stream copy by default; `-force_key_frames` re-encode when force-keyframes is enabled.
  - Credential-isolated headers/cookies are rejected before output (fail closed).
- `pkg/ytdlp` — product consumer.
  - `Request.DownloadSections []string` (repeatable).
  - `section_plans.go` expands each output plan into its requested sections with
    deterministic one-based section_number and collision-safe destinations.
  - `section_download.go` delegates to the ffmpeg section consumer.
  - Runtime download dispatch in `output_lifecycle.go` routes to the section consumer when
    a section is active; ordinary downloads are unchanged otherwise.
  - Force-keyframes validation now accepts section downloads as a valid consumer.
- `internal/cli/run.go` — repeatable `--download-sections` via the existing stringListFlag pattern.

## Behavior

- An extractor-provided section (section_start/section_end) triggers ffmpeg section downloading
  even without --download-sections, and derives the base duration as
  `section_end - section_start` rounded compatibly. This is the PR5 contract.
- CLI ranges compose with an extractor section using the extractor's start as the base offset
  (pinned YoutubeDL.py:3104-3132). start_time/end_time are consumed only by *from-url and do not
  trigger partial downloading otherwise.
- Simulation and skip-download bypass ffmpeg and produce no media artifacts, while still
  validating range syntax deterministically.
- Archive identity is one record per extractor item, written only after every section commits.
  Section outputs roll back transactionally on any failure or cancellation.

## Deferred (documented)

- Chapter-title regular expressions (negative timestamps relative to duration, arbitrary upstream
  range generators).
- Native fragment trimming; HLS/DASH partial downloads use ffmpeg as upstream does.
- Partial live streams unless deterministically selected.
- PR5 YouTube Clips routing (this PR provides the generic consumer PR5 will use).

## Validation

- `go test ./...` — 40 packages ok, 0 failures.
- `go test -race` on pkg/ytdlp, internal/compat/sections, internal/media/ffmpeg, internal/cli — pass.
- `go vet ./...` — clean.
- `go mod tidy -diff` — clean.
- `git diff --check` — clean.
- `go run ./cmd/paritycheck` — 94 capabilities validated (product.download_sections added).
- Linux and Windows cross-builds — pass.
