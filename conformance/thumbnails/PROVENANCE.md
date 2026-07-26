# Thumbnail sidecar provenance

Behavior is derived from the pinned read-only yt-dlp reference at commit
`aefce1eea4d0b6bab1ec2bd3beff09bff91a39c8`:

- `yt_dlp/YoutubeDL.py`, `_write_thumbnails`, defines best-versus-all traversal,
  extension selection, IDs in multi-thumbnail filenames, and metadata
  `filepath` updates;
- `yt_dlp/options.py` defines write, write-all, reset, and listing semantics;
- `yt_dlp/utils/_utils.py`, `OUTTMPL_TYPES`, attributes `thumbnail` and
  `pl_thumbnail` templates.

All repository fixtures are synthetic local HTTP responses. They cover
single-image promotion, deterministic ordering, best fallback, all-image and
playlist naming, typed paths, listing simulation, configuration reset,
redirect safety, cancellation, existing destinations, traversal, bounds, and
thumbnail-local header isolation, non-fatal remote exhaustion, and fuzzed
URL/filename policy. No production URL, account, cookie, credential, or
reference runtime is used.
