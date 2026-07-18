# Phase 3 Impersonation Profiles

The profile registry exposes stable, versioned IDs. A profile is admitted only
when the pinned Go engine can represent its TLS ClientHello, ordered HTTP/2
settings and priority metadata, pseudo-header order, connection flow, default
headers, and HTTP/1 header order without a Python or browser sidecar.

## Supported additions

`firefox-120` uses req v3.59.0 and the exact uTLS v1.8.2
`HelloFirefox_120` parrot. `Fingerprint` returns a defensive public snapshot of
the complete configuration for audit and conformance tooling. Lookup is exact
and fail-closed; callers cannot request a floating "latest Firefox" profile.

The existing explicit proxy dialer remains in use. Profile selection does not
read environment proxy variables, expose proxy credentials, or change request
cancellation semantics.

The public Go request and CLI `--impersonate` option select a fail-closed
default profile for ordinary transport requests. An extractor may still choose
an explicit site-specific profile; that narrower requirement takes precedence
over the request default. Unknown profile IDs are rejected before extraction or
network access.

## Known deviations and blockers

- This is a pinned engine interoperability profile, not a promise that every
  deployment of stock Firefox 120 on every operating system emitted identical
  optional headers. Callers may override request headers, and such overrides
  intentionally reduce fingerprint fidelity.
- req's Firefox multipart boundary generator is not part of the transport
  profile because multipart body construction remains owned by higher layers.
- Safari is deliberately absent: req v3.59.0's Safari 16.6 helper uses the
  uTLS Safari 16.0 ClientHello. The mismatch prevents an honest stable ID.
- QUIC/HTTP/3 is not represented by the current product transport.
