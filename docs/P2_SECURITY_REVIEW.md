# Phase 2 Gate G2 security review

Review date: 2026-07-18

Scope: credentials, archives, caches, filesystem paths, external commands,
plugins, signed packs, signatures and keys, updater/rollback, release artifacts,
and native extractor inputs.

## Decision

No open critical security finding remains in the Phase 2 claimed surface.
Capabilities that cannot establish their required platform guarantee fail
closed and remain explicit deviations; they do not silently select a weaker
implementation. The deterministic signing keys in tests and fixtures are not
production authority.

Severity here reflects exploit impact within the declared alpha boundary:
critical means unauthenticated code execution, credential disclosure, signature
bypass, or uncontrolled overwrite on a supported default path. A documented
platform limitation with a fail-closed boundary is not classified as an open
vulnerability.

## Reviewed surfaces

| Surface | Controls and automated evidence | Result |
| --- | --- | --- |
| Credentials and cookies | Synthetic profiles only; locked SQLite/WAL copies; domain-bound Chromium values; partial-decrypt accounting; unsafe-profile rejection; cancellation; provider-password zeroing; redacted events/errors. Evidence includes `TestImportCopiesLiveWALAndReturnsPartialResults`, `TestMacDecryptorRejectsWrongHostPaddingAndKey`, `TestImportZeroesProviderPassword`, `TestMeta24HostBindingRejectsWrongDomain`, `TestImportCancellationAndUnsafePath`, and `TestErrorsRedactCookieSecrets`. | Pass; unsupported platform stores are categorized, never guessed. |
| Archives and caches | Private roots, safe component encoding, cross-process locks, bounded records, digest/versioned cache records, atomic replacement, cancellation, corruption detection, and symlink/hardlink protections. Evidence includes `TestArchiveCrossProcessLocking`, `TestArchiveRejectsSymlinkCorruptionAndOversize`, `TestArchiveAcceptsTruncatedFinalRecordAndDoesNotMutateHardlink`, `TestCacheRejectsTraversalAndSymlinks`, `TestCacheAtomicReplacementDoesNotMutateHardlink`, and `TestCacheMidWriteCancellationLeavesPriorValue`. | Pass. |
| Download and output paths | Confined destinations, exclusive creation, safe partial-state names, symlink rejection, immutable finalization, bounded retry state, and signed-URL redaction. Evidence includes `TestDestinationRejectsExistingSymlink`, `TestPartialStateIgnoresPredictableTempAndRejectsUnsafeFinal`, `TestDownloadRejectsSymlinkEscape`, and `TestDownloadEventsRedactSignedURL`. | Pass. |
| External downloaders and media tools | Typed executable boundary, no shell, interpreter/trampoline rejection for external downloaders, separated argv, bounded/redacted diagnostics, process-tree cancellation, atomic outputs, and cleanup. Evidence includes `TestExternalAdapterRejectsUnsafeInputs`, `TestExternalAdapterRejectsInterpreters`, `TestCommandCancellation`, `TestAtomicPostprocessCancellationRemovesTemporaryOutput`, `TestOperationValidationAndAtomicFailure`, and `TestDiagnosticRedaction`. | Pass. ffmpeg/ffprobe remain explicit native tools, not hidden fallbacks. |
| Plugin ABI and discovery | Canonical trusted roots, owner/permission checks, symlink rejection, package revalidation, strict bounded frames, version negotiation, identity-bound permission approval, secret-field rejection, Python-runtime rejection, crash isolation, and process-tree cancellation. Evidence includes `TestDiscoverRejectsWritableAndSymlinkPaths`, `TestLoadPackageRejectsInterpreterTrampolineAndOversize`, `TestRPCRejectsSecretArgumentsEnvironmentAndPython`, `TestRPCPermissionChangeApprovalIsIdentityBound`, `TestRPCProcessTreeCancellation`, and `TestWASMRequiresTrustedPackageOrExplicitTestModeAndVerifiesDigest`. | Pass for declared ABI v1 and constrained WASM boundary. |
| Signed packs and sandboxing | Canonical deterministic archive, Ed25519 verification before mutation, digest/path binding, revocation/expiry/downgrade enforcement, permission-delta approval, journal recovery, hostile link/path rejection, and platform adapter fail-closed behavior. Evidence includes `TestVerifyRejectsAmbiguousOrHostileArchives`, `TestInstallPermissionIncreaseRequiresExplicitApproval`, `TestRollbackRejectsRevokedExpiredAndTamperedTargets`, `TestRemoveDoesNotFollowHostilePayloadLinks`, and sandbox plan/limit tests. | Pass for Linux/macOS declared guarantees; secure lifecycle is explicitly unavailable on Windows. |
| Signatures and trust keys | Threshold roots are caller-supplied and cloned; key IDs derive from public keys; canonical signed bodies reject duplicates/tampering; product, role, channel, platform, generation, expiry, and version are bound. No command creates or chooses a production key. Evidence includes `TestThresholdAndScope`, `TestVerifyRejectsTamperDuplicateAndNonCanonical`, `TestSelectProtections`, `TestManagerClonesTrustConfiguration`, and pack trust/revocation tests. | Pass. Test keys are visibly deterministic and non-production. |
| Updater and rollback | Private root validation, serialized writers, immutable version trees, bounded staging, hash/size verification, same-filesystem publication, signed monotonic metadata, health check before commitment, recovery journal, verified rollback, and direct process execution. Evidence includes `TestApplyUpdateRollbackAndHealthFailure`, `TestRecoveryRestoresLastVerifiedState`, `TestHostilePathsLocksAndJournal`, `TestCommandHealthCheckerBoundsFailedIsolation`, and `TestNativeArtifactInstallUpdateRollbackAndRun`. | Pass with residual platform limitations below. |
| Release artifacts | Explicit no-cgo/trim-path build metadata, matching linked dependency identities, replacement rejection, deterministic archives/checksums/manifest/SBOM, exact component/license coverage, symlinked license rejection, and exclusive non-replacing publication. Evidence includes `TestArchivesAreReproducibleAndNormalized`, `TestChecksumsLicensesSBOMAndManifest`, `TestReadLicenseEntriesRejectsSymlink`, `TestAtomicWriteDoesNotReplaceExistingFile`, and the manual alpha workflow's double-build/native-run/checksum matrix. | Pass as internal engineering evidence; public distribution is legally blocked, not a security fallback. |
| Extractor/network inputs | Host/scheme-specific routing precedes generic HTTP(S), opaque schemes require a matching native extractor, response bodies and playlists are bounded, continuations are validated, cancellation reaches transport, browser impersonation is explicit, and response secrets are not rendered. Extractor fixture suites cover malformed, authentication, geo, unavailable, cancellation, playlist, and parser-fuzz paths. | Pass for registered representative corpus; live canaries are not compatibility authority. |
| Python-free invariant and fallback inventory | Production-source tripwire, reference-path tripwire, scratch runtime container, no-cgo builds, interpreter rejection in downloader/plugins, and a machine-readable inventory requiring `python-backed` and `silent` to remain prohibited. | Pass. |
| Dependency vulnerabilities | Go's symbol-aware vulnerability scanner is pinned in CI. The G2 audit initially found reachable advisories in `golang.org/x/net v0.42.0` and `cloudflare/circl v1.5.0`; the Go floor and transitive pins were raised to fixed `v0.55.0` and `v1.6.3`. `govulncheck v1.6.0 ./...` then reported zero reachable vulnerabilities. | Pass. |

## Residual finding register

| ID | Severity | Residual risk and containment | Owner / milestone |
| --- | --- | --- | --- |
| G2-S01 | Medium | Windows health checking starts a process before assigning it to a Job Object. A hostile signed artifact could try to spawn and detach a child in that narrow interval. Artifact threshold trust and health rollback limit reach; this is not treated as complete hostile-code sandboxing. | release / Phase 3 suspended-process launcher evaluation |
| G2-S02 | Medium | Portable Go APIs cannot prove Windows owner/DACL policy for updater roots or provide POSIX directory-fsync semantics. Reparse-point roots are rejected and file replacement is write-through, but deployment must provision a private per-user root. | release / Phase 3 Windows installer policy |
| G2-S03 | Medium | macOS `sandbox-exec` is deprecated and does not enforce the requested CPU/memory quotas. The adapter declares this gap; callers requiring those guarantees receive unsupported rather than an unannounced weaker plan. | runtime / Phase 3 maintained sandbox adapter decision |
| G2-S04 | Low | `cmd/ytdlp-release` assumes its existing output directory and parent are controlled by the release job. It rejects a symlink at validation and publishes each file exclusively, but does not claim protection from an actor able to replace the directory concurrently. Artifacts contain no secrets and the CI workspace is single-owner. | release / before multi-tenant release workers |
| G2-S05 | Informational | Signed-pack install/rollback/remove is unavailable on Windows because equivalent owner/ACL, lock, and atomic-publication evidence is absent. Verification remains portable and lifecycle operations fail closed. | runtime / Phase 3 Windows pack installer |

## Operational and legal blockers

Production root custody, signer identities, quorum, rotation, transparency,
artifact transport, and publishing credentials are intentionally external
inputs. The repository neither generates nor selects them. Public binary
distribution is also blocked because the repository has no project-wide
distribution license and the pinned fhttp dependency has no repository-level
license conclusion. These constraints do not weaken the deterministic internal
alpha evidence and must be resolved before any public release.
