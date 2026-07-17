# Controlled live canary

This observation is current interoperability evidence, not an authoritative CI
fixture. It contains no cookies, credentials, response body, or raw capture.

- Observed at: `2026-07-17T16:21:19Z`
- Command: `go run ./cmd/impersonationcheck`
- Endpoint: `https://tls.peet.ws/api/all`
- Profile: `chrome-133`
- Engine: `github.com/bogdanfinn/tls-client@v1.9.2`
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
