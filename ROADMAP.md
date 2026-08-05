# Roadmap

This roadmap describes current priorities, not release-date commitments.
Historical phase plans and exit reviews remain available through the
[documentation index](docs/README.md), but they are not the active roadmap.

## Current direction

`ytdlp-go` is organized as one native engine with three interfaces:

1. a focused Desktop application for non-technical users;
2. a capable CLI for terminal and automation workflows; and
3. a versioned Go API for embedding.

Work is prioritized by practical user value, attributable compatibility gaps,
release safety, and maintainability rather than by claiming blanket yt-dlp
parity.

## Near term

### Desktop V0 stabilization

- Keep the interface aligned with the approved design and copy.
- Verify every view and state on candidate host platforms.
- Preserve single-video YouTube scope until the V0 workflow is reliable.
- Improve actionable diagnostics for helper, FFmpeg, network, authentication,
  and unsupported-input failures.
- Maintain queue, cancellation, retry, history, settings, and accessibility
  behavior with automated tests.

### Release engineering

- Decide canonical project and package identity.
- Define supported operating systems and architectures.
- Build native Desktop artifacts on macOS, Windows, and Linux.
- Package `ytdlp-js-helper` correctly on every platform.
- Decide and document FFmpeg/FFprobe redistribution.
- Add clean-machine installation and download smoke tests.
- Establish macOS signing/notarization and Windows signing.
- Produce checksums, provenance, third-party notices, and release notes.
- Publish a beta only after the artifacts are reproducible and verifiable.

### Public documentation

- Keep README, installation, Desktop, CLI, troubleshooting, support, and
  security guidance synchronized with observable behavior.
- Add sanitized real screenshots before the first public Desktop release.
- Replace “coming soon” release text only when real packages exist.

### High-value compatibility

- Deepen frequently used extractors and reusable backend behavior.
- Close practical format-selection, match-filter, template, and metadata gaps.
- Continue attributable HLS, DASH, downloader, and post-processing work.
- Track upstream changes that affect supported behavior.
- Keep known deviations explicit and fixture provenance reviewable.

## Before a stable release

- Validate tagged module installation from the canonical repository path.
- Define versioning and support policy.
- Establish an operational release workflow and signing identities.
- Publish supported-platform and dependency guarantees.
- Complete third-party binary redistribution review.
- Define update notification or update delivery behavior.
- Verify install, upgrade, rollback, and uninstall paths.
- Maintain a release changelog and security-fix process.
- Demonstrate the advertised Desktop workflows on clean machines.

## Later opportunities

These are candidates after the initial Desktop release is dependable:

- additional Desktop URL families;
- playlist-aware Desktop workflows;
- richer format and subtitle controls;
- optional advanced settings without exposing raw CLI complexity;
- additional Linux package formats;
- Windows and Linux ARM64 artifacts when demand and test capacity justify them;
- package-manager and store distribution; and
- signed update notifications or automatic updates.

Each addition should preserve a simple default path for non-technical users.

## Explicitly deferred or out of scope

- Blanket site or option parity claims.
- DRM decryption or DRM circumvention.
- Silent Python or interpreter fallback.
- Automatic trust of plugins or executable helpers from `PATH`.
- Treating synthetic fixtures as production deployment evidence.
- Expanding experimental SABR/UMP work into an unbounded compatibility goal.

## Sources of truth

- Current user-facing scope: [README](README.md) and focused user guides.
- Supported URL families: [Supported sites](docs/SUPPORTED_SITES.md).
- Capability claims: `conformance/parity_manifest.yaml`.
- Known compatibility boundaries: [Project status](docs/PROJECT_STATUS.md).
- Active implementation changes: repository issues and reviewed pull requests.
- Historical rationale: phase plans, exit reviews, audits, ADRs, and evidence
  linked from [Documentation](docs/README.md).
