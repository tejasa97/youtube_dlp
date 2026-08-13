# Current project scope

This document describes the repository's current product and compatibility
boundary. It is not a roadmap.

## Interfaces

`ytdlp-go` has two first-party interfaces:

1. the `ytdlp-go` command-line application; and
2. public Go packages for broad or explicitly focused provider composition.

The root `engine` and provider packages expose provider-neutral orchestration
and explicit provider composition. `pkg/ytdlp` is the broad convenience
facade. Public API maturity and compatibility are described in
[Project status](PROJECT_STATUS.md) and the
[API compatibility policy](P2_API_COMPATIBILITY_POLICY.md).

VidStow is a separate focused desktop application. Its repository owns its UI,
supported workflows, packaging, and releases. Engine capability does not imply
VidStow support.

## Compatibility claims

Compatibility is bounded by deterministic evidence in
`conformance/parity_manifest.yaml`. A compatible entry identifies its fixture,
protocol, API, or failure-condition scope and links to automated evidence.
Compatibility does not mean complete or permanent yt-dlp parity.

Current URL-family support is documented in [Supported sites](SUPPORTED_SITES.md).
Current command behavior is documented in [CLI usage](CLI_USAGE.md). Known
limitations and maturity labels are documented in
[Project status](PROJECT_STATUS.md).

There is no Python runtime fallback. Unknown or unsupported behavior returns a
categorized result rather than silently invoking Python or a less constrained
execution path.

## Architecture

The engine keeps operation state, credentials, network policy, extraction,
selection, transfer, post-processing, and artifact publication inside one
bounded operation lifecycle. Focused provider composition is explicit; there
is no registration side effect that silently broadens an embedding product.

See [Architecture](ARCHITECTURE.md) and the
[architecture decisions](adr/README.md) for ownership and trust boundaries.

## Distribution status

No standalone CLI binary release or production updater channel is currently
endorsed. Source builds require the dependencies and verification steps in
[Installation](INSTALLATION.md). Artifact and signing claims apply only to an
exact published artifact and its recorded provenance.

## Current non-goals

- blanket site or option parity claims;
- DRM decryption or circumvention;
- silent Python or interpreter fallback;
- automatic trust of plugins or executable helpers discovered on `PATH`;
- treating synthetic fixtures as production deployment evidence;
- presenting experimental SABR/UMP behavior as general compatibility; and
- defining VidStow UI or packaging behavior in this repository.

## Sources of truth

- Product overview: [README](../README.md)
- Maturity and limitations: [Project status](PROJECT_STATUS.md)
- Capability claims: `conformance/parity_manifest.yaml`
- URL families: [Supported sites](SUPPORTED_SITES.md)
- Architecture: [Architecture](ARCHITECTURE.md)
- User-visible changes: [Changelog](../CHANGELOG.md)
- Active implementation changes: reviewed issues and pull requests
