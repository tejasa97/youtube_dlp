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

`--print-to-file [WHEN:]TEMPLATE FILE` uses the same stage and field shorthand
but appends each rendered record to a confined filename template. In the Go
API, set `PrintRule.FileTemplate`. File rules neither emit console output nor
implicitly select quiet or simulation. An explicit simulation suppresses the
file, consistent with the port's global no-artifact invariant.

A bare field or comma-separated field list is expanded to output-template
expressions. Dictionary shorthand such as `{id,title}` emits one ordered JSON
object and omits unavailable fields. A trailing `=` emits the selected field
name and JSON-formatted value; it works with field lists and dictionaries and
uses indented JSON for composite values. These forms also work with
`--print-to-file`. The legacy `-g`/`--get-url`, `-e`/`--get-title`, `--get-id`,
`--get-thumbnail`, `--get-description`, `--get-duration`, `--get-filename`,
and `--get-format` aliases are supported in pinned output order. Optional
legacy fields are omitted when unavailable.

Print rules imply quiet mode unless the user explicitly selects `--no-quiet`.
Rules through the `video` stage imply simulation unless `--no-simulate` is
given. A later lifecycle stage requires the normal operation to proceed, as in
the pinned reference. Simulation still suppresses every filesystem artifact.

All rules and filenames are bounded by the native output-template engine.
Print files reject traversal, nested symlink parents, symlink/non-regular
destinations, and oversized records. Linux and macOS opens use `O_NOFOLLOW`;
records use a single append write so concurrent operations cannot share a
mutable offset. Context cancellation is honored while capturing and writing
output.

Known deviations:

- console output is buffered in the result and emitted after the full operation
  rather than streamed at each lifecycle instant;
- `post_process` and `after_move` both observe the final native pipeline path
  because this port atomically publishes postprocessed media as one operation;
- upstream-only output-template syntax beyond the bounded native parser remains
  pending;
- the `formats_table`, `thumbnails_table`, `subtitles_table`, and
  `automatic_captions_table` synthetic print fields are not exposed.
