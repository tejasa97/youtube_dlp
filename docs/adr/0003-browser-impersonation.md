# ADR 0003: Browser impersonation transport

Status: Accepted for the Phase 1 experiment

## Context

Some services fingerprint TLS, HTTP/2, header ordering, and browser behavior.
Go's standard HTTP transport cannot claim a current browser fingerprint, while
making every request impersonated would add fragility and a large maintenance
surface.

## Decision

The shared transport contract remains smaller than any concrete HTTP stack.
Native `net/http` is the default. Extractors may request a named impersonation
profile, which selects a separate transport implementation with an explicit
capability error when unavailable.

Phase 1 will evaluate maintained uTLS/fHTTP-style candidates against one pinned
protected fixture and one live canary. Profiles are versioned data, credentials
remain in the common cookie jar boundary, and logs expose the selected profile
without exposing secrets.

## Consequences

Most traffic stays simple and standard-library based. Protected paths can evolve
without forking extractor APIs. No Phase 0 code claims browser impersonation.
