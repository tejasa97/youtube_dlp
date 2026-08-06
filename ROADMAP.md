# Roadmap

This roadmap describes current engine priorities, not release-date
commitments. Historical phase plans and exit reviews remain available through
the [documentation index](docs/README.md), but they are not the active roadmap.

## Current direction

`ytdlp-go` is a native media engine with two first-party interfaces:

1. the `ytdlp-go` CLI for terminal and automation workflows; and
2. public Go packages for broad or explicitly focused provider composition.

The separate [VidStow](https://github.com/tejasa97/vidstow) repository owns the
focused desktop product, UI workflows, packaging, and release roadmap. VidStow
uses root `engine` plus `providers/youtube`; its feature commitments are not
inferred from the broader engine catalog.

Work in this repository is prioritized by practical user value, attributable
compatibility gaps, release safety, and maintainability rather than blanket
yt-dlp parity claims.

## Near term

### Public engine and provider contracts

- Stabilize the pre-1 root `engine`, `engine/provider`, and `engine/value`
  surfaces through documented compatibility policy and external-package tests.
- Keep `pkg/ytdlp` source-compatible as the broad convenience facade.
- Keep focused provider packages explicit, typed, cycle-free, and free of
  registration globals or hidden fallback catalogs.
- Publish tagged module releases with clear migration notes when public
  contracts change.

### CLI and release engineering

- Define supported operating systems and architectures for CLI binaries.
- Package `ytdlp-js-helper` beside every released CLI artifact.
- Decide and document FFmpeg/FFprobe redistribution boundaries.
- Add clean-machine installation and download smoke tests.
- Produce checksums, provenance, third-party notices, and release notes.
- Establish signing identities and a documented security-fix release process.

### High-value compatibility

- Deepen frequently used extractors and reusable shared backends.
- Close practical format-selection, match-filter, template, metadata, playlist,
  and post-processing gaps.
- Continue attributable HLS, DASH, downloader, and media-processing work.
- Track upstream changes that affect supported behavior.
- Keep known deviations explicit and fixture provenance reviewable.

### Documentation and maintenance

- Keep installation, CLI, embedding, troubleshooting, support, security, and
  supported-site guidance synchronized with observable behavior.
- Separate current user documentation from historical plans and evidence.
- Keep conformance provenance beside the fixture corpus it explains.
- Treat issues and reviewed pull requests as the source of truth for active
  implementation work.

## Before a stable release

- Define versioning and compatibility guarantees for the CLI and public Go API.
- Publish supported-platform and external-dependency guarantees.
- Complete third-party binary redistribution review.
- Verify install, upgrade, rollback, and uninstall paths where promised.
- Maintain a curated changelog and security-fix process.
- Demonstrate advertised CLI and embedding workflows on clean environments.
- Reconcile every release claim with `conformance/parity_manifest.yaml` and its
  linked evidence.

## Later opportunities

- additional first-party focused provider packages;
- richer public composition helpers without runtime feature gates;
- package-manager distribution for the CLI;
- additional platform and architecture artifacts when test capacity permits;
- signed update notifications; and
- broader plugin SDK and catalog adoption after the trust model is operational.

## Explicitly deferred or out of scope

- Blanket site or option parity claims.
- DRM decryption or DRM circumvention.
- Silent Python or interpreter fallback.
- Automatic trust of plugins or executable helpers from `PATH`.
- Treating synthetic fixtures as production deployment evidence.
- Expanding experimental SABR/UMP work into an unbounded compatibility goal.
- Owning VidStow UI features or release packaging in this repository.

## Sources of truth

- Current project scope: [README](README.md).
- Supported URL families: [Supported sites](docs/SUPPORTED_SITES.md).
- Capability claims: `conformance/parity_manifest.yaml`.
- Known compatibility boundaries: [Project status](docs/PROJECT_STATUS.md).
- Active implementation changes: repository issues and reviewed pull requests.
- Desktop product scope: [VidStow](https://github.com/tejasa97/vidstow).
- Historical rationale: phase plans, exit reviews, audits, ADRs, and evidence
  linked from [Documentation](docs/README.md).
