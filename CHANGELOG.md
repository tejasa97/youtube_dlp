# Changelog

All notable user-visible changes to `ytdlp-go` will be documented in this file.

The project is pre-release. Until a versioned public release is published,
changes are collected under **Unreleased**. Historical engineering phases and
capability evidence are documented separately and are not reconstructed here
as fictional releases.

The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and future public versions are intended to follow semantic versioning where the
interface's maturity permits it.

## Unreleased

### Added

- A Wails-based Desktop preview for single public YouTube videos, with quality
  presets, FIFO queueing, progress, cancellation, retry, history, settings, and
  FFmpeg detection.
- Public documentation paths for Desktop usage, installation, CLI workflows,
  troubleshooting, architecture, project status, and roadmap.

### Changed

- The root README now presents Desktop, CLI, and Go API as interfaces over one
  native engine and keeps detailed engineering evidence in focused documents.

### Fixed

- YouTube JavaScript challenge timeouts are surfaced in Desktop with an
  actionable retry message instead of a generic network or unsupported error.

### Security

- No entries yet.

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
