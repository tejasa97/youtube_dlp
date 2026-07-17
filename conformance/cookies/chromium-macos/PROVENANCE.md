# Chromium macOS cookie corpus

The first `v10` vector and its expected plaintext come from the cookie tests in
the pinned reference `yt-dlp/yt-dlp@aefce1eea4d0b6bab1ec2bd3beff09bff91a39c8`.
The key derivation is Chromium's documented PBKDF2-HMAC-SHA1 construction with
salt `saltysalt`, 1003 iterations, a 16-byte key, and a 16-space AES-CBC IV.

The version-24 vector was generated independently with OpenSSL from
`SHA256(".example.com") || "meta-cookie"`, PKCS#7 padding, and the published
derived key for password `abc`. It proves both prefix stripping and host-digest
validation rather than merely round-tripping this implementation's encryptor.

The SQLite integration tests generate synthetic databases from these values at
test time. Domains use `.example.com`, cookie values are artificial, Keychain access
is replaced by an injected provider, and no real browser profile is read in CI.
The live importer invokes `/usr/bin/security` only after the user explicitly
requests browser-cookie import.
