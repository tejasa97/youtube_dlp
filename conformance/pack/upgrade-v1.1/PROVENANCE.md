# Extractor pack upgrade v1.1 fixture provenance

These records are deterministic, synthetic conformance fixtures for the Go
extractor-pack compatibility contract. They were generated from the manifests
in `internal/pack/upgrade/contract_test.go` with the fixed, test-only Ed25519
seed declared there. The private key is not a production trust root.

The fixtures were introduced against repository base commit
`b1829c0a4b1ef348be6f4ad05729cc73ef140ac9`. No source, fixture, or key material
was copied from the pinned Python yt-dlp reference. That checkout is not needed
to build, test, sign, or verify these records.

| Fixture | Purpose | SHA-256 |
| --- | --- | --- |
| `signed-v1.0.json` | Old pack contract accepted by v1.0 and v1.1 hosts | `4b5e04ec25124cde3129180581830bf2af33aa6d2cc71166c0fad7c672601a30` |
| `signed-v1.1.json` | Additive v1.1 fields accepted by v1.0 and v1.1 hosts | `5ebb1bc3d621af0262919cf1ed4c5aba92bee9b9f3012363ad3ec52daaa888f1` |

The test suite regenerates both records, compares their complete canonical
bytes, verifies their signatures, and negotiates them with the compatibility
matrix. Any intentional schema or canonicalization change therefore requires
an explicit fixture and provenance update.

Known constraint: this contract models compatibility and authorization at the
pack boundary. Runtime-specific RPC/WASM execution limits remain the
responsibility of the existing pack hosts; the manifest bounds their declared
capabilities and permissions rather than implementing a second runtime.
