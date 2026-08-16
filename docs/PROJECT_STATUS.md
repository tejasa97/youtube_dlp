# Project Status

`ytdlp-go` is pre-release software. The CLI is Alpha, root engine/provider
contracts are pre-1, and the broad compatibility facade is versioned
`v1alpha1`.

This document explains what those labels mean and where compatibility claims
come from. It is intentionally more precise than the public README.

## Current interfaces

| Interface | Maturity | Scope |
| --- | --- | --- |
| `ytdlp-go` CLI | Alpha | Evidence-backed extractor, media, playlist, compatibility, and post-processing behavior |
| root `engine` and provider packages | Pre-1 | Provider-neutral orchestration and explicit focused composition |
| `pkg/ytdlp` | `v1alpha1` | Context-aware native Go API with events, categorized errors, playlists, metadata, and artifacts |

There is no endorsed public binary release or production updater channel yet.

## What “compatible” means

A capability is marked compatible only when its entry in
`conformance/parity_manifest.yaml` links to passing deterministic evidence.
That evidence has a declared scope: a fixture corpus, protocol behavior,
compatibility-language subset, API contract, or bounded failure condition.

Compatible does **not** mean:

- every behavior in current upstream yt-dlp;
- every page or API response a supported service may return;
- every region, account, subscription, age gate, or live condition;
- permanent compatibility with a third-party site; or
- production deployment evidence that has not actually been collected.

Unknown behavior remains a documented deviation or a categorized unsupported
result. There is no Python fallback.

The manifest is the authoritative source for current capability counts. The
root README carries one concise count summary that repository tests keep in
sync; other user guides should link to the manifest instead of duplicating the
numbers. Run the repository parity checker to validate the manifest and linked
evidence:

```sh
go run ./cmd/paritycheck
```

## Evidence model

Compatibility evidence normally includes:

- attributable expectations derived from the pinned, read-only behavioral
  reference;
- publication-safe fixtures with adjacent provenance;
- success and failure tests;
- cancellation, resource-limit, and malformed-input tests where applicable;
- security review for credential, filesystem, helper, plugin, or updater
  boundaries; and
- an explicit statement of what remains unsupported.

Fixture provenance is stored beside each corpus under `conformance/`. The
[fixture policy](FIXTURE_POLICY.md) defines what may be checked in.

## Current capability areas

### Extractors

The native registry contains representative support across simple sites,
shared media backends, playlists, live and post-live behavior, authentication,
regional restrictions, manifests, browser impersonation, and JavaScript-heavy
services.

The exact public URL matrix and limitations are maintained in
[Supported sites](SUPPORTED_SITES.md). Extractor breadth against the pinned
reference is tracked in [the master checklist](EXTRACTOR_MASTER_CHECKLIST.md).

### Media

Current native paths include:

- direct HTTP(S) transfer;
- HLS VOD/live behavior;
- DASH addressing and bounded compatible multi-period behavior;
- ISM/Smooth Streaming;
- separate-track selection and merging; and
- typed FFmpeg/FFprobe post-processing operations.

DRM decryption is not implemented. Allowing DRM-marked formats to participate
in selection does not make them playable.

### Playlists and selection

The engine supports reusable lazy playlists, bounded continuation, item/range
selection, reverse selection, flat-playlist behavior, format-selector parsing,
fallbacks, merges, filtering, sorting, and multi-track output planning within
the documented contract.

### Compatibility languages

The project implements evidence-backed subsets of output/progress templates,
match filters, metadata transforms, configuration files, aliases, format
selection, cache behavior, and download archives. Resource limits and known
syntax deviations are part of those contracts.

### Extensions and updates

The repository contains deterministic evidence for native RPC and constrained
WASM plugin boundaries, signed packs, catalogs, updater transactions, rollback,
and health checks. It does not yet choose production signing identities,
publishing credentials, or an endorsed update channel.

### Downstream desktop application

[VidStow](https://github.com/vidstow/vidstow) is a separate project that
intentionally exposes a small, reviewable product surface over root `engine`
plus `providers/youtube`. Its repository is authoritative for desktop scope,
packaging, and releases. VidStow support must not be inferred from engine
support or vice versa.

Synthetic fixtures are not presented as production canary, account, region,
native Windows, signing, or updater-deployment evidence.

## Principal known limitations

- No complete yt-dlp site or option parity claim.
- No endorsed binary release channel.
- No production signing identities or updater trust root.
- Public Go contracts remain pre-1 and may require reviewed migrations.
- VidStow is independently versioned and currently exposes a narrower workflow
  than the engine.
- YouTube challenge behavior may drift or exceed helper execution limits.
- Authentication and browser-cookie behavior varies by platform and extractor.
- Third-party sites may change without notice.
- DRM decryption is outside the implementation.
- Experimental SABR/UMP work is not a general compatibility promise.

## How status changes

A user-visible capability change should update, in the same coherent change:

1. implementation and tests;
2. fixture provenance when new external expectations are introduced;
3. the capability manifest;
4. supported-site or user documentation;
5. known limitations; and
6. the changelog when the change affects a release-facing workflow.

See [Contributing](../CONTRIBUTING.md), [Current scope](CURRENT_SCOPE.md), and
the [documentation index](README.md) for the detailed review paths.
