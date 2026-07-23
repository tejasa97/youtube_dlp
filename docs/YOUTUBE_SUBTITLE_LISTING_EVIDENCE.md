# YouTube subtitle listing evidence

## Reference provenance

Behavior was compared read-only with the pinned checkout at
`/Users/tejas/projects/yt-dlp-reference`, commit
`aefce1eea4d0b6bab1ec2bd3beff09bff91a39c8`.

The relevant upstream code is `yt_dlp/options.py` (`--list-subs`) and
`yt_dlp/YoutubeDL.py` (`render_subtitles_table` and `list_subtitles`). It
establishes these core behaviors:

* listing is simulation by default;
* automatic captions are listed before manual subtitles, and only when that
  field is present;
* language order follows extractor-provided mapping order;
* status/no-track messages use the informational channel while tables use
  stdout; and
* formats and names are displayed in reverse track preference order.

## Implementation and intentional deviations

`ytdlp-go --list-subs` forces `SkipDownload` and disables both subtitle write
options, including when `--skip-download`, `--write-subs`, or
`--write-auto-subs` are also explicit. This proves listing cannot create media
or subtitle files. `--no-simulate` is not implemented because this CLI has no
clean existing simulation toggle.

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
  `--skip-download`, `--print-json`, and cancellation.
