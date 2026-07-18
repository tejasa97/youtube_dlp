# Cache fixture provenance

The path expectation was derived from `yt_dlp/cache.py` in the read-only
upstream checkout at commit
`aefce1eea4d0b6bab1ec2bd3beff09bff91a39c8`.

Upstream validates section names with `^[\w.-]+$` and encodes a UTF-8 key with
`urllib.parse.quote(key, safe='').replace('%', ',')`. The Go store uses the same
ASCII filename convention, with a `.cache` suffix for its bounded binary
envelope. The checksummed envelope, expiry timestamps, atomic replacement,
strict symlink handling, and per-entry/per-namespace resource limits are
Go-port extensions.

For platform-stable paths, the port restricts namespaces to the ASCII subset
`[A-Za-z0-9_.-]`; Python's Unicode-aware `\w` also accepts non-ASCII letters.
Cache payload files are disposable and are not migrated from upstream's JSON
envelope. Multiple goroutines using one Store are serialized for hard resource
bounds; independent processes retain atomic last-writer-wins behavior but can
temporarily race near a configured namespace cap.

No secret or live service data is present. Neither Python nor the reference
checkout is required by production or tests.
