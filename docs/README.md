# Documentation

Start with the task you are trying to complete. Current user and developer
guidance is separated from compatibility evidence and architectural records.

## Users

- [Project overview and quick start](../README.md)
- [Installation](INSTALLATION.md)
- [CLI usage](CLI_USAGE.md)
- [Troubleshooting](TROUBLESHOOTING.md)
- [Current project status](PROJECT_STATUS.md)
- [Supported extractors](SUPPORTED_SITES.md)
- [Configuration](CONFIGURATION.md)
- [Browser cookie import](CHROMIUM_COOKIE_IMPORT.md)
- [Playlist behavior](PLAYLIST_MODEL.md)
- [VidStow desktop application](https://github.com/vidstow/vidstow)
- [Support and issue reporting](../SUPPORT.md)
- [Security reporting](../SECURITY.md)

The executable help is the authoritative CLI option list:

```sh
ytdlp-go --help
```

## Go embedding and extensions

- [Embedding ytdlp-go](EMBEDDING.md)
- [Public engine extraction contracts](PUBLIC_ENGINE_CONTRACTS.md)
- [YouTube provider request seam](YOUTUBE_PROVIDER_REQUEST_SEAM.md)
- [API compatibility policy](P2_API_COMPATIBILITY_POLICY.md)
- [JavaScript helper protocol](JAVASCRIPT_HELPER_PROTOCOL.md)
- [Plugin ABI v1](P2_PLUGIN_ABI_V1.md)
- [Plugin SDK guide](P3_PLUGIN_SDK_GUIDE.md)
- [Plugin threat model](P2_PLUGIN_THREAT_MODEL.md)
- [Signed plugin packs](P2_SIGNED_PACKS.md)

## Architecture and reference

- [Architecture overview](ARCHITECTURE.md)
- [Architecture decisions](adr/README.md)
- [Fixture and test-data policy](FIXTURE_POLICY.md)
- [Format-selector behavior](FORMAT_SELECTOR_PARITY.md)
- [Downloader protocols](P2_DOWNLOADER_PROTOCOLS.md)
- [Post-processing](P2_POSTPROCESSING.md)
- [Trust and security policy](P2_TRUST_SECURITY_POLICY.md)
- [Python-free runtime image](PYTHON_FREE_RUNTIME_IMAGE.md)

## Project and releases

- [Current scope](CURRENT_SCOPE.md)
- [Changelog](../CHANGELOG.md)
- [Updater and release foundations](P2_UPDATER_RELEASES.md)
- [Third-party notices](../THIRD_PARTY_NOTICES.md)
- [Code of Conduct](../CODE_OF_CONDUCT.md)
- [Contributing](../CONTRIBUTING.md)

## Evidence and audits

- [Engine E5 hardening evidence](ENGINE_E5_HARDENING.md)
- [Evidence index](evidence/README.md) — current capability claims, protocol
  records, and fixture provenance
- [Historical engineering records](history/README.md) — retained factual
  evidence at named baselines

The capability manifest at `conformance/parity_manifest.yaml` is authoritative
for compatibility counts. Provenance for an individual fixture remains beside
that fixture as `conformance/**/PROVENANCE.md`.

## Documentation conventions

- Put stable user workflows in the root README or a focused task guide.
- Put precise API, schema, protocol, and configuration facts in reference
  documents.
- Put architectural rationale in explanation documents or ADRs.
- Link compatibility claims to automated evidence and fixture provenance.
- State known deviations beside the feature they constrain.
- Do not copy upstream prose merely to imply parity; describe only behavior
  this implementation proves.
