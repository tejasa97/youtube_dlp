# Release fixture provenance

- Fixture set: `checksums.expected.txt` and `build-plan.expected.json`.
- Created: 2026-07-18.
- License: project-original synthetic test data.
- Inputs: the checksum file covers the one-byte ASCII payloads `a` and `b`;
  the build plan uses a fixed UTC source epoch and synthetic Linux target.
- Coverage: stable ordering, standard lowercase SHA-256 output, no-cgo build
  configuration, trim-path/build-VCS/build-ID controls, explicit platform, and
  deterministic build tags.
- Upstream reference: no upstream code, artifact, notice, or build metadata was
  copied. The pinned reference commit is not read by release code or tests.
