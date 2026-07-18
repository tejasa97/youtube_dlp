# Phase 3 semantic review ledger

This ledger records bounded Phase 3 shadow reports and their disposition. It
does not convert live observations into compatibility claims. Reports must be
produced with `shadowcheck`, retain no raw secrets, and identify only an
attributable fixture or approved deployment window.

| Observation set | Source | Report state | Critical | Disposition | Owner |
| --- | --- | --- | ---: | --- | --- |
| `conformance/differential/phase3/reference.json` vs `go.json` | synthetic mechanism corpus | reviewed equal | 0 | comparator/redaction foundation accepted; not beta traffic evidence | conformance |

No deployment shadow window has been submitted. Therefore Gate G3 criterion 3
is not yet demonstrated for beta traffic, even though the checked-in mechanism
corpus has no mismatch. Any future critical report remains open until a bounded
fixture, reviewed cause, patch or accepted non-critical deviation, and passing
verification are recorded here.

Local usage:

```text
go run ./cmd/shadowcheck -expected reference.json -actual go.json -fail-severity critical
```

Exit status 3 means the selected threshold was reached. A truncated report also
fails closed because omitted mismatch severities cannot be reviewed safely.
