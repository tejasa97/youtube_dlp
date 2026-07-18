# Netscape cookie-file fixture provenance

The deterministic rows in `internal/cookies/netscape/netscape_test.go` were
derived from the load/save behavior of `yt_dlp/cookies.py`, class
`YoutubeDLCookieJar`, in the pinned read-only reference commit
`aefce1eea4d0b6bab1ec2bd3beff09bff91a39c8`.

The expectations cover the seven-column Netscape format, `#HttpOnly_`, the
domain include-subdomains bit, `TRUE`/`FALSE` secure flags, fractional expiry,
and the reference's treatment of expiry zero as a session cookie. Values and
domains are synthetic and contain no account data.
