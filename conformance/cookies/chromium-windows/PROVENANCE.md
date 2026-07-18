# Chromium Windows cookie corpus

The behavioral boundary is derived from the pinned reference
`yt-dlp/yt-dlp@aefce1eea4d0b6bab1ec2bd3beff09bff91a39c8`, specifically
`yt_dlp/cookies.py`'s Windows Chromium loader: the Local State key is base64
decoded, its `DPAPI` marker is removed, Windows DPAPI unwraps the remainder,
and `v10` cookie values use a 12-byte nonce and 16-byte AES-GCM tag. Database
schema version 24 and later prefix plaintext with `SHA256(host_key)`.

The vector is synthetic and contains no browser or user data. It was generated
independently with Node's AES-256-GCM implementation from the published inputs
in `vectors.json`: the UTF-8 32-byte key, nonce, host digest, and expected value.
The Go test consumes the fixed ciphertext; it does not regenerate its own
expectation.

`v11` uses the same framed AES-GCM boundary in this implementation. Chromium's
`v20` app-bound format is intentionally tested through an injected app-bound
decryptor because a portable standalone process cannot bypass browser/application
identity binding. Neither claim is attributed to support in the pinned reference.
