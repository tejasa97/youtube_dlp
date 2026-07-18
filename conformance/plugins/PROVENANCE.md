# Plugin spike fixture provenance

These fixtures were authored for the Go port's Phase 1 plugin boundary. They
contain no captured network response, secret, copyrighted media, or executable
derived from Python.

- Reference repository: `/Users/tejas/projects/yt-dlp-reference`
- Reference commit: `aefce1eea4d0b6bab1ec2bd3beff09bff91a39c8`
- Reference use: behavioral context for the normalized extractor-result shape
  only. No upstream plugin implementation or fixture bytes were copied.
- RPC fixture: deterministic output of `cmd/ytdlp-plugin-rpc-example` for the
  checked-in request.
- WASM fixture: hexadecimal encoding of the module emitted by
  `cmd/ytdlp-plugin-wasm-example`. The module exports protocol version 1 and a
  static extractor response for request ID `one`.

The executable WASM bytes are recovered by hexadecimal decoding. Keeping the
hex representation makes the fixture reviewable and avoids requiring a WAT
compiler, TinyGo, Rust, or Python in the build and test path.
