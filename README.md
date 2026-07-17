# yt-dlp Go Port

This repository is the planning and implementation workspace for a Go port of
[yt-dlp](https://github.com/yt-dlp/yt-dlp). The intended final system is
Python-free while retaining yt-dlp's user-visible capabilities wherever they
are technically and legally reproducible.

Status: Phase 0 is implemented. The Python-free walking skeleton supports the
deterministic fixture extractor and generic direct HTTP media, ordered metadata
JSON, safe output templates, structured progress, cancellation, atomic file
completion, retries, and validated range resume. Broader site and protocol
support starts in Phase 1 and is not claimed here.

## Build and test

Go 1.23 or newer is required.

```sh
go test ./...
go test -race ./...
go vet ./...
go run ./cmd/paritycheck
go run ./cmd/paritycheck -summary
go run ./cmd/ytdlp-go --version
```

The capability manifest is the source of truth for what is implemented. A
`compatible` Phase 0 entry has named automated evidence; future capabilities
remain explicitly `not_started`.

## Project documents

- [Port evaluation](GO_PORT_EVALUATION.md)
- [Zero-Python program plan](ZERO_PYTHON_GO_PORT_PLAN.md)
- [Phase 0 implementation plan](PHASE_0_IMPLEMENTATION_PLAN.md)
- [Contribution and verification rules](CONTRIBUTING.md)
- [Phase 0 exit review](docs/PHASE_0_EXIT_REVIEW.md)
- [Architecture decisions](docs/adr/README.md)
- [Fixture policy](docs/FIXTURE_POLICY.md)
- [Third-party notices](THIRD_PARTY_NOTICES.md)

No compatibility claim should be made until a capability has a passing
conformance test. Compatibility targets behavior and workflows, not Python API
or ABI compatibility.
