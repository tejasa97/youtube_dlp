# ADR 0003: Browser impersonation transport

Status: Accepted; transport engine replaced after the Phase 2 licensing review

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

## Phase 1 selection (2026-07-17)

The pilot selects `github.com/bogdanfinn/tls-client` v1.9.2 and its Chrome 133
profile under the stable product name `chrome-133`. It composes uTLS
ClientHello behavior, fhttp HTTP/2 settings, ordered browser headers, proxy
support, and a cookie-jar adapter behind the existing standard `net/http`
boundary. Unknown profiles fail explicitly; native transport remains the
default.

The current v1.14+ line has newer HTTP/3/browser support but requires Go 1.24.1.
The v1.9.2 pin originally retained this project's Go 1.23 floor. Raising the
toolchain and updating the browser profile is an independent reviewed change,
not an implicit side effect of this pilot. Direct `refraction-networking/uTLS`
was not selected because it does not reproduce HTTP/2 behavior or header order
by itself.

The project subsequently raised its floor to Go 1.25 to consume fixed
`golang.org/x/net` and `cloudflare/circl` releases after the Gate G2
vulnerability audit. This does not silently expand the declared impersonation
profile: upgrading tls-client remains a separate conformance decision.

The deterministic gate rejects native Go TLS and accepts only the pinned hybrid
group plus required Chrome headers, while also proving shared cookies, bounds,
and cancellation. A live fingerprint canary remains separately controlled and
must not become a CI dependency.

## Transport replacement (2026-07-18)

The implementation now uses `github.com/imroc/req/v3` v3.59.0 (MIT) with
`github.com/refraction-networking/utls` v1.8.2 (BSD-3-Clause). The public
`chrome-133` profile name is unchanged. Its TLS ClientHello, ordered HTTP/2
SETTINGS, connection flow, pseudo-header order, regular-header order, proxy
support, cancellation, custom roots, and shared standard-library cookie jar are
configured explicitly behind the existing `net/http` boundary.

The replacement retains the deterministic hybrid-group protected-flow gate and
adds behavior tests for profile configuration, caller request immutability,
cookie continuity, cross-origin credential stripping, response-boundary shape,
and HTTP/1 header ordering. Explicit HTTP proxies retain the prior all-target
CONNECT behavior, including authentication, and environment proxy variables
remain ignored unless the product supplies a proxy. Neither `bogdanfinn/fhttp` nor
`bogdanfinn/tls-client` remains in the module or production dependency graph.

This resolves the fhttp licensing blocker. A project-wide license is still a
separate prerequisite for public distribution.
