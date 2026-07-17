# Browser impersonation pilot fixture

The protected-flow fixture is synthetic and deterministic. Its TLS server
rejects clients that do not offer curve/group `0x11ec` (X25519MLKEM768 in the
pinned uTLS profile), then requires Chrome 133 navigation headers and a cookie
created through the shared standard-library jar. Native Go 1.23 TLS does not
offer this profile and is expected to fail the handshake.

The selected stack is
[`bogdanfinn/tls-client` v1.9.2](https://github.com/bogdanfinn/tls-client/tree/v1.9.2),
commit `d704c12210816fb90deeb2ef68f9afbbdb50f2ce`. This is the last tagged line
available during the 2026-07-17 evaluation that retains a Go 1.22 module floor;
v1.10.0 and later require Go 1.24.1. It combines a uTLS ClientHello with fhttp
HTTP/2 settings and ordered headers, whereas direct uTLS alone covers only the
TLS ClientHello.

The test uses an ephemeral local certificate trusted through a test-only root
pool. It contains no captured site traffic, credentials, or external network
dependency. Live fingerprint observations are deliberately separate from this
authoritative CI fixture.
