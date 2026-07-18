# Phase 2 public Go API compatibility policy

## Contract

`pkg/ytdlp` is the supported embedding surface. Phase 2 begins at API version
`v1alpha1`; internal packages remain implementation details. The behavioral
fixture baseline is `yt-dlp/yt-dlp@aefce1eea4d0b6bab1ec2bd3beff09bff91a39c8`,
but the reference checkout is not a product or build dependency.

Within an alpha API version:

- existing exported functions, methods, interfaces, error categories and event
  meanings are not removed or silently reinterpreted;
- fields and options may be added, so callers should use keyed struct literals
  and ignore unknown JSON fields;
- a breaking Go signature, persisted representation, or lifecycle change
  requires an API-version change, migration note and conformance fixture;
- deprecated behavior remains for at least the next alpha API version unless a
  security defect requires earlier removal;
- compatibility claims apply only to the corpus named by the capability
  manifest, not to all yt-dlp behavior by implication.

`InfoJSON` is the extensible metadata envelope. Unknown ordered metadata is
preserved internally, while commonly used operation fields remain typed.
Errors support `errors.Is`/`errors.As`; callers should branch on
`ErrorCategory`, not diagnostic text. Event `kind` values declared by
`pkg/ytdlp` are stable within the API version. Diagnostics and events must not
contain credential values or unredacted signed input URLs.

All blocking operations accept or inherit a `context.Context`. Cancellation
must reach transport, playlist iteration, fragment transfer, helpers, plugins,
external tools, archives, caches and updater operations. A client is safe for
concurrent independent operations; mutable operation state and credentials are
not shared between calls.

The v1alpha1 trust surface is explicit:

- `InstallPluginPack`, `RollbackPluginPack`, and `RemovePluginPack` accept an
  exact Ed25519 trust map, verification time, and permission-review decision;
- an `InstalledPlugin` is opaque and can only be produced after the signed pack
  and ABI manifests agree on runtime, version, entrypoint, and permissions;
- `WithInstalledPlugins` never participates in automatic URL routing: a
  request must name `PluginID`, and an identity-bound permission approver is
  mandatory before execution;
- `PluginHost` exposes the ABI v1 extractor, postprocessor, and provider
  operations while retaining context cancellation and categorized failures;
- `OpenUpdater` snapshots threshold trust and exposes signed apply, snapshot,
  active-path, and verified rollback operations; and
- `ErrorSecurity` distinguishes signature, hash, unsafe-path, permission,
  revocation, freeze, and downgrade rejection from ordinary invalid input.

The `ytdlp-pack` and `ytdlp-update` commands are thin consumers of this same
API. They require explicit public keys and never select a publisher or
production signing ceremony.

## Compatibility change review

Every public change must identify:

1. the affected API version and capability-manifest entry;
2. deterministic success, failure, cancellation and serialization evidence;
3. persisted-data migration impact;
4. security and secret-redaction impact;
5. known deviations and removal/deprecation milestone.

The CLI builds on this API but may expose a wider compatibility surface. CLI
parsing, configuration syntax and terminal rendering do not leak into the
library contract.

## Zero-Python invariant

No supported API option may activate Python, a Python plugin, the reference
checkout, or a hidden Python fallback. Alternative native transports, helpers,
plugins or external media tools must be explicit capabilities with categorized
unavailability rather than silent semantic fallback.
