# Configuration conformance provenance

Reference: `yt-dlp/yt-dlp@aefce1eea4d0b6bab1ec2bd3beff09bff91a39c8`.

The discovery order and precedence expectations were manually derived from
`yt_dlp/options.py:43-111`, `yt_dlp/utils/_utils.py:4751-4768`, and
`yt_dlp/utils/_utils.py:4934-5034`, cross-checked against
`test/test_config.py`. Token quoting, escaping, comments, recursive relative
`--config-locations`, duplicate path suppression, and dynamic aliases were
derived from the same `Config` implementation and `options.py:313-353`.

`root.conf`, `child.conf`, and `expected.json` are newly authored synthetic
inputs containing no upstream text or credentials. They encode the observed
behavior in a deterministic, network-free form. BOM and malformed encoding
cases are generated in Go tests to preserve exact byte sequences.

Known bounded deviations are documented in package tests and API comments:
locale-dependent implicit encodings are intentionally rejected in favor of
UTF-8 unless a supported BOM or coding declaration is present; the public
alias formatter supports positional numeric fields and escaped braces, not
Python's full formatting mini-language; and the caller supplies the resolved
`--paths home:` directory through `Environment.HomeConfigDir`.
