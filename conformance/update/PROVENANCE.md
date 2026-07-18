# Signed updater fixture provenance

- Fixture set: `valid-envelope.json`, `hostile-duplicate-envelope.json`, and
  `platform-expectations.json`.
- Created: 2026-07-18.
- License: project-original synthetic test data.
- Signing authority: the valid envelope uses the deterministic Ed25519 seed
  `SHA-256("ytdlp-go deterministic NON-PRODUCTION test key: release-1")`.
  This key is test-only and is not a production publisher or trust root.
- Artifact: the ASCII bytes `portable-artifact` (without a newline), represented
  as base64 in the expectation file. Its SHA-256 is
  `2042f5c3b5166c9f5cca6eb5c16a9d84c0df1dc673088ebe971e5f20e0e326a6`.
- Coverage: canonical encoding/signature verification, public-key-derived key
  IDs, role/product/channel/platform scopes, target naming, and duplicate-key
  rejection.
- Upstream reference: no yt-dlp source, response, key, signature, release
  artifact, or metadata was copied. The pinned behavioral reference remains
  `yt-dlp/yt-dlp@aefce1eea4d0b6bab1ec2bd3beff09bff91a39c8` only for project-wide
  context; these security fixtures are an independent Go-port design.
