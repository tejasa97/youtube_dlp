# Signed-pack fixture provenance

These files are synthetic and contain no captured service response, user data,
credential, production signing key, or upstream executable code.

- Fixture authored: 2026-07-18.
- Behavioral reference inspected read-only: `yt_dlp/plugins.py` and the plugin
  installation section of `README.md` in pinned yt-dlp commit
  `aefce1eea4d0b6bab1ec2bd3beff09bff91a39c8`.
- Upstream behavior represented: plugin packages may contain nested files and
  have an identified entry module. No Python archive layout or code is copied.
- Go-specific security behavior represented: a canonical manifest, an RPC
  entrypoint, explicit permissions, per-file modes/digests/sizes, and a
  deterministic Ed25519 test signature.
- The private seed is defined only in `internal/pack/archive_test.go`; it is
  deterministic test material and MUST NOT be used as a production trust root.
- `expected.json` records the canonical archive digest, byte length, and the
  public-key-derived key ID. Tests rebuild the archive entirely in Go and
  compare all three values.

The pack format is intentionally new. It does not claim binary compatibility
with Python `.zip`, `.egg`, or `.whl` plugin packages, which execute unchecked
Python code in the reference project.
