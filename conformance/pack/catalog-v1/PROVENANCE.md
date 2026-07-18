# Offline pack catalog v1 provenance

The catalog corpus is synthetic and was authored for the Go port on
2026-07-19. Its package names, versions, hashes, paths, timestamps, revocations,
and deterministic Ed25519 test key do not originate from a production catalog
or signing system. The format is domain-separated from pack and release
signatures and has no Python dependency.

Behavioral expectations were compared with the pinned project's general plugin
compatibility and update safety requirements at
`yt-dlp/yt-dlp@aefce1eea4d0b6bab1ec2bd3beff09bff91a39c8`; no upstream source or
credential is copied into the fixture.

