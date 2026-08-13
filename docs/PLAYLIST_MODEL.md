# Playlist extraction model

The extractor boundary represents either one media item or a playlist.
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
`Playlist.Random`/`--playlist-random` applies a bounded shuffle after selection;
the Go API accepts an injected random source for deterministic tests. Random
takes precedence over Reverse with a warning. `Playlist.Lazy`/`--lazy-playlist`
keeps iterator-order streaming and disables Reverse and Random with the pinned
per-option warnings. This sequential extractor model already streams normal
ordered playlists, so Lazy does not invent an unavailable total-entry count.
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
without selecting its extractor, recursively expanding it, or downloading it.
Supported metadata actions run before incomplete-entry match filters, and
archive matches are reported when the entry declares both an extractor key and
id. The entry keeps its URL, declared extractor key, transformed id/title,
transparent/non-transparent type, and source playlist fields in both
`InfoJSON` and `Result.Entries`. Range/item selection and reverse ordering
happen before flat materialization, so their pagination and ordering bounds are
unchanged. `--no-flat-playlist` disables an inherited configuration value.

## Bounds and failure policy

- A context cancellation stops static iteration, page fetching, extraction,
  and download.
- On-demand pagination stops at the first short page.
- One operation accepts at most 10,000 entries and eight nested playlist
  levels; recursive URL cycles fail before another request.
- Ordinary entry extraction/download failures follow `Playlist.ErrorPolicy`:
  the zero-value Continue policy records a redacted event, increments
  `Result.SuppressedFailures`, and advances to the next entry; Abort propagates
  the indexed error immediately. Cancellation, security/internal failures, playlist
  resource limits, invalid request options, and event-handler failures always
  propagate. Iterator failures are playlist-global and always propagate.
- `Playlist.MaxFailures`/`--skip-playlist-after-errors` stops the remaining
  queue after the configured number of ordinary failures and emits one bounded
  aggregate event. Zero disables the threshold.
- CLI `--ignore-errors`/`-i` treats a completed partial playlist as successful;
  the default `--no-abort-on-error` continues but exits non-zero when
  `SuppressedFailures` is non-zero. `--abort-on-error` and
  `--no-ignore-errors` select Abort.
- Metadata is held in memory after resolution so `--print-json` and
  `--dump-single-json` can emit the complete ordered hierarchy, while
  `--dump-json` recursively emits ordered leaf entries.

This is the reusable base for the representative site pilots. The non-CLI
global/discard variants of `extract_flat` and arbitrary transparent field
overlays are outside the current compatibility claim rather than hidden
behavior. The Go API intentionally exposes continue-versus-abort plus an observable
failure count; yt-dlp's internal `ignoreerrors="only_download"` return-code
sentinel is represented only at the CLI exit-policy boundary. Unlike upstream random-access paged lists, this sequential
extractor boundary may fetch earlier pages while seeking a later sparse index.
