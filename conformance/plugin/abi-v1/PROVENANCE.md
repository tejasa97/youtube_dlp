# Plugin ABI v1 fixture provenance

The manifests in this directory were authored for the Go port's Phase 2
Plugin ABI v1. They contain no captured response, executable, credential,
copyrighted media, or Python-derived byte sequence.

- `manifest-v1.0.json` declares the baseline 1.0 extractor ABI.
- `manifest-v1.1.json` declares the backwards-compatible inclusive 1.0–1.1
  range and all three v1 capability classes.
- Numeric version `1` preserves the Phase 1 wire value for v1.0. Numeric
  version `65537` encodes major 1, minor 1.

The pinned yt-dlp checkout at
`/Users/tejas/projects/yt-dlp-reference` commit
`aefce1eea4d0b6bab1ec2bd3beff09bff91a39c8` was used only as behavioral
context for extractor result concepts. No upstream plugin code or fixtures were
copied. The ABI, security policy, and fixtures are original Go-port work.
