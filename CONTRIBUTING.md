# Contributing

This repository is an independent Go implementation informed by yt-dlp's
observable behavior. Do not submit AI-generated work from this repository to
upstream yt-dlp: the upstream project explicitly does not accept it.

## Local checks

The minimum checks for every change are:

```sh
gofmt -w cmd internal pkg
go mod tidy
go vet ./...
go test ./...
go test -race ./...
go run ./cmd/paritycheck
```

The CI Python-free job additionally builds and tests in an Alpine Go image that
does not contain Python, then runs the static executable in a `scratch` image.

## Compatibility claims

Every capability is tracked in `conformance/parity_manifest.yaml`. A capability
may be marked `compatible` only when its manifest entry links to passing,
automated evidence. Unsupported behavior should fail explicitly and remain
`partial` or `not_started`.

## Design rules

- All blocking APIs accept `context.Context`.
- Core packages emit structured events; they do not render terminal output.
- Extractors describe media and downloaders transfer it. Neither assumes the
  other's responsibilities.
- Metadata must preserve field order, unknown fields, and missing-versus-null
  semantics.
- Avoid global mutable operation state.
- New dependencies need a documented purpose, a compatible license, active
  maintenance, and a clear advantage over the standard library.
- Fixtures must be deterministic, small, license-safe, and free of real
  credentials or account data.
- Generated files must identify their generator and be reproducible in CI.

## Change shape

Keep foundational changes independently reviewable and leave the repository
building after each change. Interface changes should update all consumers and
their conformance evidence together.
