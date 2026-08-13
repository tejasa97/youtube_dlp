# ADR 0002: JavaScript runtime isolation

Status: Accepted

## Context

Modern extraction, especially YouTube challenge handling, requires JavaScript.
The implementation must not depend on Python, and challenge programs must not
share unrestricted memory or process privileges with the CLI.

## Decision

JavaScript executes through a versioned request/response protocol in a
supervised helper process. The helper uses the reviewed pure-Go goja engine and
pinned EJS assets. Engine details remain behind the process protocol rather
than becoming extractor API.

Every request has wall-clock, memory, source-size, output-size, and module
allowlist limits. The helper has no ambient network or filesystem API. It is
terminated on cancellation, malformed protocol data, or budget breach. The EJS
assets are checked against the pinned SHA3-512 allowlist at startup.

Chromium is a separate explicit browser workflow and is not the default
JavaScript evaluator.

## Consequences

The product remains Python-free, helper failures remain outside host operation
memory, and JavaScript engine details do not alter extractor contracts. The
native helper and its exact assets remain an explicit distribution and trust
boundary.
