# Post-processing boundary

`internal/media/ffmpeg` is the sole external-tool boundary. It starts ffmpeg
and ffprobe with argument vectors (never a shell), uses an explicitly bounded
environment and diagnostics, starts a separate process group on Unix, and
atomically finalizes outputs. Cancellation terminates the entire process tree —
on Unix via process-group kill, on Windows via a tested Job Object
(KILL_ON_JOB_CLOSE). Windows children start CREATE_SUSPENDED, are assigned to
the Job, then resumed, so no user code runs before Job membership. Failed and
cancelled work removes temporary outputs.

Each invocation gets a private same-filesystem temporary directory and an
exclusive output file, so concurrent operations cannot collide or replace its
intermediate through the destination directory. Existing destination symlinks and
non-regular files are rejected. Concat accepts bounded, existing local regular
files only; it cannot turn a URL or protocol string into an ffmpeg input.

`internal/media/postprocess` represents work as typed operations over typed
artifacts. Supported operations are audio extraction, subtitle and thumbnail
conversion, bounded video recoding, metadata and chapter embedding, thumbnail and subtitle embedding, compatibility fixups,
concat, and safe file moves. In the active download lifecycle, media sources
are explicitly non-destructive graph inputs; the lifecycle transaction retires
one only after its successor has atomically committed, and keeps a reversible
snapshot until every output plan commits. Metadata and media-option values are
validated; there is no command-string API.

The public Go request contract exposes a tagged postprocessor union and returns
typed output artifacts. `Request.KeepVideo` retains successfully replaced
intermediate media, while `Request.PostOverwrites` (nil defaults to enabled)
controls only postprocessor destinations. The CLI exposes audio extraction, remuxing, bounded
`--recode-video` target mappings, automatic canonical `--embed-metadata` and
`--embed-chapters`, thumbnail conversion/embedding, and bounded multi-track subtitle embedding;
embedders can request every typed operation. Operation count and path confinement are
checked before network work begins, and product integration is covered by
generated-media ffprobe verification.

Recode follows the pinned `FFmpegVideoConvertorPP` surface: callers select only
an allowlisted target or ordered source-to-target mapping. ffmpeg selects the
encoders, with the pinned Xvid AVI exception; arbitrary codec/argument injection
is not exposed. A same-format target or unmatched mapping is a true no-op, and
when both CLI conversion modes are supplied recode wins over remux with a
warning.

Automatic metadata embedding derives only the pinned common fields from the
canonical `Info` envelope, validates per-field and aggregate bounds, and emits
keys deterministically. Chapter embedding consumes the final post-cut chapter
timeline. The product order is conversion/recode, subtitle embedding, chapter
cuts, metadata/chapter embedding, thumbnail embedding, then staged prints.
Supported automatic metadata/chapter containers are FLAC, M4A, Matroska,
MOV/MP4, MP3, Ogg/Opus, and WebM; unsupported targets fail during preflight.

Known deviations: chapter writing uses explicit millisecond `ffmetadata`
chapters and preserves supplied boundaries/titles. Automatic thumbnail
embedding covers MP3, MP4-family, Matroska, Ogg, Opus, and FLAC outputs,
replaces existing compatible cover art, and promotes merged WebM outputs to
Matroska. Xiph picture blocks are produced natively in Go and passed through a
private metadata file; no Python or mutagen runtime is used.

Safe cross-device moves stream through an exclusive temporary file, honor context
cancellation, sync before publish, and retain the source until publication. On
Windows, overwriting an existing move destination is refused because the Go
rename primitive cannot provide the same atomic replacement guarantee there;
the same restriction applies to ffmpeg post-processing finalization.
The former Windows start-then-assign race (G2-S01) is closed for ffmpeg by the
CREATE_SUSPENDED → Job assign → resume sequence. A narrow residual remains:
Go's os/exec discards the CreateProcess thread handle, so resume uses a
Toolhelp thread snapshot rather than that handle.
Hardlink-count inspection is intentionally not enforced cross-platform: callers
must treat an `Owned` artifact as exclusively owned before asking the graph to
delete it.

This lifecycle PR deliberately does not add shell hooks, `--exec`,
`--postprocessor-args`, plugin postprocessors, concat, or chapter splitting.
Simulation and skip-download remain media-write suppressors, and `.part` versus
`--no-part` remains owned by the downloader rather than postprocessor cleanup.
