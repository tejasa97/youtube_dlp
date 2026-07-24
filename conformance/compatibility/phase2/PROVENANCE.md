# Phase 2 compatibility-language fixtures

These compact, hand-authored fixtures derive behavioural expectations from the
read-only pinned checkout at `/Users/tejas/projects/yt-dlp-reference`, commit
`aefce1eea4d0b6bab1ec2bd3beff09bff91a39c8`. They are not executed against
that checkout and add no Python runtime, build, or test dependency.

Reference loci:

- `yt_dlp/YoutubeDL.py` lines 1207-1524 and 2205-2650 cover output
  templates and format selection;
- `yt_dlp/utils/_utils.py` lines 1776-1845, 2096-2153, and 3239-3355
  cover filesize parsing, duration parsing, and the non-interactive
  match-filter grammar;
- `yt_dlp/options.py` lines 742-767 describe the match-filter CLI contract;
- `test/test_YoutubeDL.py` lines 908-991 provide the attributable
  match-filter behavior matrix; and
- `yt_dlp/utils/_utils.py` lines 5498-5615 and
  `yt_dlp/postprocessor/metadataparser.py` cover the remaining Phase 2
  compatibility-language corpus.

`matchfilter.yaml` version 2 records hand-authored expectations for unary
presence checks, OR/AND composition, none-inclusive and incomplete-field
semantics, escaped ampersands, Unicode quoted values, negated string
operators, and bounded filesize/duration coercion. It does not copy upstream
fixtures.

The Go implementation deliberately excludes interactive `-` prompting and
break-filter queue control flow, which belong at the product/CLI layer.
Regular expressions use Go's bounded RE2 engine; Python-only look-around and
backreference syntax is rejected explicitly.
