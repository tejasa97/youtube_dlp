# Phase 3 Windows Chromium cookie evidence

## Delivered

`internal/cookies/chromiumwindows` is a Python-free, cgo-free importer for
Chrome, Chromium, Edge, Brave, Vivaldi, and Opera cookie stores on Windows.
It provides bounded profile discovery or explicit internal paths, native DPAPI
unprotection, Local State key loading, AES-GCM `v10`/`v11` decoding, schema-24
host binding, and an injectable `v20` app-bound boundary. Cookie database and
WAL files are copied through already-open handles into a private snapshot, then
opened query-only.

Inputs reject symlinks/reparse points, non-regular files, hard links, oversized
files, unexpected Unix ownership/modes, and unexpected Windows owner/DACL
entries. Windows opens permit browser read/write/delete sharing while requesting
the reparse point itself. Errors expose categories and counts, never paths,
cookie material, protected bytes, or backend error strings. Cancellation is
checked during discovery, snapshot copying, row iteration, and decryption.

## Automated evidence

- Synthetic modern and legacy SQLite schemas, including a live WAL database.
- Plaintext, injected DPAPI legacy, AES-GCM `v10` and `v11`, injected app-bound
  `v20`, corrupted ciphertext, schema-24 host mismatch, partial success, limits,
  discovery, cancellation, and secret-redaction tests.
- Unix symlink, hard-link, ownership/mode security tests.
- Fixed conformance vector and provenance under
  `conformance/cookies/chromium-windows`.
- Fuzz coverage for arbitrary encrypted framing, host strings, and meta versions.
- `go test`, race detector, vet, and Windows amd64/arm64 no-cgo compile checks.

## Explicit deviations and boundaries

- The pinned reference supports Windows DPAPI legacy values and `v10`; `v11`
  is treated as the same AES-GCM envelope based on its framing. `v20` is not
  bypassed: callers must supply an identity-appropriate app-bound decryptor.
- Discovery is deliberately bounded to the selected profile's `Network/Cookies`
  and legacy `Cookies` locations. It does not recursively scan disks or profiles.
- Strict owner, link, ACL, and mode checks may reject unusual enterprise-managed
  profiles. This is fail-closed and callers receive only `ErrUnsafePath`.
- CI proves portability with synthetic stores and injected decryptors. A real
  Windows profile/DPAPI account integration run remains environment evidence,
  not a hermetic automated test.
