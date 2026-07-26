# Thumbnail sidecar evidence

`ytdlp-go` exposes `--write-thumbnail`, `--write-all-thumbnails`,
`--no-write-thumbnail`, `--list-thumbnails`, `--convert-thumbnails`, and
`--embed-thumbnail`/`--no-embed-thumbnail`.
Listing implies simulation unless `--no-simulate` is explicit.
`--skip-download` still permits requested thumbnail files. The public API uses
`ThumbnailOptions`.

A lone `thumbnail` URL is promoted into the bounded `thumbnails` collection.
Candidates are ordered deterministically by preference, dimensions, ID, URL,
and source position. Best mode tries candidates from best to worst and stops
after the first successful image; all mode writes every valid image and adds a
bounded thumbnail ID to each filename. Declared recognized image extensions
take precedence over URL suffixes, with `jpg` as the conservative fallback.

Entry and playlist images use the `thumbnail` and `pl_thumbnail` output
template and output-path types. Repeatable `--paths` values or the public
`OutputPaths` map route either type to a relative child beneath home, with
home fallback. Paths retain output-root confinement, symlink rejection,
resumable atomic publication, overwrite policy, cancellation, request retry
limits, and a 16 MiB per-image ceiling. Redirects are bounded, loop checked,
reject credential-bearing targets and HTTPS downgrade, and remove explicit
credential headers across origins. Only thumbnail-local `http_headers` are
used; top-level media credentials are not forwarded to thumbnail hosts.
Remote candidate failures emit warnings and are removed from normalized
metadata without aborting the media operation.

Written images may be converted to `jpg`, `png`, or `webp`. The option accepts
yt-dlp-style ordered rules such as `webp>png/jpg`: the first conditional rule
matching the normalized source extension wins, otherwise the first
unconditional rule wins. `jpeg` and `jpg` compare as the same source format;
same-format and unmatched mappings are no-ops, and `none` disables conversion.
Downloaded RIFF/WebP images carrying a different declared extension are
corrected to `.webp` before mapping resolution, matching the reference's
content-aware fixup.
Conversion is argv-only through the shared ffmpeg boundary. The replacement is
published before the source is removed, metadata is updated to the committed
path and extension, and cleanup failure retains and reports both artifacts.
Mappings and declared-extension destinations are preflighted before network
work. Content-corrected destinations are confined and collision-checked before
move or conversion. ffmpeg is discovered lazily only when a downloaded image
actually requires conversion.

Embedding implicitly downloads the best thumbnail and runs only after media
download, container-changing postprocessors, chapter cuts, and subtitle
embedding. MP3, MP4/M4A/M4V/MOV, Matroska MKV/MKA, FLAC, Ogg, and Opus are
supported. Images unsupported by MP3/MP4/Xiph containers are converted to a
confined temporary PNG; Matroska attaches the recognized source image directly.
Ogg and Opus receive a bounded native FLAC-picture block through a private
ffmetadata file, avoiding Python, mutagen, shell interpolation, and large
process arguments. FLAC uses the muxer's native picture stream. Existing
same-type cover art is removed before the replacement is added, metadata and
audio packets are stream-copied, source mtime is retained, and a merged WebM
audio/video selection is planned and published as MKV before download.
Explicit
`--write-thumbnail` or `--write-all-thumbnails` retains sidecars, while an
implicitly downloaded image is removed only after the media replacement
commits. Conversion/embedding failure or cancellation preserves the original
media and image. Missing images warn and continue; unsupported media
containers and multi-output format plans fail closed.

The implementation follows the pinned reference's
`YoutubeDL._write_thumbnails`, thumbnail options, and `OUTTMPL_TYPES` routing at
commit `aefce1eea4d0b6bab1ec2bd3beff09bff91a39c8`.

Known deviations:

- existing files fail closed unless overwrite is enabled rather than being
  treated as an already-completed thumbnail;
- only recognized image extensions are accepted.
