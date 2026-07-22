# Documentation

This index separates public guidance from implementation plans and historical
evidence. Start with the user or embedding documents; consult phase evidence
when evaluating a compatibility claim.

## Users

- [Project overview and quick start](../README.md)
- [Supported extractors](SUPPORTED_SITES.md)
- [Configuration](CONFIGURATION.md)
- [Chromium cookie import](CHROMIUM_COOKIE_IMPORT.md)
- [Native netrc evidence](P3_NETRC_EVIDENCE.md)
- [Impersonation profiles](P3_IMPERSONATION_PROFILES.md)
- [Playlist model](PLAYLIST_MODEL.md)
- [Support and issue reporting](../SUPPORT.md)
- [Security reporting](../SECURITY.md)

The executable help is the authoritative CLI option list:

    ytdlp-go --help

## Go embedding and extensions

- [Embedding ytdlp-go](EMBEDDING.md)
- [API compatibility policy](P2_API_COMPATIBILITY_POLICY.md)
- [Plugin ABI v1](P2_PLUGIN_ABI_V1.md)
- [Plugin threat model](P2_PLUGIN_THREAT_MODEL.md)
- [Signed plugin packs](P2_SIGNED_PACKS.md)
- [JavaScript helper protocol](JAVASCRIPT_HELPER_PROTOCOL.md)
- [Trust and security policy](P2_TRUST_SECURITY_POLICY.md)
- [Go plugin SDK guide](P3_PLUGIN_SDK_GUIDE.md)

## Media and compatibility behavior

- [Compatibility languages](P2_COMPAT_LANGUAGES.md)
- [Downloader and protocols](P2_DOWNLOADER_PROTOCOLS.md)
- [Post-processing](P2_POSTPROCESSING.md)
- [Fallback inventory](P2_FALLBACK_INVENTORY.md)
- [YouTube protected-playback continuation](YOUTUBE_PROTECTED_PLAYBACK_PLAN.md)
- [Native YouTube PO-token evidence](YOUTUBE_POT_EVIDENCE.md)
- [YouTube caption extraction evidence](YOUTUBE_CAPTIONS_EVIDENCE.md)
- [YouTube subtitle CLI and sidecar evidence](YOUTUBE_SUBTITLE_CLI_EVIDENCE.md)
- [Privacy-safe telemetry](P3_TELEMETRY.md)
- [Semantic review ledger](P3_SEMANTIC_REVIEW_LEDGER.md)

## Releases and security

- [Alpha release procedure](P2_ALPHA_RELEASE.md)
- [Updater and releases](P2_UPDATER_RELEASES.md)
- [Phase 2 security review](P2_SECURITY_REVIEW.md)
- [Publication-readiness review](PUBLICATION_READINESS.md)
- [Third-party notices](../THIRD_PARTY_NOTICES.md)

## Plans and exit reviews

| Phase | Plan | Exit or current evidence |
| --- | --- | --- |
| Zero-Python program | [Program plan](../ZERO_PYTHON_GO_PORT_PLAN.md) | [Port evaluation](../GO_PORT_EVALUATION.md) |
| Phase 0 | [Implementation plan](../PHASE_0_IMPLEMENTATION_PLAN.md) | [Exit review](PHASE_0_EXIT_REVIEW.md) |
| Phase 1 | [Implementation plan](../PHASE_1_IMPLEMENTATION_PLAN.md) | [Exit review](PHASE_1_EXIT_REVIEW.md) and [differential review](P1_DIFFERENTIAL_REVIEW.md) |
| Phase 2 | [Implementation plan](../PHASE_2_IMPLEMENTATION_PLAN.md) | [Exit review](PHASE_2_EXIT_REVIEW.md) |
| Phase 3 | [Implementation plan](../PHASE_3_IMPLEMENTATION_PLAN.md) | [Exit review](PHASE_3_EXIT_REVIEW.md) and [wave ledger](P3_WAVE_LEDGER.md) |

## Architecture and provenance

- [Architecture decisions](adr/README.md)
- [Fixture and test-data policy](FIXTURE_POLICY.md)
- Conformance fixture provenance is stored beside each corpus as
  conformance/**/PROVENANCE.md.

## Independent audits

- [Phase 3 coverage and parity audit](audits/P3_COVERAGE_AND_PARITY_AUDIT.md)
- [Phase 3 security, privacy, and isolation audit](audits/P3_SECURITY_PRIVACY_ISOLATION_AUDIT.md)
- [Phase 3 release, operations, and ABI audit](audits/P3_RELEASE_OPERATIONS_ABI_AUDIT.md)

Audit findings describe their exact historical baseline. Later remediation is
recorded in the Phase 3 exit review rather than rewriting an independent audit.

Historical lane reports under docs/P1_*.md and docs/P2_*.md explain why a
capability was claimed at a particular gate. They are evidence records, not a
substitute for current user documentation or the capability manifest.

## Documentation conventions

- Put stable user workflows in README.md or a focused user document.
- Put public Go contracts and security boundaries in focused docs linked from
  this index.
- Keep time-bound implementation plans and exit reviews clearly dated and
  labeled with status.
- Link every compatibility claim to automated evidence and fixture provenance.
- State known deviations next to the feature they constrain.
- Never copy upstream prose merely to imply parity; describe only behavior this
  implementation proves.
