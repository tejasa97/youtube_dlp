# Output-template language provenance

Reference: `yt-dlp` `aefce1eea4d0b6bab1ec2bd3beff09bff91a39c8`, inspected
read-only at `/path/to/yt-dlp-reference` on 2026-07-29.

## Feature gap ledger

| Feature | Pinned source | Status |
| --- | --- | --- |
| Literal text and `%%` | README OUTPUT TEMPLATE | covered |
| Traversal, indexing, slicing and projection | `YoutubeDL.prepare_outtmpl` | covered and bounded |
| Alternatives, defaults and replacements | `prepare_outtmpl` `_ReplacementFormatter` | literal, escaped-brace and bounded `{}` / `{0}` string alignment covered; arbitrary field access rejected |
| Arithmetic | `MATH_FUNCTIONS` | left-to-right `+`, `-`, `*`, finite bounded operands |
| Normal numeric conversions | `STR_FORMAT_TYPES` | `diouxXeEfFgG` covered |
| Date/time | `strftime_or_none` | YYYYMMDD and numeric Unix seconds, deterministic UTC directives covered |
| JSON, repr/ascii, Unicode and bytes | conversion dispatch | covered for repository value kinds |
| Missing/null | `outtmpl_na_placeholder` default | deterministic `NA` / explicit defaults |
| Nested/hostile expressions | traversal and renderer | source spans, depth/item/size limits, cancellation boundary |
| Lifecycle-only output types | README type list | renderer is reusable; annotation/chapter/pl_video/temp lifecycle creation remains intentionally outside this track |

## Fixture provenance

`output-template-language-phase2.yaml` is a deterministic, checked-in table
fixture exercised by `internal/compat/template`. It is source-audited against
the pinned SHA above. The available host interpreter is Python 3.9.6, while
this yt-dlp revision requires Python 3.10+, and no compliant interpreter or
pullable Python image was available in this worktree. Therefore it is **not**
represented as generated oracle output. Before asserting byte-for-byte oracle
provenance, regenerate it with Python 3.10+ using the pinned checkout and
record `python --version`, `git rev-parse HEAD`, command, and fixture SHA here.

Runtime, build, and Go tests do not invoke Python.

## Security and compatibility bounds

No Python evaluation, attribute access, item access in replacements, locale
formatting, or host-local timezone state is admitted. Rendering is bounded by
template, expression, traversal, depth, scalar and output budgets; `Resolve`
continues to sanitize and confine paths. `RenderContext` supplies a
cancellation boundary while existing callers retain `Render` behavior.
