# Linux Chromium cookie import provenance

The v10 and v11 public ciphertext expectations come from
`test/test_cookies.py` (`TestCookies.test_chrome_cookie_decryptor_linux_*`) and
the format/schema behavior comes from `yt_dlp/cookies.py` at pinned read-only
commit `aefce1eea4d0b6bab1ec2bd3beff09bff91a39c8`.

All database rows beyond those public ciphertext vectors are generated with
synthetic hosts, passwords, and values. Tests never inspect a real profile or
call a desktop credential store. The provider abstraction makes credential
access explicit, cancellable, and replaceable by product integration.

Default profile discovery is intentionally limited to Chrome, Chromium, and
Brave's standard Linux XDG locations plus a named profile. It does not parse
browser-specific profile registries. v11 support requires product integration
to supply a Secret Service/KWallet provider; provider failures are categorized
without including credential-store error text. Explicit database/profile paths
remain portable for deterministic tests and migration tooling on every OS.
