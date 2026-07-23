# YouTube subtitle listing evidence

## Reference provenance

Behavior was compared read-only with the pinned checkout at
`/Users/tejas/projects/yt-dlp-reference`, commit
`aefce1eea4d0b6bab1ec2bd3beff09bff91a39c8`.

The relevant upstream code is `yt_dlp/options.py` (`--list-subs`,
`--simulate`, `--no-simulate`, and `--skip-download`) and
`yt_dlp/YoutubeDL.py` (the tri-state `simulate` normalization,
`render_subtitles_table`, `list_subtitles`, and the early simulation return).
It establishes these core behaviors:

* listing is simulation by default unless `--no-simulate` is explicit;
* explicit simulation writes neither media nor related files, while
  `--skip-download` may still write requested related files;
* automatic captions are listed before manual subtitles, and only when that
  field is present;
* language order follows extractor-provided mapping order;
* status/no-track messages use the informational channel while tables use
  stdout; and
* formats and names are displayed in reverse track preference order.

## Implementation and intentional deviations

`ytdlp-go` exposes the same ordered tri-state through `-s`/`--simulate` and
`--no-simulate`. With neither flag explicit, `--list-subs` enables product
simulation and cannot create media, subtitle, archive, or postprocessor
artifacts. With `--no-simulate`, listing still renders but processing continues
normally, including explicitly requested subtitle writes. A later positive or
negative simulation flag wins, including values inherited through the existing
configuration loader.

The public Go request keeps `Simulate` separate from `SkipDownload`.
`SkipDownload` (and its CLI alias `--no-download`) skips only media and can
still write requested subtitle sidecars; `Simulate` returns after extraction
and before all artifact-producing operations.

Tables always retain the `Language`, `Name`, and `Formats` columns for stable
plain-text parsing. `--print-json` remains supported: tables precede the final
InfoJSON on stdout, as upstream list-only behavior permits both outputs.
`--quiet` suppresses normal extraction progress but not requested listing
status/tables.

The renderer accepts normalized InfoJSON only. It keeps JSON object order with
a streaming decoder, bounds InfoJSON to 4 MiB, languages to 200, tracks per
language to 100, and rendered field text to 512 runes (with control characters
made single-line). Invalid shapes fail with the non-sensitive message
`subtitle listing: invalid subtitle metadata`.

## Test evidence

* `internal/cli/subtitle_list_test.go` covers automatic/manual order, no-track
  sections, malformed structures, bounds, cancellation, and fuzzing.
* `internal/cli/run_test.go` covers end-to-end simulation with conflicting
  write options (zero output files), stdout/stderr separation, quiet,
  `--skip-download`, `--print-json`, cancellation, ordered tri-state
  precedence, boolean flag forms, the short alias, `--no-download`, and
  `--list-subs --no-simulate` media/sidecar output.
* `pkg/ytdlp/client_test.go` proves embedding-level simulation suppresses
  media, subtitle, archive, and postprocessor artifacts while returning valid
  metadata.
