# yt-dlp Go Port

This repository is the planning and implementation workspace for a Go port of
[yt-dlp](https://github.com/yt-dlp/yt-dlp). The intended final system is
Python-free while retaining yt-dlp's user-visible capabilities wherever they
are technically and legally reproducible.

The Phase 1 JavaScript path is also Python-free: the product supervises a
separate pure-Go helper and embeds hash-pinned EJS JavaScript assets for the
selected YouTube challenge corpus. No Python executable, library, helper, or
plugin is loaded at runtime.

Status: Phase 0 and the Phase 1 risk-retirement pilot are complete; Phase 2
native-alpha development is underway. Phase 1
includes HLS/DASH pipelines, ffmpeg supervision, compatibility parser pilots,
differential comparison, isolated EJS execution, browser impersonation and
cookie import, RPC/WASM plugin experiments, and representative generic,
YouTube, Vimeo, Twitch, SoundCloud, TikTok, authenticated, and regional
extractors. Compatibility remains scoped to each checked-in corpus and its
explicit deviations; this is not yet a broad yt-dlp parity release. See the
[Phase 1 exit review](docs/PHASE_1_EXIT_REVIEW.md) for the gate evidence and
the time-bounded upstream-replay limitation.

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

On macOS, an explicitly selected Chrome profile can seed the operation cookie
jar. This may trigger the normal Keychain authorization prompt:

```sh
go run ./cmd/ytdlp-go --cookies-from-browser chrome:Default URL
```

See [Chromium cookie import](docs/CHROMIUM_COOKIE_IMPORT.md) for its security
model and current platform boundary.

The capability manifest is the source of truth for what is implemented. A
`compatible` entry has named automated evidence; incomplete or deliberately
bounded capabilities remain explicit as `partial`, `not_started`, or
`intentional_deviation`.

## Project documents

- [Port evaluation](GO_PORT_EVALUATION.md)
- [Zero-Python program plan](ZERO_PYTHON_GO_PORT_PLAN.md)
- [Phase 0 implementation plan](PHASE_0_IMPLEMENTATION_PLAN.md)
- [Phase 1 implementation plan](PHASE_1_IMPLEMENTATION_PLAN.md)
- [Phase 2 implementation plan](PHASE_2_IMPLEMENTATION_PLAN.md)
- [Contribution and verification rules](CONTRIBUTING.md)
- [Phase 0 exit review](docs/PHASE_0_EXIT_REVIEW.md)
- [Phase 1 exit review](docs/PHASE_1_EXIT_REVIEW.md)
- [Phase 1 differential review](docs/P1_DIFFERENTIAL_REVIEW.md)
- [Architecture decisions](docs/adr/README.md)
- [Fixture policy](docs/FIXTURE_POLICY.md)
- [Chromium cookie import](docs/CHROMIUM_COOKIE_IMPORT.md)
- [Playlist extraction model](docs/PLAYLIST_MODEL.md)
- [Third-party notices](THIRD_PARTY_NOTICES.md)

No compatibility claim should be made until a capability has a passing
conformance test. Compatibility targets behavior and workflows, not Python API
or ABI compatibility.
