# Chromium cookie import

The Phase 1 pilot imports Google Chrome cookies on macOS without Python, cgo,
or a system SQLite library. It is opt-in and runs only when a request sets
`CookiesFromBrowser` or the CLI receives `--cookies-from-browser`.

## Usage

Use the default Chrome profile:

```sh
ytdlp-go --cookies-from-browser chrome URL
```

Or select one profile directory by name:

```sh
ytdlp-go --cookies-from-browser "chrome:Profile 1" URL
```

Profile names cannot contain path separators. Arbitrary database paths are
available only to the internal adapter and its deterministic tests, not through
the public CLI.

## Security model

The importer locates `Network/Cookies` (or the older `Cookies` location) under
the explicitly selected profile. It rejects symlinks and non-regular or
oversized databases, creates a private mode-0700 temporary directory, and
copies the database and active WAL before opening the copy query-only. The copy
is deleted before the operation continues.

Encrypted `v10` values use Chrome's macOS PBKDF2-HMAC-SHA1 and AES-CBC format.
The password is requested from the `Chrome Safe Storage` Keychain item through
the absolute `/usr/bin/security` path without a shell. Key and password buffers
are zeroed after use where Go permits. Database version 24 and later also
validate the SHA-256 host digest stored inside the encrypted value.

Cookie names and values, profile paths, Keychain backend messages, and raw
SQLite errors are excluded from events and rendered errors. A bounded event
reports counts only. If a subset cannot be decrypted, usable cookies continue
to extraction and the skipped count is reported. If no cookies can be loaded,
the operation fails in the authentication category.

## Current boundary

Only Google Chrome on macOS is claimed compatible. Other Chromium-family
brands, Windows DPAPI/App-Bound encryption, and Linux Secret Service/KWallet
adapters require separate versioned profiles and fixtures. CI uses synthetic
databases and an injected key provider; it never reads a real browser profile
or invokes Keychain. Real-profile access is therefore an explicit local action.
