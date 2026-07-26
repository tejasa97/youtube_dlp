# Thumbnail sidecar provenance

Behavior is derived from the pinned read-only yt-dlp reference at commit
`aefce1eea4d0b6bab1ec2bd3beff09bff91a39c8`:

- `yt_dlp/YoutubeDL.py`, `_write_thumbnails`, defines best-versus-all traversal,
  extension selection, IDs in multi-thumbnail filenames, and metadata
  `filepath` updates;
- `yt_dlp/options.py` defines write, write-all, reset, and listing semantics;
- `yt_dlp/options.py`, `create_mapping_re`, defines the ordered conditional
  mapping accepted by `--convert-thumbnails`;
- `yt_dlp/postprocessor/ffmpeg.py`, `FFmpegThumbnailsConvertorPP`, defines
  `jpeg` normalization, first-match resolution, same-format skipping, metadata
  updates, and source cleanup after successful conversion;
- `yt_dlp/options.py` and `yt_dlp/__init__.py` define
  `--embed-thumbnail`/`--no-embed-thumbnail`, implicit best-image writing,
  and explicit sidecar retention;
- `yt_dlp/postprocessor/embedthumbnail.py`, `EmbedThumbnailPP`, defines best
  on-disk selection, format correction, supported container families, and
  cleanup only after successful embedding;
- `yt_dlp/utils/_utils.py`, `OUTTMPL_TYPES`, attributes `thumbnail` and
  `pl_thumbnail` templates.

All repository fixtures are synthetic local HTTP responses. They cover
single-image promotion, deterministic ordering, best fallback, all-image and
playlist naming, typed paths, listing simulation, configuration reset,
redirect safety, cancellation, existing destinations, traversal, bounds, and
thumbnail-local header isolation, non-fatal remote exhaustion, and fuzzed
URL/filename policy. Conversion fixtures also cover ordered mapping, CLI and
configuration precedence, no-op behavior, metadata/artifact accounting,
content-aware WebP correction, correction collisions, conversion and cleanup
failures, cancellation-safe ownership, and fuzzed mapping/path confinement. No
production URL, account, cookie, credential, Python runtime, or reference
runtime is used.

Generated license-free media verifies attached-picture output and replacement
for MP3, MP4-family, Matroska, FLAC, Ogg, and Opus containers. Product and CLI
fixtures cover mislabeled WebP correction, merged-WebM promotion, implicit and
explicit ownership, temporary conversion, flag/configuration reset,
missing/unsupported inputs, cancellation, cleanup warnings, multi-output
rejection, metadata mutation, mtime retention, and exact artifact/byte
accounting. Xiph picture serialization is derived from the FLAC metadata block
picture format used by the pinned `EmbedThumbnailPP`; fixtures are generated
locally and require no Python or reference runtime.
