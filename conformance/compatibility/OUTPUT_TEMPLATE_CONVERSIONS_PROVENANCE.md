# Output-template conversion provenance

This increment is pinned to
[yt-dlp commit `aefce1eea4d0b6bab1ec2bd3beff09bff91a39c8`](https://github.com/yt-dlp/yt-dlp/tree/aefce1eea4d0b6bab1ec2bd3beff09bff91a39c8).
The sources were captured by direct inspection at that revision.

Sources captured by direct file inspection:

- `README.md`, **OUTPUT TEMPLATE**, “More Conversions” and “Unicode
  normalization”.
- `test/test_YoutubeDL.py`, output-template tests around lines 720–850.
- `yt_dlp/YoutubeDL.py`, `prepare_outtmpl` conversion dispatch around lines
  1470–1505.
- `yt_dlp/utils/_utils.py`, `escapeHTML`, `format_decimal_suffix`,
  `sanitize_filename`, `shell_quote`, and `variadic`.

Implemented scope covers `l`, `h`, `U`, `D`, `B`, `c`, `q`, `S`, `r`, and
`a`. The Go implementation uses the repository's existing ordered values and
hard allocation limits. Unicode normalization and restricted filename
normalization use `golang.org/x/text/unicode/norm`.

Bounded deviations:

- Nested list/object/bytes values in `l` are rejected instead of exposing a
  Python or Go representation.
- String width and precision for `l`, `h`, and `U` are Unicode-code-point
  based so UTF-8 is never split.
- Negative and non-finite `D` inputs, out-of-range integer-style results, and
  oversized intermediate output are categorized errors.
- Python `repr`/`ascii` is reproduced for the repository value model, not for
  arbitrary Python extension objects.
- Shell quoting follows the pinned POSIX and Windows algorithms selected at
  build target runtime; it does not execute a shell.
- Arbitrary Python formatting, locale formatting, and full output-template
  parity are not claimed.
