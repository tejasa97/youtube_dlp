# Impersonation Profile Provenance

## Firefox 120

- Public ID: `firefox-120`.
- Engine source: `github.com/imroc/req/v3@v3.59.0`,
  `client_impersonate.go`, `ImpersonateFirefox` and its adjacent ordered
  settings, priority, pseudo-header, header-order, and default-header tables.
- TLS source: `github.com/refraction-networking/utls@v1.8.2`, exact immutable
  `HelloFirefox_120` parrot. No floating `HelloFirefox_Auto` alias is used.
- Capture date: 2026-07-19.
- Capture method: the factual engine tables were transcribed into typed Go
  metadata, canonically serialized, and checked against `firefox-120.json`.
  Local raw HTTP/1.1 capture verifies that req emits the configured header
  sequence. TLS and HTTP/2 interoperability use the pinned engine's exact ID,
  SETTINGS, flow, PRIORITY frames, header priority, and pseudo-header order.
- Licensing: req is MIT licensed; uTLS carries a BSD-style license. The fixture
  contains factual numeric/string configuration rather than source code.
- Sanitization: no network capture, credentials, cookies, or personal data are
  present. Hostnames in tests are loopback or reserved `.example` names.

## Safari limitation

No Safari profile is exposed. req v3.59.0 labels its helper Safari 16.6 but
configures uTLS `HelloSafari_16_0`; the available engine therefore cannot make
one version-consistent Safari claim. Relabeling that hybrid as either Safari
16.0 or 16.6 would invent unsupported fidelity. A future profile requires a
version-matched ClientHello and attributable HTTP/2/header capture.
