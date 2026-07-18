# Firefox cookie import provenance

The schema and expected behavior are derived from `_extract_firefox_cookies`
and `_firefox_browser_dirs` in `yt_dlp/cookies.py` at pinned read-only commit
`aefce1eea4d0b6bab1ec2bd3beff09bff91a39c8`.

Synthetic SQLite databases cover old optional-column schemas, schema 16+
millisecond expiry, WAL-safe copying, Firefox containers, session cookies,
security flags, cancellation, and malformed data. No real browser profile or
credential is used.

Discovery checks the pinned reference's platform roots and common direct,
single-child, and `Profiles/*` layouts, selecting the newest database. It does
not parse `profiles.ini` aliases; callers can pass `ProfileDir` or
`DatabasePath` for nonstandard layouts.
