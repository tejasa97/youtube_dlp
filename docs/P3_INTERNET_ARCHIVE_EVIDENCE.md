# Phase 3 Internet Archive extractor evidence

The native extractor accepts `archive.org/details/{identifier}` and
`archive.org/embed/{identifier}`, with an optional safe item-relative media
path. It uses only the bounded public metadata endpoint and never invokes
Python or exposes a private file.

Automated evidence covers deterministic format selection, source preference,
single-file routing, reusable lazy playlist entries, inherited item metadata,
file metadata, thumbnails, subtitles, private/unavailable/authentication and
network categories, cancellation, response limits, hostile asset names, URL
routing, malformed inventories, and parser fuzzing.

Known deviations from the pinned reference:

- The Go extractor does not fetch the embed page. It infers playable groups
  from metadata `original` relationships, avoiding a second unbounded HTML
  parse. Items whose player-only playlist lacks usable metadata relationships
  may not be grouped identically.
- Authenticated access to private formats is intentionally absent. A private
  requested file returns authentication-required rather than being emitted.
- Reviews/comments are not included in this increment.
- Media types outside the explicit audio/video extension allowlist are not
  emitted until native protocol support and deterministic tests exist.
