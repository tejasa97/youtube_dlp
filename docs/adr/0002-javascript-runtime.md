# ADR 0002: JavaScript runtime isolation

Status: Accepted for the Phase 1 experiment

## Context

Modern extraction, especially YouTube challenge handling, requires JavaScript.
The implementation must not depend on Python, and untrusted or rapidly changing
scripts must not share unrestricted memory or process privileges with the CLI.

## Decision

JavaScript execution will use a versioned request/response boundary implemented
by a supervised helper process. The first experiment will compare a pure-Go
ECMAScript engine with a QuickJS-family engine against an EJS/challenge corpus.
Engine choice is deliberately behind the process protocol.

Every request receives wall-clock, memory, source-size, output-size, and module
allowlist limits. The helper has no ambient network or filesystem access. It is
terminated on context cancellation, malformed protocol data, or budget breach.
Chromium may be a separate explicit browser workflow; it is not the default JS
evaluator.

## Consequences

The product remains Python-free and engine replacement does not alter extractor
interfaces. Process startup and distribution cost are accepted for isolation.
Phase 1 must prove EJS execution and deterministic cancellation before the
runtime is used by a production extractor.
