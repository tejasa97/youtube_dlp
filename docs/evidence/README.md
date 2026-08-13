# Evidence index

`ytdlp-go` makes capability-scoped compatibility claims. Evidence documents
record the fixture corpus, attributable behavioral source, success and failure
coverage, resource boundaries, known deviations, and review status behind
those claims.

This index is for maintainers and evaluators. Most users should start with
[`PROJECT_STATUS.md`](../PROJECT_STATUS.md) and
[`SUPPORTED_SITES.md`](../SUPPORTED_SITES.md).

## Authoritative current sources

- Capability manifest: [`conformance/parity_manifest.yaml`](../../conformance/parity_manifest.yaml)
- Current limitations and evidence model: [Project status](../PROJECT_STATUS.md)
- Public URL-family claims: [Supported sites](../SUPPORTED_SITES.md)
- Fixture admission rules: [Fixture policy](../FIXTURE_POLICY.md)
- Extractor inventory: [Master checklist](../EXTRACTOR_MASTER_CHECKLIST.md)

Validate the manifest and linked records with:

```sh
go run ./cmd/paritycheck
```

## CLI and compatibility behavior

- [CLI flag inventory](../CLI_FLAG_INVENTORY.md)
- [CLI JSON-output evidence](../CLI_JSON_OUTPUT_EVIDENCE.md)
- [CLI print-output evidence](../CLI_PRINT_OUTPUT_EVIDENCE.md)
- [Format-selector parity](../FORMAT_SELECTOR_PARITY.md)
- [Format-selector pinned closure](../FORMAT_SELECTOR_PINNED_CLOSURE_EVIDENCE.md)
- [Current upstream format-selector delta](../FORMAT_SELECTOR_CURRENT_UPSTREAM_DELTA_EVIDENCE.md)
- [Match-filter parity](../MATCH_FILTER_PARITY_EVIDENCE_2026_07_29.md)
- [Metadata transformation parity](../METADATA_TRANSFORMATION_PARITY_EVIDENCE.md)
- [Output-template language evidence](../P2_COMPAT_LANGUAGES.md)

## Media and output behavior

- [Adaptive-streaming resilience](../ADAPTIVE_STREAMING_RESILIENCE_EVIDENCE.md)
- [HLS protocol closure](../HLS_PROTOCOL_CLOSURE_EVIDENCE.md)
- [HLS ad-fragment suppression](../HLS_AD_FRAGMENT_SUPPRESSION_EVIDENCE.md)
- [DASH dynamic SIDX](../DASH_DYNAMIC_SIDX_EVIDENCE.md)
- [DASH multi-period behavior](../DASH_MULTI_PERIOD_EVIDENCE.md)
- [Post-processing](../P2_POSTPROCESSING.md)
- [Multi-output lifecycle](../FORMAT_MULTIOUTPUT_LIFECYCLE_EVIDENCE.md)
- [Multi-output transaction](../FORMAT_MULTIOUTPUT_TRANSACTION_EVIDENCE.md)
- [Metadata sidecars](../METADATA_SIDECARS_EVIDENCE.md)
- [Thumbnail sidecars](../THUMBNAIL_SIDECARS_EVIDENCE.md)

## Extractors and providers

- [Extractor discovery](../EXTRACTOR_DISCOVERY_EVIDENCE.md)
- [Extractor routing controls](../EXTRACTOR_ROUTING_CONTROLS_EVIDENCE.md)
- [YouTube production-parity evidence](../YOUTUBE_PRODUCTION_PARITY_LEDGER.md)
- [YouTube player metadata](../YOUTUBE_PLAYER_METADATA_EVIDENCE.md)
- [YouTube format fidelity](../YOUTUBE_FORMAT_FIDELITY_EVIDENCE.md)
- [YouTube captions and subtitles](../YOUTUBE_CAPTIONS_EVIDENCE.md)
- [YouTube live audit](../YOUTUBE_LIVE_AUDIT_FIX_EVIDENCE.md)
- [YouTube PO-token integration](../YOUTUBE_POT_EVIDENCE.md)
- [SoundCloud search](../SOUNDCLOUD_SEARCH_EVIDENCE.md)
- [Vimeo authenticated video](../VIMEO_PRIVATE_VIDEO_EVIDENCE.md)

## Release, security, and operational evidence

- [Current project scope](../CURRENT_SCOPE.md)
- [Trust and security policy](../P2_TRUST_SECURITY_POLICY.md)
- [Plugin threat model](../P2_PLUGIN_THREAT_MODEL.md)
- [Signed packs](../P2_SIGNED_PACKS.md)
- [Updater and releases](../P2_UPDATER_RELEASES.md)
- [Privacy-safe telemetry](../P3_TELEMETRY.md)
- [Semantic review ledger](../P3_SEMANTIC_REVIEW_LEDGER.md)
- [Historical evidence](../history/README.md#retained-evidence)

## Fixture provenance

Fixture-specific provenance remains next to the data it governs under
`conformance/**/PROVENANCE.md`. Those records should not be centralized or
moved merely to shorten the documentation tree: collocation makes review,
licensing, sanitization, and subsequent fixture changes auditable.

Some evidence documents retain historical `P1_*`, `P2_*`, or `P3_*` filenames
so existing capability-manifest and documentation links remain stable.
