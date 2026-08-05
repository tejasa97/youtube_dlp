# Public engine extraction contracts

Status: cycle-free public contracts and root provider-neutral orchestration
established; the public complete-YouTube bundle and Desktop switch remain staged.

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

Root `engine` owns the operational `Client`, run `Request`/`Result`, options,
events, errors, and extraction/download/media orchestration. It exposes
`NewComposition`, whose typed catalog and request adapter create a fresh
provider runtime for every run. A zero composition fails closed; engine never
discovers or falls back to the broad catalog.

## Completed orchestration ownership

`pkg/ytdlp` is now the broad compatibility facade. Its existing exported
surface aliases or wraps `engine`, while `pkg/ytdlp.NewClient` explicitly
supplies the unchanged full provider catalog and installed-plugin behavior.
Provider-specific error classification, host/asset/media policies, external
service identity, challenge construction, PO-token adaptation, and live/SABR
reload enter through typed composition hooks rather than engine switches.

The dependency proof shows that importing root `engine` does not reach the
mixed compatibility extractor package, concrete provider packages, EJS, the
PO-token director, or the broad catalog. Neutral tests use public fake bundles;
the moved orchestration suite uses test-only broad adapters, while production
broad catalog and compatibility ownership remains in `pkg/ytdlp`.
The module-internal `internal/enginetest` fixture package supplies provider
fakes without importing root `engine` or any concrete provider package; this
lets moved orchestration tests retain white-box coverage without exporting
operation internals.

## Next: complete YouTube composition

PR 3 adds `github.com/tejasa97/youtube_dlp/providers/youtube`. That public
package adapts the complete eight-provider internal YouTube family to the
engine contracts and exposes an explicit typed bundle in the preserved order.
It converts the challenge and PO-token seams through the public engine types;
no exported signature exposes a module-internal type.

Desktop then composes both analysis and per-download clients from `engine`
plus that bundle. Desktop's narrow request mapping and UI remain the workflow
boundary even though the bundle contains the complete YouTube family.
