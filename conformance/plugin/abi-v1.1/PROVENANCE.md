# Plugin ABI v1.1 upgrade fixture provenance

The fixture in this directory was authored specifically for the Go port's
Phase 3 compatible-upgrade conformance harness. It contains synthetic plugin
manifests and exchange messages only. It contains no captured site response,
credential, executable, copyrighted media, or Python-derived byte sequence.

`compatible-extractor-upgrade.json` models an ABI 1.0 extractor moving to the
inclusive 1.0–1.1 range. The 1.1 exchange adds data only through the existing
`request.options` and `response.metadata` extension maps. A 1.0 consumer can
ignore the new option while all candidate response metadata survives the
language-neutral JSON exchange. Identity, runtime, entrypoint, capabilities,
and permissions remain invariant.

The pinned yt-dlp checkout at `/Users/tejas/projects/yt-dlp-reference` commit
`aefce1eea4d0b6bab1ec2bd3beff09bff91a39c8` was not needed to derive these
fixtures. The contract and expectations are original Go-port work.

Known deviation: the current public envelope decoder rejects unknown
top-level struct fields. Consequently this evidence deliberately confines the
compatible 1.1 extension to the ABI's existing open maps. A future ABI that
adds optional top-level fields must first add an explicit extension-retention
policy to `pkg/pluginapi`; this harness does not claim that behavior today.

The automated rejection matrix also proves that Python runtimes, a new ABI
major, minor-range downgrades, capability expansion, permission escalation,
changed required fields, duplicate JSON keys, unknown envelope fields,
oversized fixtures, and malformed exchanges cannot be reported compatible.
