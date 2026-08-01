# ADR 0006: JavaScript helper launch identity

Status: Proposed design; documentation and evidence only (2026-08-01)

## Decision summary

Keep the current helper-discovery hardening as the supported baseline, but do
not call it launch-bound identity. The baseline is a meaningful reduction of
accidental or search-path code execution: it requires an absolute configured
path or the helper beside the application, rejects symlinks and unsafe Unix
parents, compares the opened file with the inspected file, and can validate an
embedding-supplied SHA-256 digest. The validation handle is then closed and
`exec.Command(path)` resolves the pathname again. That is a residual
same-user/path-replacement race, and the optional digest is not bound to the
bytes eventually executed.

The next implementation should be capability-based and platform-specific:

1. Make Linux the first candidate for a strict, handle-bound launcher using a
   verified descriptor and `fexecve`/`execveat(AT_EMPTY_PATH)`. It must also
   establish that the executable bytes cannot be changed after hashing; a file
   descriptor alone prevents pathname replacement, not writes through another
   reference to the same file.
2. Treat macOS as a protected-path and code-signing deployment design unless a
   native, documented handle launch primitive is proven. Apple’s static code
   validation is path-based and explicitly warns that its result is valid only
   while the code remains unmodified and is not secure under concurrent
   modification.
3. Treat Windows as a native file-identity, publisher-policy, and protected
   installation-root design unless a supported handle-to-image launch mechanism
   is proven. The documented `CreateProcessW` contract takes an executable name,
   not an executable file handle.
4. Expose no portable `LaunchBoundIdentity` claim until each supported target
   has a native race test that proves which bytes ran. A strict mode may fail
   closed on targets that cannot prove the requested property.

This ADR does not implement an OS-specific launcher. A portable-looking change
around `os/exec`, a post-launch inspection, or a signature check followed by a
path-based launch would not close P3-SP-02 and must not be presented as one.

## Context and current flow

The helper is a native executable, not an in-process JavaScript asset. A
successful helper launch gives code running with the host user’s normal OS
privileges; protocol framing, a scrubbed environment, timeouts, memory limits,
and process termination apply after that boundary is crossed.

The current flow is:

```text
configured path / executable sibling
        |
        v
Lstat + canonicalize + parent/file policy
        |
        v
open path -> compare opened identity -> optional SHA-256 over opened bytes
        |
        v
close validation handle
        |
        v
exec.Command(canonical path) -> OS resolves the path again
```

The relevant implementation is
`internal/javascript/supervisor/supervisor.go`:

- `resolveHelper` does not consult `PATH`; the only implicit location is beside
  the main executable.
- `validateHelperFile` checks regular-file status, size, Unix execute/mode and
  owner policy, safe Unix parents, opened-file identity, and the optional
  `ExpectedHelperDigest`.
- `startLocked` calls `validateHelperFile` again, then constructs
  `exec.Command(client.config.Path)` after that validation has returned and its
  file handle has closed.
- `safe_file_other.go` intentionally cannot claim owner or parent-DACL trust
  from portable `os.FileMode` metadata.

The public `ytdlp.WithJavaScriptHelper` option supplies only a path. It does
not currently supply a release digest, signer requirement, or installation-root
policy. `ExpectedHelperDigest` is therefore an internal/embedding-level hook,
not a product-wide release identity guarantee.

## Threat model

### Protected assets and impact

The protected asset is the integrity of native code launched as the JavaScript
helper, plus the host user’s accessible data and credentials. A swapped helper
can read or modify anything available to that user, impersonate the helper
protocol, or use the host process’s ambient access before any protocol limit is
meaningful. This is user-level arbitrary native execution, not inherently a
privilege-escalation claim.

### In-scope attacker

The attacker may be a same-UID local process or a local actor who can influence
the configured helper path and can modify, rename, replace, or rewrite files in
the helper’s directory during validation and launch. The attacker may race the
validation hook, replace a path component, replace the final path with a
symlink or another regular file, or mutate the original file through another
reference after it was hashed.

The attacker is not assumed to have root/Administrator rights, kernel control,
the ability to defeat OS access checks, or a compromised signed release trust
root. If the deployment lets the same user write the helper bytes, the design
must treat those bytes as mutable; “owned by the current user” is not an
attacker-resistant provenance boundary against another process of that user.

### Out of scope for this ADR

This ADR does not solve a compromised kernel or loader, malicious shared
libraries/dependencies loaded by a trusted executable, vulnerabilities in the
helper itself, a hostile administrator, or complete sandboxing/resource
isolation. It also does not claim that a helper can safely be selected from an
arbitrary user-controlled path merely because its protocol is bounded.

## Findings by platform

### Linux and Unix-like systems

POSIX specifies `fexecve`: the executable is determined by a file descriptor
rather than a pathname. Linux provides `execveat`; with an empty path and
`AT_EMPTY_PATH`, the descriptor itself identifies the executable. Linux also
documents that `execveat` may use an `O_PATH` descriptor. `openat2` can further
constrain path resolution with `RESOLVE_NO_SYMLINKS`, `RESOLVE_BENEATH`, and
related flags on kernels that support it.

These primitives close the pathname replacement window between validation and
the exec operation. They do not freeze file contents: the Linux `fexecve`
documentation explicitly says that contents can still change between checksum
and execution unless permissions prevent malicious modification. Therefore a
Linux strict mode needs both:

- descriptor-bound execution; and
- a release/install policy that makes the verified bytes non-writable by the
  attacker, such as a protected root-owned install, or a separately proven
  sealed-image design.

`memfd_create` plus sealing is a possible Linux-only design for binding verified
bytes to an anonymous file description, but it changes loader, diagnostics,
observability, and artifact-update behavior. It must be a separate prototype
with native execution tests, not inferred from the existence of `memfd_create`.

The current Go dependency has Linux syscall-number constants for
`SYS_EXECVEAT`, but the host-side probe did not find a ready `x/sys/unix`
`Execveat` or `Fexecve` wrapper. That is an implementation consideration, not
evidence that a raw syscall or a small OS-specific adapter is safe. The adapter
must respect Go’s process-launch/runtime constraints and must not silently
fall back to pathname execution when strict mode is requested.

`fexecve` is a POSIX interface, but this ADR does not generalize availability
or semantics to every BSD or Unix variant without a target-specific probe and
race test.

### macOS

Apple’s documented `execve` interface is pathname-based. On the development
host, the local SDK man-page and `libSystem` symbol probes found `execve` and
`posix_spawn`, but no `fexecve` or `execveat`; this is a deployment probe, not a
claim about every Darwin derivative. The current Go supervisor also exposes
only path-based `os/exec.Cmd.Path`.

macOS code signing is useful for provenance. `SecStaticCodeCheckValidity` can
validate a static code object against a `SecRequirement`, including a pinned
publisher/team/designated requirement. Apple explicitly says this is secure
only when the code is not concurrently modified and the result remains valid
only while the code is unmodified. A signature check followed by a path-based
launch therefore does not bind the verified signer or bytes to the later
launch.

For signed universal helpers, the verifier must also select the architecture
that will run or validate all slices; Apple warns that different slices need
not have the same signer. Code signing identifies a signer and validates sealed
code under a policy; it is not the same thing as a release-artifact digest and
does not by itself prevent a same-UID path swap.

The practical macOS phase is therefore a protected installation root plus
signature requirement and expected release digest, with the product claim
limited to “validated provenance before launch.” A per-user writable helper
path remains outside a launch-bound identity claim.

### Windows

The documented `CreateProcessW` API accepts `lpApplicationName` as the module
name/path. Its contract warns about path parsing and recommends supplying the
application name explicitly; it does not define an executable-file-handle
parameter. `STARTUPINFOEX` handle lists control inherited handles, not which
image the process loader uses. The standard Go `os/exec.Cmd` API likewise
models `Path`, and its `ExtraFiles` facility is not supported on Windows.

Windows does provide useful validation primitives:

- `CreateFile` can control reparse-point handling and share modes. A handle can
  be inspected without following the final reparse point when the appropriate
  flag is used.
- `GetFileInformationByHandleEx(FileIdInfo)` returns a volume serial number
  and 128-bit file identifier suitable for comparing two open handles.
- `WinVerifyTrust` can ask the Software Publisher Trust Provider to validate a
  PE file’s Authenticode publisher and signed integrity.

Those facilities are not a launch-bound identity proof when verification is
performed on one handle or pathname and `CreateProcessW` later opens by name.
Keeping a validation handle open may block some delete/rename/write requests
depending on requested access and share modes, but Microsoft’s file-sharing
rules and filesystem behavior require a target-specific proof. It must not be
treated as a portable substitute for handle execution.

The practical Windows phase is an administrator-protected or otherwise
explicitly ACL-protected install root, pinned publisher policy where signed
artifacts are available, and a trusted expected digest/release record. A
same-user writable path remains “pre-launch validated only.” If a strict
launch-bound mode is required, Windows must fail closed until a supported
launcher and adversarial test demonstrate the property.

## Secure-parent policy

The current Darwin/Linux parent walk is valuable: every canonical ancestor must
be a directory, not a symlink, owned by root or the effective user, and not
group/world-writable except for a root-owned sticky directory. It reduces
opportunistic path substitution and makes the default sibling deployment less
fragile.

It does not establish immutable identity against a same-UID attacker. The same
user may own the helper and its parent, may rename entries, and may rewrite the
file unless the deployment’s permissions or filesystem policy prevent it. On
Windows, portable Go metadata cannot inspect equivalent owner/DACL trust, so a
secure parent must be a documented native check or an installation/deployment
invariant. “Secure parent” is a prerequisite to a stronger claim, not the
stronger claim itself.

## Digest and release provenance

`ExpectedHelperDigest` is a useful check only when the expected value comes from
trusted release provenance. Computing a digest from the same untrusted path at
startup, or accepting a digest supplied by the same untrusted configuration,
does not establish trust.

A release record should bind at least:

- product and helper name;
- version/channel and target OS/architecture;
- exact artifact size and SHA-256;
- release metadata generation/freshness and a trusted signature or key ID; and
- where applicable, the platform publisher requirement (Apple) or signer
  policy (Windows).

The digest establishes exact bytes; a publisher signature/requirement
establishes an origin policy. Neither one, without launch binding or protected
installation permissions, proves that the bytes later selected by a path-based
launch are the bytes that were validated. The public embedding API should not
silently invent a digest. It should either receive an explicit immutable
release record or retain the narrower current claim.

## Phased recommendation

### Phase 0 — retain and document the baseline (implemented)

- Keep absolute configured path or executable-sibling discovery; never restore
  implicit `PATH` discovery.
- Keep regular-file, no-symlink, file-size, Unix mode/owner, secure-parent,
  opened-identity, and optional expected-digest checks.
- Keep the current public wording: safer pre-launch validation, not
  handle-atomic execution or unconditional release identity.
- Treat `TestSupervisorRejectsHelperPathSwapDuringValidation` as evidence that a
  swap at the injected validation boundary is detected, not as evidence that
  the close-to-`Start` race is closed.

### Phase 1 — Linux handle-bound prototype

Implement an internal Linux-only launcher behind an explicit capability. The
prototype must:

1. open the exact candidate with no-follow and appropriate read/execute
   semantics;
2. validate type, identity, parent policy, size, and trusted expected digest
   from the still-open descriptor;
3. launch through `fexecve` or `execveat(fd, "", ..., AT_EMPTY_PATH)` without
   re-resolving the pathname; and
4. prove that bytes cannot be modified after validation, or use a separately
   proven sealed-image path.

The native test must pause after validation, replace the path and attempt an
in-place mutation, resume launch, and assert both that the replacement did not
run and that the intended artifact identity was observed. If the primitive is
unavailable or any proof step fails, strict mode returns an explicit
unsupported/identity error; it must not fall back to `exec.Command(path)`.

### Phase 2 — macOS and Windows protected-provenance adapters

For macOS, add native static code validation against an expected requirement,
validate the selected universal-binary slice(s), and require a protected
install root for any stronger product claim. For Windows, add native
reparse/file-ID and DACL checks, validate Authenticode against an explicit
publisher policy when used, and bind an expected release digest. Keep the
resulting claim as protected-path provenance until a supported handle launch is
proven.

Do not use a post-launch path/hash check as a substitute: by then the helper has
already crossed the native-code boundary. A suspended-process check is useful
only if the image identity proof is completed before any attacker-controlled
thread can execute and the full mechanism is covered by a native test.

### Phase 3 — capability contract and release integration

Represent launch evidence internally with explicit states such as:

- `prelaunch_validated`: current baseline;
- `protected_path_provenance`: validation plus a deployment-enforced immutable
  or administrator-controlled root; and
- `handle_bound`: a target-specific launch operation proved to use the verified
  file description and byte-integrity policy.

Only the last state may satisfy a future launch-bound identity requirement. The
release/update lane may populate expected helper digests only from signed,
target-scoped release metadata. The public API should expose the strictness
choice and categorized failure, not an unverifiable boolean assertion.

## Testability and evidence requirements

Required evidence before changing status:

- deterministic unit tests for absolute-path, symlink/reparse, parent policy,
  opened identity, digest, and release-record validation;
- a race harness with a controlled pause between validation and launch that
  exercises final-name replacement, ancestor replacement where applicable,
  symlink/reparse substitution, and in-place byte mutation;
- a launch sentinel that proves which fixture executed, not merely that a
  process was created;
- native execution on every claimed OS/filesystem class; cross-compilation is
  only compile evidence;
- tests for missing/unsupported native primitives that assert fail-closed
  behavior in strict mode; and
- release tests that prove the expected digest/publisher requirement is supplied
  by trusted metadata rather than derived from the candidate path.

Safe repository evidence already present includes:

- `TestSupervisorRejectsSearchPathAndSymlinkHelpers`;
- `TestSupervisorRejectsWritableHelper` and
  `TestSupervisorRejectsWritableHelperParent` on Unix;
- `TestSupervisorValidatesOptionalHelperReleaseDigest`;
- `TestSupervisorRejectsHelperPathSwapDuringValidation`; and
- no-cgo compile probes for the supervisor on Linux/amd64, macOS/arm64, and
  Windows/amd64.

Those tests support the current pre-launch validation claim. They do not prove
handle-bound launch identity because the repository has no test that keeps the
verified handle as the launch authority.

## Non-goals

- no portable wrapper that claims to turn `os/exec` pathname launch into
  descriptor launch;
- no `PATH` discovery, shell launch, interpreter fallback, or helper download;
- no claim that protocol bounds sandbox native code or protect credentials after
  the helper has user-level execution;
- no claim that SHA-256 alone identifies a trusted release;
- no claim that Apple code signing or Windows Authenticode closes a TOCTOU race;
- no claim that current-user ownership is hostile-user protection;
- no claim of Windows/macOS handle-atomic launch without native proof; and
- no production signing keys, publisher choices, or release credentials added
  to the repository.

## Truthful status

As of 2026-08-01, P3-SP-02 is partially remediated and remains open in narrowed
form. The default path is safer and fail-closed against implicit search-path
execution. The helper launch is not handle-atomic, and the public product does
not populate `ExpectedHelperDigest`. This ADR records a phased design and test
bar; it does not close the finding or claim launch-bound identity.

## Evidence and source references

Repository evidence:

- [P3 security, privacy, and isolation audit](../audits/P3_SECURITY_PRIVACY_ISOLATION_AUDIT.md)
- [JavaScript helper protocol](../JAVASCRIPT_HELPER_PROTOCOL.md)
- [helper supervisor implementation](../../internal/javascript/supervisor/supervisor.go)
- [helper supervisor tests](../../internal/javascript/supervisor/supervisor_test.go)
- [Phase 2 updater and release foundations](../P2_UPDATER_RELEASES.md)

Platform references:

- [POSIX `fexecve`](https://pubs.opengroup.org/onlinepubs/9799919799/functions/exec.html)
- [Linux `fexecve(3)`](https://man7.org/linux/man-pages/man3/fexecve.3.html)
- [Linux `execveat(2)`](https://man7.org/linux/man-pages/man2/execveat.2.html)
- [Linux `openat2(2)`](https://man7.org/linux/man-pages/man2/openat2.2.html)
- [Apple `SecStaticCodeCheckValidity`](https://developer.apple.com/documentation/security/secstaticcodecheckvalidity%28_%3A_%3A_%3A%29)
- [Apple code-signing limitations](https://developer.apple.com/library/archive/documentation/Security/Conceptual/CodeSigningGuide/Introduction/Introduction.html)
- [Apple code-signing requirements](https://developer.apple.com/library/archive/documentation/Security/Conceptual/CodeSigningGuide/RequirementLang/RequirementLang.html)
- [Microsoft `CreateProcessW`](https://learn.microsoft.com/en-us/windows/win32/api/processthreadsapi/nf-processthreadsapi-createprocessw)
- [Microsoft `CreateFile` reparse and sharing behavior](https://learn.microsoft.com/en-us/windows/win32/api/fileapi/nf-fileapi-createfilea)
- [Microsoft `GetFileInformationByHandleEx`](https://learn.microsoft.com/en-us/windows/win32/api/fileapi/nf-fileapi-getfileinformationbyhandleex)
- [Microsoft `FILE_ID_INFO`](https://learn.microsoft.com/en-us/windows/win32/api/winbase/ns-winbase-file_id_info)
- [Microsoft `WinVerifyTrust`](https://learn.microsoft.com/en-us/windows/win32/api/wintrust/nf-wintrust-winverifytrust)
- [Microsoft Authenticode](https://learn.microsoft.com/en-us/windows-hardware/drivers/install/authenticode)
