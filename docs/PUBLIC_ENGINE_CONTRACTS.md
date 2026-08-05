# Public engine extraction contracts

Status: public provider-contract ownership established; orchestration and
public provider composition remain staged follow-up work.

The `github.com/tejasa97/youtube_dlp/engine` package owns the nameable neutral
contracts used between orchestration and a composed provider:

- generic provider, search-prefix, retry, registry, selection, and metadata
  behavior;
- operation request, transport capabilities, credentials, extraction results,
  media metadata, URL results, playlists, entries, and lazy iteration;
- deterministic metadata/value objects under `engine/value`; and
- typed JavaScript-challenge and proof-of-origin token extension seams.

Provider-specific packages remain free to define typed request structs that
contain an `engine.Request` and their own bounded options. Registry request
types are generic, so adding provider-specific state does not require an
option map, runtime profile, provider-name switch, or global registration.

The former internal contract and value packages are compatibility aliases and
thin wrappers. Existing concrete providers, the mixed compatibility package,
and `pkg/ytdlp` therefore continue using the same implementations and error
identities. The EJS challenge DTOs and PO-token request/response DTOs are also
aliases to engine-owned public types.

## PR 2: orchestration ownership

PR 2 moves provider-neutral client orchestration and its public request,
result, event, option, and error API into `engine`. It changes the operation
registry to consume these engine-owned contracts. `pkg/ytdlp` becomes the
broad compatibility facade: its existing exported surface aliases or wraps
`engine`, while `pkg/ytdlp.NewClient` explicitly supplies the unchanged full
provider catalog and plugin behavior.

The dependency proof for that stage must show that importing `engine` does not
reach the mixed compatibility extractor package or broad provider catalog.

## PR 3: complete YouTube composition

PR 3 adds `github.com/tejasa97/youtube_dlp/providers/youtube`. That public
package adapts the complete eight-provider internal YouTube family to the
engine contracts and exposes an explicit typed bundle in the preserved order.
It converts the challenge and PO-token seams through the public engine types;
no exported signature exposes a module-internal type.

Desktop then composes both analysis and per-download clients from `engine`
plus that bundle. Desktop's narrow request mapping and UI remain the workflow
boundary even though the bundle contains the complete YouTube family.
