# ytdlp-go

This is an independent Go implementation informed by the observable behavior
of [yt-dlp](https://github.com/yt-dlp/yt-dlp). It is not affiliated with,
endorsed by, or sponsored by yt-dlp, GitHub, Google/YouTube, or the operators of
supported services. Product and service names are used only to identify
compatibility targets.

The intended final system is Python-free while retaining yt-dlp's user-visible
capabilities wherever they are technically and legally reproducible.

The Phase 1 JavaScript path is also Python-free: the product supervises a
separate pure-Go helper and embeds hash-pinned EJS JavaScript assets for the
selected YouTube challenge corpus. No Python executable, library, helper, or
plugin is loaded at runtime.

Status: Phase 0, Phase 1, and the Phase 2 repository implementation are
complete. Gate G2 awaits a clean native Windows observation of the assembled
alpha lifecycle; Phase 3 beta work now has an active, local-first implementation
plan. Phase 2 includes the compatibility foundation, downloader and
postprocessor matrices, secure plugin and signed-pack boundaries, reproducible
alpha assembly, and 25 representative native extractors. Compatibility remains
scoped to each checked-in corpus and its explicit deviations; this is not yet a
broad yt-dlp parity release. See the [Phase 2 exit
review](docs/PHASE_2_EXIT_REVIEW.md) and [security
review](docs/P2_SECURITY_REVIEW.md) for the precise evidence and limitations.

## Build and test

Go 1.25.12 or newer is required. The floor includes security-fixed networking and
cryptographic transitive dependencies used by browser impersonation.

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
- [Phase 3 implementation plan](PHASE_3_IMPLEMENTATION_PLAN.md)
- [Contribution and verification rules](CONTRIBUTING.md)
- [Phase 0 exit review](docs/PHASE_0_EXIT_REVIEW.md)
- [Phase 1 exit review](docs/PHASE_1_EXIT_REVIEW.md)
- [Phase 1 differential review](docs/P1_DIFFERENTIAL_REVIEW.md)
- [Phase 2 exit review](docs/PHASE_2_EXIT_REVIEW.md)
- [Phase 2 security review](docs/P2_SECURITY_REVIEW.md)
- [Publication-readiness review](docs/PUBLICATION_READINESS.md)
- [Security policy](SECURITY.md)
- [Architecture decisions](docs/adr/README.md)
- [Fixture policy](docs/FIXTURE_POLICY.md)
- [Chromium cookie import](docs/CHROMIUM_COOKIE_IMPORT.md)
- [Playlist extraction model](docs/PLAYLIST_MODEL.md)
- [Third-party notices](THIRD_PARTY_NOTICES.md)

No compatibility claim should be made until a capability has a passing
conformance test. Compatibility targets behavior and workflows, not Python API
or ABI compatibility.

## License

Project code is licensed under the [Apache License 2.0](LICENSE). Embedded
assets and dependencies retain their own licenses; see [third-party
notices](THIRD_PARTY_NOTICES.md).
