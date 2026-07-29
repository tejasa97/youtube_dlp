# Metadata transformation pinned cases

The normative reference is read-only `yt-dlp/yt-dlp` commit
`aefce1eea4d0b6bab1ec2bd3beff09bff91a39c8`, specifically
`yt_dlp/postprocessor/metadataparser.py` and `yt_dlp/options.py`.

These deterministic normal-input vectors are source-derived, not a new oracle
capture. No Python interpreter is used by Go builds, tests, or runtime. If a
maintainer later refreshes fixture observations, the capture environment must
record the exact interpreter (`CPython 3.12.13` for the pinned lane) and the
reference SHA in the JSON fixture before replacing these cases.
