# yt-dlp Go Port

This repository is the planning and implementation workspace for a Go port of
[yt-dlp](https://github.com/yt-dlp/yt-dlp). The intended final system is
Python-free while retaining yt-dlp's user-visible capabilities wherever they
are technically and legally reproducible.

Status: Phase 0 foundation implementation is underway. The repository currently
contains the command/API skeleton, capability-manifest checker, ordered metadata
values, and deterministic offline HTTP fixtures. Extraction and downloading are
not implemented yet.

## Build and test

Go 1.23 or newer is required.

```sh
go test ./...
go test -race ./...
go vet ./...
go run ./cmd/paritycheck
go run ./cmd/ytdlp-go --version
```

The capability manifest is the source of truth for what is implemented. The
CLI deliberately returns an explicit error for a media URL until the first
end-to-end extraction path exists.

## Project documents

- [Port evaluation](GO_PORT_EVALUATION.md)
- [Zero-Python program plan](ZERO_PYTHON_GO_PORT_PLAN.md)
- [Phase 0 implementation plan](PHASE_0_IMPLEMENTATION_PLAN.md)
- [Contribution and verification rules](CONTRIBUTING.md)

No compatibility claim should be made until a capability has a passing
conformance test. Compatibility targets behavior and workflows, not Python API
or ABI compatibility.
