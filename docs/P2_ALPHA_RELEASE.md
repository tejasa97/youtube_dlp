# Phase 2 Python-free alpha release procedure

`cmd/ytdlp-release` assembles explicit, already-built Go executables into a
deterministic internal alpha release set. It does not invoke a compiler,
download dependencies, execute Python, select signing keys, publish an artifact,
or contact a release service.

The command reads Go build information from every input and rejects target sets
whose linked module identities differ. It creates normalized `tar.gz` archives
for Unix targets and ZIP archives for Windows, plus a sorted `SHA256SUMS`, a
canonical `release.json`, an SPDX 2.3 dependency SBOM, and a deterministic
license bundle. Retained third-party license files are included separately in
each archive. Inputs and outputs are bounded; existing output paths, symlinked
license inputs, malformed targets, non-Go binaries, and dependency drift fail
closed.

Every artifact must identify the product main package, declare its exact target,
have cgo disabled, and carry trim-path build metadata. Module replacements are
rejected so an unrecorded local fork cannot be hidden behind the expected module
identity. Destination files are published with an exclusive hard-link operation
and are never replaced.

`.github/workflows/alpha-release.yml` is manual and has read-only repository
permissions. Linux, macOS, and Windows each build the product twice from the
same no-cgo inputs, compare complete bytes, run the native executable, and run
the signed native-artifact install/update/rollback/execution test. A Linux
assembly job packages those exact binaries and verifies every checksum.
Artifacts expire after seven days and are not published as a GitHub Release.

The repository has no project-wide distribution license, and the pinned fhttp
dependency has no repository-level license conclusion. Therefore this workflow
is engineering evidence only: public or third-party distribution remains
blocked on owner/legal review or dependency replacement. No production signing
key is selected or generated here. Signed updater scenarios continue to use
deterministic test keys; production root custody and publishing infrastructure
are external policy inputs.
