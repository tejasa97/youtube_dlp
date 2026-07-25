# Phase 2 compatibility-language fixtures

These compact, hand-authored fixtures derive behavioural expectations from the
read-only pinned checkout at `/Users/tejas/projects/yt-dlp-reference`, commit
`aefce1eea4d0b6bab1ec2bd3beff09bff91a39c8`. They are not executed against
that checkout and add no Python runtime, build, or test dependency.

Reference loci:

- `yt_dlp/YoutubeDL.py` lines 1207-1524 and 2205-2650 cover output
  templates and format selection;
- `yt_dlp/utils/_utils.py` lines 1776-1845, 2096-2153, and 3239-3355
  cover filesize parsing, duration parsing, the match-filter grammar, and the
  interactive marker;
- `yt_dlp/options.py` lines 742-767 describe the match-filter CLI contract;
- `yt_dlp/utils/_utils.py` lines 3331-3351 and `yt_dlp/YoutubeDL.py` lines
  1641-1672 describe breaking-filter ordering and queue cancellation;
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

The Go product layer evaluates breaking filters before ordinary filters and
stops playlist expansion before retaining the rejected entry. Exact
`--match-filter -` prompting occurs only for complete entries after those
filters pass; flat/incomplete and archived entries do not prompt. A breaking
`-` prompt runs first and an accepted answer bypasses ordinary filters, matching
the pinned behavior. Automatic subtitle listing suppresses prompting, explicit
simulation does not, and EOF becomes a categorized cancellation instead of an
uncaught input error. Prompt filenames include the selected single-output
format, only the pinned `_format_fields` are treated as incomplete during the
initial check, and those conditions are reevaluated against the original
selected-format metadata plus normalized selection fields,
and no-format entries fail before prompting. Merged A/V fields follow the
pinned merge policy; multi-output interactive plans fail explicitly because
per-output prompting is not claimed. The core grammar
remains free of queue and prompting policy. Regular expressions use Go's
bounded RE2 engine; Python-only
look-around and backreference syntax is rejected explicitly.
