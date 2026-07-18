# Phase 3 security, privacy, and isolation audit

Audit date: 2026-07-19

Audited commit: `c40182f6f36f00c2f686c6d063e079d9a2fcfb44`

Audit branch: `codex/p3-audit-security`

## Decision

No critical unreviewed Gate G3 blocker was found.

Two high-severity residual risks are inherited, explicit trust boundaries rather
than previously hidden claims: native RPC plugin permissions are approval labels
without operating-system enforcement, and JavaScript-helper discovery may execute
an unverified binary found through `PATH`. Neither is a critical default-path
vulnerability under the documented alpha deployment model, but both prohibit a
claim that hostile native plugins or a hostile executable search path are safely
contained. They should be resolved before broadening that model.

One new medium resource-hardening defect exists in the macOS Chromium cookie
importer. Two new low-severity cancellation/filesystem-hardening defects were also
found. None bypasses pack signatures, updater trust, browser credential
cryptography, network TLS verification, WASM isolation, or the Python-free
invariant.

| Severity | New/unreviewed | Inherited and explicit | Total |
| --- | ---: | ---: | ---: |
| Critical | 0 | 0 | 0 |
| High | 0 | 2 | 2 |
| Medium | 1 | 0 | 1 |
| Low | 2 | 0 | 2 |

Severity is based on impact within the declared alpha boundary. Critical means a
supported default path permits unauthenticated code execution, credential
disclosure, signature bypass, or uncontrolled overwrite. High means code
execution or isolation failure after a meaningful local trust precondition.
Medium means bounded-scope denial of service or a platform guarantee gap. Low
means hardening or availability risk requiring a local race or adversarial API
caller.

## Findings

### P3-SP-01: native RPC permissions are not syscall-enforced (High, inherited and explicit)

An approved native plugin is executed directly with `exec.Command` in
`internal/plugin/rpc/client.go:167-173`. Unix isolation creates only a process
group (`internal/plugin/rpc/process_unix.go:17-36`); Windows creates a kill-on-close
Job Object (`internal/plugin/rpc/process_windows.go:18-53`). Neither path applies
the filesystem, network, process, or resource plan exposed by
`internal/sandbox/sandbox.go:68-93`. A production-source search found no call to
`sandbox.Prepare`.

Consequently, a correctly signed and approved native plugin receives the user's
ambient OS filesystem and network access even if its manifest omits those
permissions. Sanitized argv/environment, exact identity-bound approval, package
revalidation, strict framing, and process-tree termination limit several attack
paths but do not enforce the advertised permissions.

This is explicit in `docs/P2_PLUGIN_ABI_V1.md:103-106,158-164`, which calls approval
a trust gate rather than a syscall sandbox, and in
`docs/P2_PLUGIN_THREAT_MODEL.md:13-17,67-89`. The latter says a valid publisher
signature does not make behavior safe and defines fail-closed adapters, but the
RPC launch path does not integrate them.

Required disposition: wire the native host to a fail-closed platform sandbox and
operation-scoped writable/read-only roots, or rename the permissions as review
metadata and formally exclude hostile native plugins. Do not grant `secrets` or
`cookies` until an operation-scoped broker exists, as already required by
`docs/P2_PLUGIN_THREAT_MODEL.md:54-65`.

### P3-SP-02: JavaScript helper may be resolved from an unverified `PATH` (High, inherited and explicit)

`pkg/ytdlp/client.go:792-809` checks an explicitly configured helper, then the
application directory, then `exec.LookPath("ytdlp-js-helper")`.
`internal/javascript/supervisor/supervisor.go:41-49,123-138` resolves and executes
that path without checking an expected digest, owner/mode, build identity, or
protocol version before process creation. Protocol framing, a sanitized
environment, bounded stderr, timeout, and memory controls apply only after the
selected binary has already obtained user-level native code execution.

The discovery order is an explicit permanent policy in
`docs/P2_FALLBACK_INVENTORY.md:19-25`; this audit therefore classifies the issue as
inherited rather than concealed. A writable or attacker-controlled search-path
directory can turn JavaScript-helper use into arbitrary native execution.

Required disposition: prefer an absolute configured path or a helper beside the
main executable, verify an expected digest/release identity and safe file
attributes, and make `PATH` discovery opt-in or unavailable in a hardened mode.
Until then, document `PATH` integrity as a deployment prerequisite.

### P3-SP-03: macOS Chromium import lacks cookie/value resource bounds (Medium, new)

The macOS importer limits each database/WAL snapshot to 2 GiB and stabilizes the
opened source identity (`internal/cookies/chromium/snapshot.go:12-14,99-149`), but
its row loop has no maximum cookie count, no per-value bound, and no validation of
cookie host/name/value/path (`internal/cookies/chromium/import.go:78-118`). Its
public options expose no `MaxCookies` field (`internal/cookies/chromium/types.go:36-43`).

This is inconsistent with Linux Chromium, which defaults to 1,000,000 rows,
validates fields, and caps encrypted values at 16 MiB
(`internal/cookies/chromiumlinux/import.go:64-91`), Firefox's equivalent row/field
checks (`internal/cookies/firefox/firefox.go:99-121`), and Windows Chromium's row,
value, and field checks (`internal/cookies/chromiumwindows/import.go:94-99,138-141`).
A malformed, opt-in local profile can therefore force excessive allocation or
admit oversized/control-bearing cookie fields. Exploitation requires selection of
the local database and is primarily availability impact.

Required disposition: add a hard/default row cap, bounded string/encrypted-value
sizes, shared cookie validation, `ErrLimit`, and deterministic success/failure and
fuzz evidence matching the other importers.

### P3-SP-04: Linux Chromium and Firefox snapshot opens have a path-swap race (Low, new)

Linux Chromium checks `os.Lstat`, then separately calls `os.Open`, but never
compares `input.Stat()` with the inspected file
(`internal/cookies/chromiumlinux/import.go:209-225`). Firefox uses the same pattern
(`internal/cookies/firefox/firefox.go:240-253`). A concurrent replacement between
those operations can make the copier read a symlink target or different regular
file after the initial type/size check.

The macOS implementation demonstrates the stronger pattern by comparing the
opened handle with the pre-open identity (`internal/cookies/chromium/snapshot.go:104-125`).
The practical impact is low because an attacker able to race files in the user's
browser profile is normally same-UID, which the project threat model excludes
(`docs/P2_PLUGIN_THREAT_MODEL.md:19-24`), and cookie import is opt-in.

Required disposition: use a no-follow open where available and validate the
opened handle's type, size, and identity before copying; recheck after the copy.

### P3-SP-05: differential cancellation can strand a blocking read goroutine (Low, new)

`ParseObservation` accepts an arbitrary `io.Reader`. Its cancellation wrapper
starts `io.ReadAll` in a goroutine and returns on cancellation; it can interrupt
only readers that also implement `io.Closer`
(`internal/differential/shadow.go:153-178`). A blocking non-closable reader leaves
that goroutine alive indefinitely. The bounded `LimitReader` caps bytes, not time
or a read that never returns.

This does not disclose sanitized observations, and ordinary files, pipes, and
HTTP bodies are closable. It is an availability issue for adversarial library
callers.

Required disposition: document the closable-reader requirement or redesign the
API around a caller-owned interruptible source. Add a deterministic non-closable
blocking-reader lifecycle test without itself leaking a goroutine.

## Surface review

| Surface | Security/privacy controls reviewed | Result |
| --- | --- | --- |
| Netrc and browser credentials | Netrc file type, mode, link, size and parser bounds; canonical host lookup; categorized secret-safe failures. Browser import uses private snapshots, read-only SQLite, bounded databases, cancellation, platform-native key stores/DPAPI, host-bound ciphertext where applicable, password zeroing, and redacted public errors. | Pass except P3-SP-03 and P3-SP-04. No real credentials were used. |
| Telemetry privacy | `internal/telemetry` accepts only constructor-allowlisted extractor/capability identifiers and a closed outcome enum; no raw URL, metadata, credential, arbitrary-label, or error-message API exists (`internal/telemetry/telemetry.go:1-7,106-179`). Snapshots are bounded, deterministic, and timestamp-free. Export destination, authentication, retention, and user consent remain outside this in-memory component. | Pass for the stated aggregate boundary. |
| Differential privacy/redaction | Shadow observations sanitize at JSON persistence and comparison boundaries; URLs, fragments, sensitive query names, headers, credential handles, nested metadata, formats, playlists, manifests, and warnings are reduced/redacted (`internal/differential/shadow.go:101-109,413-473`). Shadow comparison sanitizes both sides before constructing differences. | Pass, with P3-SP-05 availability caveat. Generic `Document`/`diffcheck` reports exact values and is suitable only for the documented pre-sanitized fixture workflow (`docs/P1_DIFFERENTIAL_REVIEW.md:8`), not live secret-bearing inputs. |
| Network, proxy, and impersonation | Unknown profiles fail closed; environment proxies are explicitly disabled for impersonated transport; explicit proxy syntax is validated; TLS uses normal verification and optional caller roots; credential headers are stripped across unsafe redirects and redacted from public failures. | Pass. Known protocol/fingerprint fidelity deviations are compatibility issues, not trust bypasses. |
| JavaScript engine and helper | Fresh goja runtime, no ambient filesystem/network bindings, source/module/result bounds, context interruption, strict bounded supervisor protocol, sanitized environment, crash categorization, and process termination were reviewed. | In-process engine passes; external helper trust is limited by P3-SP-02. Soft memory/process-tree platform limitations remain explicit. |
| Plugin SDK/RPC/WASM | SDK v1.0/v1.1 exact negotiation and capability dispatch, strict JSON/framing, cancellation, secret-safe failures, and native-only author validation pass. Host RPC revalidates signed package identity/digest/manifest, requires exact approval, rejects interpreters/Python, bounds messages/stderr, and kills process trees. WASM uses wazero without WASI or imports, with digest revalidation, memory-page and timeout limits, and strict output validation (`internal/plugin/wasm/host.go:102-164`). | WASM passes its constrained isolation claim. Native RPC remains subject to P3-SP-01. |
| Signed packs, catalogs, and upgrades | Canonical Store-only ZIPs, bounded archive/path rules, domain-separated Ed25519 signatures, derived key IDs, explicit trust/time, expiry/revocation/downgrade checks, exact offline catalog resolution, permission-delta review, atomic Unix lifecycle, and recovery journals were inspected. | Pass for declared platforms. Windows lifecycle remains fail-closed. The standalone pack-upgrade contract is evidence, not installer integration, and must not be represented otherwise. |
| Filesystem and external commands | Download destinations are confined and symlink-aware; outputs are exclusive/atomic; diagnostics are bounded/redacted. External downloader, ffmpeg/ffprobe, keychain, update health checker, and Git replay use argv-based execution without a shell. Explicit executable selection remains a trusted-code boundary. | Pass for the documented boundary, with P3-SP-04 and inherited platform residuals below. |
| Cancellation and limits | Request bodies, manifests, frames, archives, catalog data, telemetry cells, JS/WASM work, native plugin lifetime/stderr, downloads, and media operations have bounded controls and cancellation tests. | Pass except P3-SP-03 and P3-SP-05. Native CPU/address-space/process quotas remain explicit deviations. |
| Python-free dependency paths | Production source tripwires reject Python invocation/reference-checkout coupling. `go.mod` contains Go dependencies only. The Python-free Dockerfile checks the build image has no `python`/`python3`, builds/tests in Go, and copies only static Go binaries, CA certificates, and notices into `scratch` (`.github/python-free.Dockerfile:1-37`). | Pass. No runtime or build-time dependency on Python or `/Users/tejas/projects/yt-dlp-reference` was found. Docker itself was not run in this independent audit. |

## Inherited explicit deviations

The following prior findings remain visible and were not reclassified as new:

- `G2-S01` (Medium): Windows update health-check start-to-Job-assignment race.
- `G2-S02` (Medium): Windows updater-root owner/DACL and directory durability
  guarantees require deployment policy.
- `G2-S03` (Medium): deprecated macOS `sandbox-exec` and unsupported resource
  quotas fail closed.
- `G2-S04` (Low): release output directory assumes a single-owner CI workspace.
- `G2-S05` (Informational): Windows signed-pack lifecycle is unavailable and
  verification-only.
- Native plugin path revalidation-to-exec is not handle-atomic; native portable
  CPU/address-space/process-count limits are absent; Windows RPC has a narrow
  start-to-Job-assignment race; WASM has wall-clock but no instruction-fuel
  accounting (`docs/P2_PLUGIN_ABI_V1.md:223-231`).
- The SDK requires its input `Close` to interrupt a blocked read, and a handler
  that ignores context can delay in-process SDK shutdown; the host's process
  supervisor remains the hard-stop boundary.

These deviations remain acceptable only within their documented scope. In
particular, P3-SP-01 and P3-SP-02 must not be converted into broader isolation or
untrusted-discovery claims merely because the deterministic tests pass.

## Verification evidence

All commands ran locally from the clean audit worktree at the audited commit. No
CI services, live credentials, browser profiles, publication systems, or network
canaries were used.

| Command | Result |
| --- | --- |
| `go test ./...` | Pass, all packages. |
| Focused `go test -race` over credentials, browser cookies, telemetry, differential, network/impersonation, JavaScript, plugin/RPC/WASM, packs/catalog/upgrades, sandbox, downloader/media, plugin SDK, and product packages | Pass, all scoped packages. |
| `go vet ./...` | Pass, no diagnostics. |
| 100-execution fuzz runs: `FuzzParse`, `FuzzMacDecryptor`, `FuzzDecryptBoundaries`, `FuzzParseObservation`, `FuzzReadFrame`, `FuzzDecodeResponse`, pack `FuzzVerify`, and `FuzzServerMalformedInput` | Pass. For targets with more than 100 seed inputs, Go completed the requested 100 baseline executions and did not enter mutation; no failure occurred. |
| `go mod tidy -diff` | Pass, no module drift. |
| Production static scans for Python/PyPy execution, reference-checkout strings, sandbox integration, external command launches, filesystem operations, proxy/TLS controls, and credential/redaction fields | No Python/reference dependency; identified findings above. |

This audit did not run `govulncheck`, Docker, live browser import, live network
impersonation, release publication, or platform-native execution on Linux/Windows.
Those require separate reproducible/local or CI evidence and are not inferred from
cross-builds or unit fixtures.

## Gate G3 statement

There is no critical unreviewed G3 blocker at the audited commit. Gate acceptance
must nevertheless preserve the narrowed claims in this report: constrained WASM
is the hostile-plugin isolation boundary; native RPC is signed-and-approved code,
not permission-enforced hostile code; and JavaScript-helper `PATH` integrity is a
deployment prerequisite until P3-SP-02 is remediated. P3-SP-03 should be fixed
before macOS browser-cookie support is promoted beyond alpha because it is the
only new medium finding in this audit.
