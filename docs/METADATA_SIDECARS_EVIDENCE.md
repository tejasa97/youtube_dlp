# Metadata sidecar evidence

`ytdlp-go` can write normalized metadata, descriptions, and internet shortcuts
without downloading media. The public Go request uses `RelatedFileOptions`; the
CLI exposes `--write-info-json`, `--write-description`, `--write-link`,
`--write-url-link`, `--write-webloc-link`, and `--write-desktop-link`.

Related files are reported as artifacts. The public API accepts
`OutputTemplates`, and repeatable `--output TYPE:TEMPLATE` values configure
`thumbnail`, `description`, `infojson`, `link`, `pl_thumbnail`,
`pl_description`, and `pl_infojson`
independently. An exact type falls back to `default`, then the legacy Go
`OutputTemplate`, then the built-in template. `--skip-download` permits
explicitly requested files, while simulation suppresses them. Existing regular
files are retained unless `--force-overwrites` is set. Temporary files are
written beside the destination and published atomically; symlink and
non-regular destinations fail closed.

Repeatable `--paths` values and the public `OutputPaths` map independently
route `description`, `infojson`, `link`, `pl_description`, and `pl_infojson`
under the common home directory. A missing exact path falls back to home.
Typed paths must remain relative and confined beneath home.

The implementation follows the pinned upstream ordering and file formats for
video metadata, descriptions, and `.url`, `.webloc`, and `.desktop` shortcuts.
Shortcut URLs are limited to bounded HTTP(S) URLs without credentials or
control characters. XML and desktop-entry values are escaped for their target
formats.

Playlist `.info.json` and `.description` files contain the final selected
entries and are enabled by default when their corresponding write option is
selected. `--no-write-playlist-metafiles` suppresses playlist-level files.
Internet shortcuts remain video-only, matching the pinned processing path.

Known deviations:

- the Go metadata JSON is the port's deterministic normalized schema rather
  than every private Python `YoutubeDL` field;
- annotation, chapter, and other upstream output-template types are
  rejected until their corresponding artifact producers exist;
- Windows replacement of an existing related file follows Go's native rename
  guarantees and may fail closed where atomic replacement is unavailable;
- annotation and comment-specific sidecars are outside the current
  capability claim.
