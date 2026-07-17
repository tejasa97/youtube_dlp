# Differential Conformance Fixtures

`diffcheck` compares a checked-in normalized reference snapshot with a Go
snapshot. It never starts yt-dlp, Python, or any other oracle process.

The comparison document is JSON with `schema_version: 1` and these required
sections:

- `metadata`: normalized extractor metadata.
- `formats`: ordered format records.
- `playlists`: playlist entries or playlist summaries.
- `events`: structured lifecycle events.
- `selection`: selected format and compatibility-parser outcomes.
- `outputs`: filenames, sizes, and SHA-256 checksums.

Additional capability-specific sections are allowed. Object and list order are
significant unless a reviewed policy says otherwise. An absent object field is
different from a field whose value is `null`.

Policies support `exact`, `ordered`, `set`, `ignore`, `tolerance`, and
`redacted_url` modes. Every exception mode requires a reason and owner. Paths
use a bounded JSON-path subset such as `$.metadata.duration` and
`$.formats[*].url`.

Run the checked-in smoke corpus with:

```sh
go run ./cmd/diffcheck \
  -policy conformance/differential/pilot/policy.yaml \
  conformance/differential/pilot/reference.json \
  conformance/differential/pilot/go.json
```

## Fixture provenance

Every reference fixture directory must include a `PROVENANCE.md` recording:

- exact upstream repository and commit or release;
- capture date and command/options used;
- source URL category without credentials or private identifiers;
- license or reason the derived factual output is distributable;
- sanitization steps, including replacement of signed URLs and personal data;
- the behavior and capability the fixture proves.

Oracle generation is a migration-time developer activity outside product and
Python-free CI execution. Captures must also satisfy
[`docs/FIXTURE_POLICY.md`](../../docs/FIXTURE_POLICY.md).
