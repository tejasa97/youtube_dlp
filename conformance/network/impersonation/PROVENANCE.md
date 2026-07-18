# Browser impersonation pilot fixture

The protected-flow fixture is synthetic and deterministic. Its TLS server
rejects clients that do not offer curve/group `0x11ec` (X25519MLKEM768 in the
pinned uTLS profile), then requires Chrome 133 navigation headers and a cookie
created through the shared standard-library jar. Native Go TLS does not
offer this profile and is expected to fail the handshake.

The selected stack is
[`imroc/req` v3.59.0](https://github.com/imroc/req/tree/v3.59.0), commit
`57c964406ef49bac80f73c5e0ec4376c514df7d6`, with
[`refraction-networking/utls` v1.8.2](https://github.com/refraction-networking/utls/tree/v1.8.2).
It replaced `bogdanfinn/tls-client` v1.9.2 and `bogdanfinn/fhttp` v0.5.34 on
2026-07-18 because the pinned fhttp module had no repository-level license.

The Chrome 133 expectations were derived from the previously pinned
`profiles.Chrome_133`: uTLS `HelloChrome_133`, ordered HTTP/2 settings
`HEADER_TABLE_SIZE=65536`, `ENABLE_PUSH=0`, `INITIAL_WINDOW_SIZE=6291456`, and
`MAX_HEADER_LIST_SIZE=262144`; connection flow `15663105`; and pseudo-header
order `:method`, `:authority`, `:scheme`, `:path`. The regular header order and
Windows Chrome navigation headers remain the repository's previously reviewed
product profile. These values are now explicit versioned Go data rather than
being inherited implicitly from a dependency profile.

The test uses an ephemeral local certificate trusted through a test-only root
pool. It contains no captured site traffic, credentials, or external network
dependency. Live fingerprint observations are deliberately separate from this
authoritative CI fixture.
