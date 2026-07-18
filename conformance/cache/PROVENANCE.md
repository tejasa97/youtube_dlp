# Cache fixture provenance

The path expectation was derived from `yt_dlp/cache.py` in the read-only
upstream checkout at commit
`aefce1eea4d0b6bab1ec2bd3beff09bff91a39c8`.

Upstream validates section names with `^[\w.-]+$` and encodes a UTF-8 key with
`urllib.parse.quote(key, safe='').replace('%', ',')`. The Go store uses the same
ASCII filename convention, with a `.cache` suffix for its bounded binary
envelope. The envelope, expiry timestamps, atomic replacement, strict symlink
handling, and resource limits are Go-port extensions.

No secret or live service data is present. Neither Python nor the reference
checkout is required by production or tests.
