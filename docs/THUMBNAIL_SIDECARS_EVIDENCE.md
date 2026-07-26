# Thumbnail sidecar evidence

`ytdlp-go` exposes `--write-thumbnail`, `--write-all-thumbnails`,
`--no-write-thumbnail`, and `--list-thumbnails`. Listing implies simulation
unless `--no-simulate` is explicit. `--skip-download` still permits requested
thumbnail files. The public API uses `ThumbnailOptions`.

A lone `thumbnail` URL is promoted into the bounded `thumbnails` collection.
Candidates are ordered deterministically by preference, dimensions, ID, URL,
and source position. Best mode tries candidates from best to worst and stops
after the first successful image; all mode writes every valid image and adds a
bounded thumbnail ID to each filename. Declared recognized image extensions
take precedence over URL suffixes, with `jpg` as the conservative fallback.

Entry and playlist images use the `thumbnail` and `pl_thumbnail` output
template types. Paths retain output-root confinement, symlink rejection,
resumable atomic publication, overwrite policy, cancellation, request retry
limits, and a 16 MiB per-image ceiling. Redirects are bounded, loop checked,
reject credential-bearing targets and HTTPS downgrade, and remove explicit
credential headers across origins. Only thumbnail-local `http_headers` are
used; top-level media credentials are not forwarded to thumbnail hosts.
Remote candidate failures emit warnings and are removed from normalized
metadata without aborting the media operation.

The implementation follows the pinned reference's
`YoutubeDL._write_thumbnails`, thumbnail options, and `OUTTMPL_TYPES` routing at
commit `aefce1eea4d0b6bab1ec2bd3beff09bff91a39c8`.

Known deviations:

- existing files fail closed unless overwrite is enabled rather than being
  treated as an already-completed thumbnail;
- only recognized image extensions are accepted;
- conversion and media-container embedding are separate postprocessor work.
