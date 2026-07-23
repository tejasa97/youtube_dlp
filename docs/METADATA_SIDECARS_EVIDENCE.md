# Metadata sidecar evidence

`ytdlp-go` can write normalized metadata, descriptions, and internet shortcuts
without downloading media. The public Go request uses `RelatedFileOptions`; the
CLI exposes `--write-info-json`, `--write-description`, `--write-link`,
`--write-url-link`, `--write-webloc-link`, and `--write-desktop-link`.

Related files use the normal confined output template and are reported as
artifacts. `--skip-download` permits explicitly requested files, while
simulation suppresses them. Existing regular files are retained unless
`--force-overwrites` is set. Temporary files are written beside the destination
and published atomically; symlink and non-regular destinations fail closed.

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
- per-type output-template dictionaries are not yet exposed, so related files
  derive from the single configured output template;
- Windows replacement of an existing related file follows Go's native rename
  guarantees and may fail closed where atomic replacement is unavailable;
- thumbnail, annotation, and comment-specific sidecars are separate roadmap
  work.
