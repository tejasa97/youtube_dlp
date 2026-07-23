# Playlist extraction model

The Phase 1 extractor boundary represents either one media item or a playlist.
A playlist owns metadata plus an `EntrySequence`; constructing it does not fetch
or materialize its entries. Static and on-demand paged sequences both create
independent, ordered iterators.

An entry follows yt-dlp's URL-result shape: URL, optional extractor key, id,
title, and transparent/non-transparent type. An explicit extractor key is
authoritative and an unknown key fails instead of silently choosing Generic.
For transparent entries, the pilot merges the producer's id and title over the
resolved result.

The product resolves entries sequentially. A resolved entry may itself be a
playlist, so nested results retain their hierarchy in `InfoJSON` and in the Go
API's `Result.Entries`. Each materialized child metadata object receives
`playlist_index`, `playlist_id`, and `playlist_title`; downloads use the same
operation transport, cookie jar, challenge solver, output policy, and
cancellation context. Parent byte counts and download status aggregate
successful descendants.

Each playlist encountered by an operation applies the request's inclusive,
one-based `Playlist.Start` and `Playlist.End` bounds (`0` means the first entry
or no explicit end in the Go API; the legacy end value `-1` is also unbounded).
The CLI exposes these as `--playlist-start` and `--playlist-end`, with
`--no-playlist-reverse` available to override inherited configuration. Normal
selection stays lazy and does not request a page
after the end bound. `Playlist.Reverse`/`--playlist-reverse` reverses the
selected range, so it buffers at most the bounded 10,000-entry operation limit.
In either output order, `playlist_index` remains the entry's original position
in its source playlist.

`Playlist.Items` and `-I`/`--playlist-items` select comma-separated one-based
indexes or `[START]:[END][:STEP]` ranges; the legacy `START-END` spelling is
also accepted. Sparse order, duplicate suppression, zero, `inf`, open ranges,
positive and negative steps, and negative indexes follow the pinned reference
corpus. An item expression takes precedence over Start/End with a structured
warning when either range bound was also selected, and Reverse is applied
afterward. Finite non-negative expressions stop iteration at the last
requested source index. Expressions that need the final playlist length are
resolved after consuming the bounded sequence. Specifications are limited to
4 KiB, 256 segments, integer magnitudes of one billion, and the existing
10,000-source-entry operation bound.

`Playlist.Flat` and `--flat-playlist` retain each selected URL-result entry
without selecting its extractor, recursively expanding it, applying media
filters or archives, or downloading it. The entry keeps its URL, declared
extractor key, id, title, transparent/non-transparent type, and source playlist
fields in both `InfoJSON` and `Result.Entries`. Range/item selection and reverse
ordering happen before flat materialization, so their pagination and ordering
bounds are unchanged. `--no-flat-playlist` disables an inherited configuration
value.

## Bounds and failure policy

- A context cancellation stops static iteration, page fetching, extraction,
  and download.
- On-demand pagination stops at the first short page.
- One operation accepts at most 10,000 entries and eight nested playlist
  levels; recursive URL cycles fail before another request.
- Iterator, extraction, and download errors are fail-fast in this pilot. They
  retain structured error categories and the failing one-based entry index.
- Metadata is held in memory after resolution so `--print-json` can emit the
  complete ordered hierarchy.

This is the reusable base for the representative site pilots. Broader yt-dlp
options such as random ordering, the non-CLI global/discard variants of
`extract_flat`, arbitrary transparent field overlays, and configurable
ignore-error thresholds remain explicit later compatibility work rather than
hidden behavior. Unlike upstream random-access paged lists, this sequential
extractor boundary may fetch earlier pages while seeking a later sparse index.
