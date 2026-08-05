# Public engine extraction contracts

Status: cycle-free public provider-contract and composition-support ownership
established; root orchestration and public provider composition remain staged.

The `github.com/tejasa97/youtube_dlp/engine/provider` package owns the nameable neutral
contracts used between orchestration and a composed provider:

- generic provider, search-prefix, retry, registry, selection, and metadata
  behavior;
- operation request, transport capabilities, credentials, extraction results,
  media metadata, URL results, playlists, entries, and lazy iteration;
- deterministic metadata/value objects under `engine/value`; and
- typed JavaScript-challenge and proof-of-origin token extension seams; and
- a generic `Bundle[C]` runtime with typed error classification, host/asset
  policy, status/network error, and attributable reload hooks.

Provider-specific packages remain free to define typed request structs that
contain a `provider.Request` and their own bounded options. Registry request
types are generic, so adding provider-specific state does not require an
option map, runtime profile, provider-name switch, or global registration.

Root `engine` and the former internal contract packages are compatibility
aliases and thin wrappers. Existing concrete providers, the mixed compatibility
package, and `pkg/ytdlp` therefore continue using the same implementations and
error identities. EJS, PO-token support, and the complete internal YouTube
family import the cycle-free owner directly.

## PR 2: orchestration ownership

The next PR moves provider-neutral client orchestration and its public request,
result, event, option, and error API into `engine`. It changes the operation
registry to consume these engine-owned contracts. `pkg/ytdlp` becomes the
broad compatibility facade: its existing exported surface aliases or wraps
`engine`, while `pkg/ytdlp.NewClient` explicitly supplies the unchanged full
provider catalog and plugin behavior.

The dependency proof for that stage must show that importing root `engine`
does not reach the mixed compatibility extractor package or broad catalog.
Its neutral same-package tests use public fake bundles; broad concrete-provider
coverage remains in `pkg/ytdlp` so tests cannot recreate the package cycle.
The module-internal `internal/enginetest` fixture package supplies provider
fakes without importing root `engine` or any concrete provider package; this
lets moved orchestration tests retain white-box coverage without exporting
operation internals.

## PR 3: complete YouTube composition

PR 3 adds `github.com/tejasa97/youtube_dlp/providers/youtube`. That public
package adapts the complete eight-provider internal YouTube family to the
engine contracts and exposes an explicit typed bundle in the preserved order.
It converts the challenge and PO-token seams through the public engine types;
no exported signature exposes a module-internal type.

Desktop then composes both analysis and per-download clients from `engine`
plus that bundle. Desktop's narrow request mapping and UI remain the workflow
boundary even though the bundle contains the complete YouTube family.
