# EJS 0.8.0 Pilot Provenance

- yt-dlp reference: `aefce1eea4d0b6bab1ec2bd3beff09bff91a39c8`.
- Required component: `yt-dlp-ejs==0.8.0`, as pinned by that commit's
  `pyproject.toml`.
- Distribution: `yt_dlp_ejs-0.8.0-py3-none-any.whl`, published by the yt-dlp
  organization on PyPI on 2026-03-17.
- Distribution SHA-256:
  `79300e5fca7f937a1eeede11f0456862c1b41107ce1d726871e0207424f4bdb4`.
- Embedded core SHA3-512:
  `ee5b307d07f55e91e4723edf5ac205cc877a474187849d757dc1322e38427b157a9d706d510c1723d3670f98e5a3f8cbcde77874a80406bd7204bc9fea30f283`.
- Embedded library SHA3-512:
  `8420c259ad16e99ce004e4651ac1bcabb53b4457bf5668a97a9359be9a998a789fee8ab124ee17f91a2ea8fd84e0f2b2fc8eabcaf0b16a186ba734cf422ad053`.

The two embedded JavaScript files are the official prebuilt solver assets. The
hashes match yt-dlp's own 0.8.0 allowlist. They are loaded directly by Go and do
not include or invoke the wheel's Python modules.

The player fixture is repository-authored synthetic JavaScript shaped to
exercise EJS's real AST extraction and n/signature execution flow. It contains
no captured YouTube player source, media, identifier, personal data, or secret.
Expected values were first evaluated with the official 0.8.0 bundle and are
then checked through the Go helper in offline tests.

EJS is Unlicense; its embedded library contains Meriyah 6.1.4 under ISC and
Astring 1.9.0 under MIT, with the complete license banners retained in the
assets.
