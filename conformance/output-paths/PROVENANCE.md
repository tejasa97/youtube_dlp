# Per-type output-path provenance

This corpus derives its routing model from the read-only yt-dlp checkout at
commit `aefce1eea4d0b6bab1ec2bd3beff09bff91a39c8`.

Primary reference locations:

- `yt_dlp/options.py:1397-1410` defines repeatable `-P` / `--paths
  [TYPES:]PATH`, the default `home` key, and the relationship to output-template
  types.
- `yt_dlp/YoutubeDL.py:1217-1224` resolves an exact typed directory beneath
  `home` and otherwise falls back to `home`.
- `yt_dlp/utils/_utils.py:2877-2890` declares the output-template type names
  used by path routing.

The deterministic Go fixtures exercise only artifact producers implemented by
this port: media (`home`), subtitles, thumbnails, descriptions, info JSON,
links, playlist descriptions, playlist info JSON, and playlist thumbnails.
They cover repeatable and comma-separated CLI values, configuration versus
command-line precedence, exact-type routing, home fallback, unknown-prefix and
Windows-drive parsing, inherited-path clearing, validation ordering,
cancellation, and direct or nested symlink rejection from the home boundary.

No reference Python code executes during builds or tests. No production URL,
credential, cookie, token, or captured user traffic is included.

Explicit deviations:

- non-home directories must be relative children beneath `home`, preserving
  this port's single-root confinement rather than allowing an absolute typed
  path to replace home;
- `temp` is excluded because safely separating partial downloads and
  postprocessor staging requires a distinct lifecycle;
- `chapter`, `annotation`, and `pl_video` are excluded because this port does
  not currently produce those independently routed artifacts.
