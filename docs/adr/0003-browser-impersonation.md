# ADR 0003: Browser impersonation transport

Status: Accepted

## Context

Some services fingerprint TLS, HTTP/2, header ordering, and browser behavior.
Go's standard HTTP transport cannot claim a browser fingerprint, while applying
impersonation to every request would increase fragility and maintenance cost.

## Decision

The shared transport contract remains smaller than any concrete HTTP stack.
Native `net/http` is the default. An extractor may explicitly request a named
impersonation profile; an unavailable or unknown profile returns a capability
error.

The current implementation uses `github.com/imroc/req/v3` with
`github.com/refraction-networking/utls`. The public `chrome-133` profile pins
TLS ClientHello behavior, HTTP/2 settings and flow, pseudo-header and regular
header order, proxy behavior, cancellation, custom roots, and cookie-jar
integration behind the common transport boundary.

Credentials remain within the common cookie and request-isolation policy. Logs
may identify the selected profile but must not expose secrets.

## Consequences

Most traffic uses the standard transport. Protected paths opt into a bounded,
versioned fingerprint without changing extractor APIs. Profile behavior is a
compatibility claim only within its deterministic evidence; it is not a claim
to reproduce every browser behavior.
