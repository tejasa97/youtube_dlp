# Kick risk fixtures

These are deterministic synthetic API responses authored on 2026-07-18 from
`yt_dlp/extractor/kick.py` at upstream commit
`aefce1eea4d0b6bab1ec2bd3beff09bff91a39c8`. All hosts and identities are
reserved examples. The fixtures contain no session token or signed production
manifest.

Known deviation: the Go extractor relies on the transport's cookie jar and does
not materialize Kick's `session_token` into an Authorization header. This keeps
credentials outside extractor metadata and diagnostics. Live, VOD, and clip
API shapes are supported and every API request requires the named impersonation
profile.
