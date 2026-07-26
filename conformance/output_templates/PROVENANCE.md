# Per-type output-template provenance

The routing model is derived from the pinned read-only yt-dlp reference at
commit `aefce1eea4d0b6bab1ec2bd3beff09bff91a39c8`:

- `yt_dlp/YoutubeDL.py`, `_prepare_filename`, selects an exact artifact type
  and otherwise falls back to `default`;
- `yt_dlp/utils/_utils.py`, `OUTTMPL_TYPES`, supplies forced extensions for
  description and JSON artifacts; and
- the subtitle and link write paths prepare their typed filename before adding
  the language or shortcut extension.

The Go port exposes only artifact types it can currently produce:
`default`, `subtitle`, `description`, `infojson`, `link`,
`pl_description`, and `pl_infojson`. Recognized upstream types whose artifact
producer is not implemented fail before extraction; an unmatched colon prefix
remains an untyped default template, preserving upstream and legacy behavior.
The compatibility fallback adds the pre-existing singular Go
`OutputTemplate` between typed `default` and the built-in template so existing
API callers do not change behavior.

All tests are deterministic and synthetic. They exercise API and repeatable
CLI routing, configuration precedence, entry/playlist separation, forced
extensions, traversal and symlink rejection, simulation, and fuzzed type
parsing. No reference runtime, network traffic, credentials, or production
media is used.
