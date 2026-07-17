# yt-dlp Go Port

This repository is the planning and implementation workspace for a Go port of
[yt-dlp](https://github.com/yt-dlp/yt-dlp). The intended final system is
Python-free while retaining yt-dlp's user-visible capabilities wherever they
are technically and legally reproducible.

The Phase 1 JavaScript path is also Python-free: the product supervises a
separate pure-Go helper and embeds hash-pinned EJS JavaScript assets for the
selected YouTube challenge corpus. No Python executable, library, helper, or
plugin is loaded at runtime.

Status: Phase 0 is complete and Phase 1 is in progress. Completed Phase 1
milestones include HLS/DASH fragment pipelines, ffmpeg supervision,
compatibility parser pilots, differential comparison, and the isolated EJS
challenge path. The first offline YouTube video corpus now covers player
metadata, direct and ciphered formats, `n`/signature transformation, and
HLS/DASH manifest exposure through the product registry. Playlists, broader
site coverage, browser-cookie import, plugins, and upstream-delta replay are
still in progress and are not claimed complete. The versioned Chrome 133
TLS/HTTP2 impersonation pilot is available for protected extractor flows.

## Build and test

Go 1.23 or newer is required.

```sh
go test ./...
go test -race ./...
go vet ./...
go run ./cmd/paritycheck
go run ./cmd/paritycheck -summary
go run ./cmd/ytdlp-go --version
go build -o /tmp/ytdlp-js-helper ./cmd/ytdlp-js-helper
go run ./cmd/jscheck -helper /tmp/ytdlp-js-helper
```

YouTube challenge extraction uses the separate `ytdlp-js-helper` executable.
Place it beside `ytdlp-go`, put it on `PATH`, or pass its path with
`--js-helper`. Pages whose formats do not require a JavaScript challenge do not
start the helper.

The browser-profile live canary is intentionally opt-in and excluded from CI:

```sh
go run ./cmd/impersonationcheck
```

The capability manifest is the source of truth for what is implemented. A
`compatible` entry has named automated evidence; incomplete capabilities remain
explicitly `partial` or `not_started`.

## Project documents

- [Port evaluation](GO_PORT_EVALUATION.md)
- [Zero-Python program plan](ZERO_PYTHON_GO_PORT_PLAN.md)
- [Phase 0 implementation plan](PHASE_0_IMPLEMENTATION_PLAN.md)
- [Phase 1 implementation plan](PHASE_1_IMPLEMENTATION_PLAN.md)
- [Contribution and verification rules](CONTRIBUTING.md)
- [Phase 0 exit review](docs/PHASE_0_EXIT_REVIEW.md)
- [Architecture decisions](docs/adr/README.md)
- [Fixture policy](docs/FIXTURE_POLICY.md)
- [Third-party notices](THIRD_PARTY_NOTICES.md)

No compatibility claim should be made until a capability has a passing
conformance test. Compatibility targets behavior and workflows, not Python API
or ABI compatibility.
