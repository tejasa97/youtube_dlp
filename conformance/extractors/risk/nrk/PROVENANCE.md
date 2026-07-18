# NRK risk fixtures

Synthetic playback API fixtures authored on 2026-07-18 from
`yt_dlp/extractor/nrk.py` at pinned upstream commit
`aefce1eea4d0b6bab1ec2bd3beff09bff91a39c8`. Example media hosts and invented
programme IDs avoid any production or user-derived data.

Known deviation: the Go scope supports direct programme/channel playback and
series/season catalog playlists. It does not expand all legacy NRK URL aliases,
podcast-specific catalogs, parts, or CDN hostname fallback rewriting. Encrypted
assets are rejected, and geo/auth/unavailable states fail closed.
