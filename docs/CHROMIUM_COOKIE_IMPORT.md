# Browser cookie import

Browser-cookie import is opt-in and remains native Go: no Python, cgo, shell,
or live browser process is required. It runs only when a request sets
`CookiesFromBrowser` or the CLI receives `--cookies-from-browser`.

## Supported sources

| Platform | Sources |
| --- | --- |
| macOS | Chrome, Firefox, and Safari |
| Linux | Chrome, Chromium, Brave, and Firefox |
| Windows | Chrome, Chromium, Edge, Brave, Vivaldi, Opera, and Firefox |

These are bounded profile importers, not a promise that every browser version,
enterprise profile, credential-store configuration, or encrypted cookie can be
read. Unsupported browser/platform combinations fail explicitly.

## Usage

Use a browser's default profile:

```sh
ytdlp-go --cookies-from-browser chrome URL
ytdlp-go --cookies-from-browser firefox URL
ytdlp-go --cookies-from-browser safari URL
```

Select one named Chromium or Firefox profile:

```sh
ytdlp-go --cookies-from-browser "chrome:Profile 1" URL
ytdlp-go --cookies-from-browser "firefox:work" URL
```

Firefox can additionally select a named container with `::CONTAINER`; the
special container name `none` selects cookies outside containers:

```sh
ytdlp-go --cookies-from-browser "firefox:work::Personal" URL
```

Safari accepts its default cookie store or an explicit absolute/`~/` database
path. Named profiles and Firefox containers reject path separators, empty
components, and traversal forms. Arbitrary Chromium/Firefox database paths are
available only to internal adapters and deterministic tests, not through the
public CLI.

## Security model

SQLite importers locate the selected profile's `Network/Cookies` or legacy
`Cookies` file, reject unsafe/non-regular or oversized inputs, and copy the
database plus an active WAL into a private snapshot before opening it
query-only. Firefox applies the same snapshot boundary and validates optional
container metadata. Safari validates and parses the bounded
`Cookies.binarycookies` file directly. Temporary snapshots are removed before
the operation continues.

Cookie names and values, profile paths, key-provider messages, protected bytes,
and raw database errors are excluded from public events and rendered errors.
Events expose bounded counts only. When an attributable subset cannot be
decrypted, successfully imported cookies are retained and the failure count is
reported; an unsafe source or complete import failure fails closed.

## Platform encryption boundaries

- macOS Chrome supports the browser's `v10` PBKDF2/AES-CBC format and obtains
  `Chrome Safe Storage` from Keychain through the absolute
  `/usr/bin/security` path. A normal Keychain prompt may appear.
- Linux Chromium-family import supports plaintext and legacy `v10` values.
  `v11` requires an embedding-supplied Safe Storage password provider; the CLI
  does not guess or invoke Secret Service/KWallet commands.
- Windows Chromium-family import supports native DPAPI legacy values, Local
  State keys, and AES-GCM `v10`/`v11`. App-bound `v20` values require an
  identity-appropriate embedding-supplied decryptor; ordinary CLI import keeps
  other successfully imported cookies and reports the unavailable subset.
- Firefox imports its SQLite cookie store without browser encryption and can
  filter by container identity.
- Safari import is macOS-only and reads the native bounded binary-cookie
  format.

Synthetic fixtures prove schema, crypto, snapshot, cancellation, limits,
partial-result, and secret-redaction behavior. Real-profile access remains an
explicit local action and is not inferred from CI. Never attach a browser
profile, cookie database, cookie value, or credential-store output to a public
issue.

Detailed Windows behavior and residual platform evidence are recorded in
[Windows Chromium cookie evidence](P3_WINDOWS_CHROMIUM_COOKIES_EVIDENCE.md)
and the [Phase 3 exit review](PHASE_3_EXIT_REVIEW.md).
