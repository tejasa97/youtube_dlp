# Phase 2 post-processing fixtures

The behavioral scope is derived from the read-only yt-dlp reference checkout
at `/Users/tejas/projects/yt-dlp-reference`, commit
`aefce1eea4d0b6bab1ec2bd3beff09bff91a39c8`:

- `yt_dlp/postprocessor/ffmpeg.py` covers audio extraction/conversion,
  subtitle conversion/embedding, metadata, merger/fixups, thumbnails,
  chapters, and concat.
- `yt_dlp/postprocessor/embedthumbnail.py`, `modify_chapters.py`, and
  `movefilesafterdownload.py` establish adjacent semantics.

The product subtitle-embedding slice additionally derives its exact
enable/disable flags from `yt_dlp/options.py:1683-1690`, implicit selection and
retention policy from `yt_dlp/__init__.py:674-681`, and bounded container,
mapping, metadata, replacement, and cleanup behavior from
`yt_dlp/postprocessor/ffmpeg.py:581-658` in that pinned checkout.

No upstream fixture is copied and no test executes Python. Tests create tiny
license-free audio, video, image, and subtitle inputs with ffmpeg's `lavfi`
generators, then assert ffprobe-visible stream/container semantics, including
two ordered subtitle tracks, language/name metadata, MP4 `mov_text`, WebM VTT
policy, replacement, cancellation, and cleanup. The source checkout is
provenance only and never a build, runtime, or test dependency.
