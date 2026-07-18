# Native netrc fixture provenance

`valid.netrc` is synthetic and was authored for the Go port. Its usernames,
accounts, hosts, and passwords are non-production fixture values. It contains
no captured credential, site response, executable, or Python-derived byte
sequence.

The grammar expectations were derived from the read-only pinned yt-dlp checkout
at `/Users/tejas/projects/yt-dlp-reference`, commit
`aefce1eea4d0b6bab1ec2bd3beff09bff91a39c8`, specifically
`yt_dlp/extractor/common.py` netrc lookup behavior, `yt_dlp/utils/_utils.py`
`netrc_from_content`, and `test/testdata/netrc/netrc`. Python's standard-library
netrc parser was inspected only to understand the grammar used by that pinned
code; no implementation or fixture bytes were copied.

Derived expectations include `machine` and `default`, `user` as a `login`
alias, login/account/password tuples, empty quoted values, shlex-like quoting
and escaping, comments, final duplicate precedence, and `macdef` body
termination at a blank line.
