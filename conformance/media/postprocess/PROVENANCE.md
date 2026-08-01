# Phase 2 post-processing fixtures

The behavioral scope is derived from the read-only yt-dlp reference checkout
at `/Users/tejas/projects/yt-dlp-reference`, commit
`aefce1eea4d0b6bab1ec2bd3beff09bff91a39c8`:

- `yt_dlp/postprocessor/ffmpeg.py` covers audio extraction/conversion,
  subtitle conversion/embedding, metadata, merger/fixups, thumbnails,
  chapters, and concat.
- `yt_dlp/postprocessor/embedthumbnail.py`, `modify_chapters.py`, and
  `movefilesafterdownload.py` establish adjacent semantics.

The recode and automatic metadata slice derives its option normalization and
postprocessor order from `yt_dlp/__init__.py:555,663-704`, its public flags from
`yt_dlp/options.py:1633-1647,1699-1716`, and its argv/field mapping from
`yt_dlp/postprocessor/ffmpeg.py:538-580,662-790`. The implementation keeps the
pinned target-mapping surface but adds explicit resource and container bounds;
it does not expose arbitrary ffmpeg arguments.

The product subtitle-embedding slice additionally derives its exact
enable/disable flags from `yt_dlp/options.py:1683-1690`, implicit selection and
retention policy from `yt_dlp/__init__.py:674-681`, and bounded container,
mapping, metadata, replacement, and cleanup behavior from
`yt_dlp/postprocessor/ffmpeg.py:581-658` in that pinned checkout.

The lifecycle-control slice derives `--keep-video` and
`--post-overwrites` from `yt_dlp/options.py:1667-1681` and the deferred
deletion/overwrite behavior from `yt_dlp/YoutubeDL.py:3808-3856`. The Go
implementation keeps final-output overwrite separate from postprocessor
destination overwrite, records media ownership in the active output
transaction, and retires a source only after its successor has committed.
`Request.PostOverwrites == nil` selects the pinned default of allowing a
postprocessor overwrite; an explicit false value fails closed.

No upstream fixture is copied and no test executes Python. Tests create tiny
license-free audio, video, image, and subtitle inputs with ffmpeg's `lavfi`
generators, then assert ffprobe-visible stream/container semantics, including
two ordered subtitle tracks, language/name metadata, MP4 `mov_text`, WebM VTT
policy, replacement, recode mapping/no-op behavior, canonical metadata and
post-cut chapters, cancellation, and cleanup. The source checkout is
provenance only and never a build, runtime, or test dependency.

Lifecycle product coverage is in
`pkg/ytdlp/postprocess_lifecycle_product_test.go` and the CLI inventory
coverage is in `internal/cli/run_test.go`. The fixture matrix covers success,
retention, postprocessor failure rollback, cancellation after a committed
successor, overwrite rejection followed by retry, multi-output cleanup with
`--no-part`, and simulate/skip suppression. This PR does not add shell hooks,
`--exec`, `--postprocessor-args`, plugin postprocessors, concat, or chapter
splitting surfaces.
