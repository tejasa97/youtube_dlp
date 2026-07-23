# CLI staged print-output evidence

`ytdlp-go` supports repeatable `-O`/`--print` rules with an optional lifecycle
prefix:

```text
--print title
--print "%(id)s %(format_id)s"
--print "after_filter:%(title)s"
--print "after_move:%(filename)s"
--print "playlist:%(title)s"
```

The public Go API represents these as `PrintRule` values and returns ordered
`PrintOutput` records. Supported stages are `pre_process`, `after_filter`,
`video`, `before_dl`, `post_process`, `after_move`, `after_video`, and
`playlist`. Values are captured at the corresponding native operation boundary;
the CLI emits them deterministically after the operation returns.

A bare field or comma-separated field list is expanded to output-template
expressions. The legacy `-g`/`--get-url`, `-e`/`--get-title`, `--get-id`,
`--get-thumbnail`, `--get-description`, `--get-duration`, `--get-filename`,
and `--get-format` aliases are also supported in pinned output order. Optional
legacy fields are omitted when unavailable.

Print rules imply quiet mode unless the user explicitly selects `--no-quiet`.
Rules through the `video` stage imply simulation unless `--no-simulate` is
given. A later lifecycle stage requires the normal operation to proceed, as in
the pinned reference. Simulation still suppresses every filesystem artifact.

All rules are bounded by the native output-template engine and validated before
related-file or media side effects. Context cancellation is honored while
capturing and writing output.

Known deviations:

- console output is buffered in the result and emitted after the full operation
  rather than streamed at each lifecycle instant;
- `post_process` and `after_move` both observe the final native pipeline path
  because this port atomically publishes postprocessed media as one operation;
- `--print-to-file`, dict shorthand, trailing `=` diagnostic shorthand, and
  upstream-only output-template syntax remain pending;
- the `formats_table`, `thumbnails_table`, `subtitles_table`, and
  `automatic_captions_table` synthetic print fields are not exposed.
