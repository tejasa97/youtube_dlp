# Phase 3 operations evidence

This lane supplies deployment-neutral primitives rather than enabling live
canaries in the product. A versioned suite separates public, credential, and
region canaries. Targets are bounded references resolved only by an injected
runner; credential entries carry secret handles, never secret values.

Execution requires explicit opt-in, applies a per-canary deadline, propagates
cancellation, and exports only canary ID, class, extractor, closed outcome and
failure categories, an optional predeclared capability, and timing. Target,
region, secret handle, runner error text, URLs, titles, and media metadata are
excluded from records. Tests prove a runner error containing a synthetic secret
cannot reach serialized evidence. Runner panics are recovered and reduced to
the same closed, redacted failure class; panic values are never retained.

Rolling metrics retain bounded record and incident windows. They report exact
success, breakage, fallback, unsupported, credential/region unavailable,
canceled, and timeout counts with integer basis-point rates. Repair evidence is
a strict breakage → diagnosis → patch → successful-verification state machine.
The deterministic major-site fixture proves the 24-hour tier; tests also prove
the 24–48-hour and missed-48-hour tiers.

`ytdlp-ops validate-suite` canonicalizes an offline suite, and
`ytdlp-ops summarize` validates redacted record/incident sets against that
suite before emitting the bounded metrics snapshot. The command performs no
canary network execution and never resolves target or secret handles.

## Deployment deviations

- No scheduler, dashboard, alert sink, credential resolver, regional runner, or
  network transport is wired by this lane.
- Production runners must resolve target/secret handles through an approved
  deployment policy and honor context cancellation. The executor returns on its
  timeout even for a faulty runner, but Go cannot forcibly terminate that
  runner's leaked goroutine.
- Live canaries must remain opt-in and non-authoritative. Deterministic fixtures
  are still required before a live observation can support a parity claim.
- Retention, export consent, access controls, and alert thresholds remain
  product/deployment policy owned by later integration work.
