# Changelog

All notable user-visible changes to `ytdlp-go` will be documented in this file.

The project is pre-release. Versioned module tags may be published without
endorsed public binaries. Changes not yet in a tagged release are collected
under **Unreleased**. Historical engineering phases and capability evidence
are documented separately and are not reconstructed here as fictional releases.

The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/).
Published version identifiers follow the compatibility policy for the relevant
interface maturity.

## Unreleased

### Added

- No entries yet.

### Changed

- No entries yet.

### Fixed

- YouTube adaptive downloads use upstream-compatible randomized 10 MiB HTTP
  ranges, avoiding request-rate 403 failures caused by fixed 1 MiB ranges.
- Ordinary YouTube direct-media downloads recover from a bounded HTTP 403 by
  re-extracting the source, rotating through distinct Innertube client URLs,
  matching the exact representation (including combined IDs such as 18-0),
  and resuming only when the refreshed server validates the saved byte range.
  Googlevideo 403s, including the first Range, retry the current URL with
  backoff before refresh; if a refreshed URL still 403s at a mid-file offset,
  only the affected partial is restarted from byte zero.

### Security

- No entries yet.

## [0.2.2] - 2026-08-17

### Added

- Public `DownloadHTTPStatusError` and `DownloadHTTPStatusCode` so desktop
  callers can detect a media-downloader HTTP status (including an expired
  signed URL's 403) with `errors.As` instead of matching error text.
  Extractor `HTTPStatusError` values remain a separate type.

### Changed

- Public VidStow links now point at `github.com/vidstow/vidstow`, and the
  documented desktop scope includes public YouTube videos, Shorts, and
  playlists.

## [0.2.1] - 2026-08-15

### Added

- Resume for finite HLS and static DASH sessions on the public resumable
  session facade.
- Restart-safe FFmpeg processing workspaces so pause and resume can recover
  post-processing without restarting from scratch.
- Staged session publication and multi-track recovery for direct downloads.

### Fixed

- Concurrent YouTube player preprocessing is serialized so parallel analyses
  no longer race the shared player pipeline.
- Adaptive YouTube media downloads use bounded byte ranges.
- Duplicate YouTube caption resources are collapsed before download.

## Release-note policy

Add an entry when a change affects:

- installation, packaging, supported platforms, or updates;
- Desktop workflows or visible copy;
- CLI behavior, output, defaults, or compatibility;
- the public Go API;
- supported sites or important known limitations;
- security boundaries or dependency redistribution; or
- migration steps users must take.

Pure test refactors, fixture maintenance, and internal evidence changes do not
need a changelog entry unless they alter an observable claim.

[Unreleased]: https://github.com/tejasa97/youtube_dlp/compare/v0.2.2...HEAD
[0.2.2]: https://github.com/tejasa97/youtube_dlp/compare/v0.2.1...v0.2.2
[0.2.1]: https://github.com/tejasa97/youtube_dlp/compare/v0.2.0...v0.2.1
