# Match-filter pinned corpus provenance

This fixture is a hand-authored, deterministic statement of behavior observed
from the read-only yt-dlp checkout at
`aefce1eea4d0b6bab1ec2bd3beff09bff91a39c8`. The normative implementation is
`yt_dlp/utils/_utils.py`, `_match_one`, `match_str`, and `match_filter_func`
(lines 3239-3355); the command-line contract is in `yt_dlp/options.py`
(lines 742-767).

No Python executable is run by Go tests, builds, or the Docker image. The
fixture contains small attributable expectations, not copied upstream tests or
captured upstream output. Maintainers may reproduce an oracle investigation
only with the stated commit and a supported pinned interpreter; any resulting
fixture update must record its command, interpreter version, and date here.

The contract deliberately excludes malformed metadata and resource-exhausting
regular expressions. Those inputs fail closed with sanitized local errors.
