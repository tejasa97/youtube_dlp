# Phase 2 post-processing lane

`internal/media/ffmpeg` is the sole external-tool boundary. It starts ffmpeg
and ffprobe with argument vectors (never a shell), uses an explicitly bounded
environment and diagnostics, starts a separate process group on Unix, and
atomically finalizes outputs. Cancellation kills the supervised process tree;
failed and cancelled work removes `.part` outputs.

Each invocation receives a unique same-directory temporary filename, so
concurrent operations cannot collide. Existing destination symlinks and
non-regular files are rejected. Concat accepts bounded, existing local regular
files only; it cannot turn a URL or protocol string into an ffmpeg input.

`internal/media/postprocess` represents work as typed operations over typed
artifacts. Supported operations are audio extraction, subtitle and thumbnail
conversion, metadata and chapter embedding, thumbnail and subtitle embedding, compatibility fixups,
concat, and safe file moves. An owned input is removed only after its replacement
has been atomically finalized. Metadata and media-option values are validated;
there is no command-string API.

Known deviations: chapter writing uses explicit millisecond `ffmetadata`
chapters and preserves supplied boundaries/titles; yt-dlp's more extensive
chapter removal and sponsor-block mutation workflows are not part of this lane
yet. Thumbnail embedding depends on ffmpeg/container support and reports the
categorized media failure without altering the input.

Safe cross-device moves stream through a private temporary file, honor context
cancellation, sync before publish, and retain the source until publication. On
Windows, overwriting an existing move destination is refused because the Go
rename primitive cannot provide the same atomic replacement guarantee there.
Hardlink-count inspection is intentionally not enforced cross-platform: callers
must treat an `Owned` artifact as exclusively owned before asking the graph to
delete it.
