# Controlled live canary baseline

This observation is current interoperability evidence, not an authoritative CI
fixture. It contains no cookies, credentials, response body, or raw capture.

- Observed at: `2026-07-17T16:21:19Z`
- Command: `go run ./cmd/impersonationcheck`
- Endpoint: `https://tls.peet.ws/api/all`
- Profile: `chrome-133`
- Engine: `github.com/bogdanfinn/tls-client@v1.9.2` (pre-replacement baseline)
- HTTP status/protocol: `200`, `HTTP/2.0`
- Observed user agent: `Chrome/133.0.0.0` on the pinned Windows header profile
- JA3 hash: `1f2eee44e1c45c73d909e29a90efabbd`
- PeetPrint hash: `1d4ffe9b0e34acac0bd883fa7f79d7b5`
- HTTP/2 fingerprint hash: `52d84b11737d980aef856699f885ca86`
- Bounded body SHA-256: `cbc25427123a87fa911448ba2385d104137ed2ff15bbe3b435ff03c938d7d519`

The canary tool emits only bounded normalized evidence. Reruns may change
the JA3 hash because the selected profile intentionally randomizes TLS extension
order, and response-body hashes may change as the service evolves. PeetPrint and
HTTP/2 hashes were stable across the host and scratch-image verification runs.
Such changes require review but do not alter the deterministic protected-flow
gate.

## Replacement observation

- Observed at: `2026-07-18T15:49:23Z`
- Command: `go run ./cmd/impersonationcheck`
- Endpoint: `https://tls.peet.ws/api/all`
- Profile: `chrome-133`
- Engine: `github.com/imroc/req/v3@v3.59.0`
- HTTP status/protocol: `200`, `HTTP/2.0`
- Observed user agent: `Chrome/133.0.0.0` on the pinned Windows header profile
- JA3 hash: `1fdf219227b364b41623e0ad0d89dfa2`
- PeetPrint hash: `1d4ffe9b0e34acac0bd883fa7f79d7b5`
- HTTP/2 fingerprint hash: `52d84b11737d980aef856699f885ca86`
- Bounded body SHA-256: `d50afd94620888145ac34fe49494aee6c418866e53fa4f6540485444f63833b5`

The replacement preserved both the PeetPrint and HTTP/2 hashes exactly. The
JA3 difference is expected because both implementations randomize TLS extension
order. The deterministic local gate remains authoritative.
