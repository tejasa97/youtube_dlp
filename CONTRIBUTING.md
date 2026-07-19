# Contributing to ytdlp-go

ytdlp-go is an independent Go implementation informed by yt-dlp's observable
behavior. Contributions are welcome when they keep compatibility claims
evidence-backed, preserve the Python-free boundary, and do not expose user or
service credentials.

- [Before opening an issue](#before-opening-an-issue)
- [Choosing the right report](#choosing-the-right-report)
- [Development setup](#development-setup)
- [Making a change](#making-a-change)
- [Adding or changing an extractor](#adding-or-changing-an-extractor)
- [Compatibility and fixture evidence](#compatibility-and-fixture-evidence)
- [Design rules](#design-rules)
- [Submitting a pull request](#submitting-a-pull-request)
- [Licensing and upstream separation](#licensing-and-upstream-separation)

## Before opening an issue

Read [Support](SUPPORT.md), search existing issues, and reproduce against the
current `main` revision. Keep one independently actionable problem per issue.

For a bug, include:

- the exact revision from `git rev-parse HEAD` and output of
  `ytdlp-go --version`;
- the operating system, architecture, and whether ffmpeg, ffprobe, a browser
  cookie source, an impersonation profile, or the JavaScript helper was used;
- the smallest command and public URL that reproduce the problem;
- expected behavior, actual behavior, and plain-text diagnostic output; and
- whether the same URL shape previously worked in ytdlp-go.

Remove cookies, authorization headers, signed query parameters, local paths,
usernames, titles of private media, and other personal data. Do not upload
browser profiles or captured private responses. Report security-sensitive
behavior privately under [SECURITY.md](SECURITY.md).

This repository is not the yt-dlp issue tracker. Reproduce the behavior in
ytdlp-go before filing it here, and do not submit work produced for this
repository to upstream yt-dlp.

## Choosing the right report

### Regression in a listed extractor

Name the extractor from [Supported extractors](docs/SUPPORTED_SITES.md), give a
safe public example URL, and explain which documented URL family or behavior
regressed. A listed extractor covers its declared corpus, not every page,
account state, region, or future response from that service.

### New site or URL shape

Provide a legal, public example URL with obvious playable media. State whether
the service uses a backend already represented in the catalog and whether the
content requires login, payment, a particular region, live state, browser
impersonation, or JavaScript. Requests involving only DRM-protected media are
out of scope because ytdlp-go does not implement DRM decryption.

### Feature request

Describe the user problem and why the existing CLI, Go API, plugin, or
configuration mechanisms do not solve it. For broad API, architecture,
security-boundary, or compatibility-language changes, discuss the design
before investing in an implementation.

## Development setup

Go 1.25.12 or newer is required. Clone the repository and run:

```sh
go mod download
go test ./...
go run ./cmd/ytdlp-go --help
```

Python is neither required nor permitted in the production, build, test,
plugin, or conformance path. The separately pinned upstream checkout may be
used read-only to derive attributable expectations, but code and tests in this
repository must run without it.

Before submitting a change, run the local gate:

```sh
test -z "$(gofmt -l .)"
go mod tidy -diff
go vet ./...
go test ./...
go test -race ./...
go run ./cmd/paritycheck
```

Run relevant fuzz targets and no-cgo cross-builds when changing parsers,
protocols, packaging, platform code, or release behavior. The
[publication-readiness review](docs/PUBLICATION_READINESS.md) records the wider
local gate used while GitHub Actions is disabled.

## Making a change

- Keep commits coherent and leave the repository buildable after each one.
- Add deterministic success and failure tests. Add cancellation, limit,
  malformed-input, and fuzz evidence where the changed boundary needs it.
- Return a categorized error for unsupported behavior; do not silently fall
  back to Python, a shell, or a less safe execution path.
- Update user documentation and the capability manifest in the same change
  when observable behavior changes.
- Do not mark a capability `compatible` until its manifest entry points to
  passing automated evidence.
- Preserve unrelated work and avoid generated or bulk-formatted churn.

## Adding or changing an extractor

Prefer a reusable shared-backend extractor when several sites expose the same
player or API. A platform extractor should normally include:

1. conservative URL matching and routing tests, including near-miss URLs;
2. deterministic, publication-safe fixtures with adjacent `PROVENANCE.md`;
3. normalized metadata and format expectations;
4. explicit handling of playlists, pagination, live state, authentication,
   region restrictions, manifests, or anti-bot behavior that it claims;
5. categorized tests for malformed, unavailable, authentication, and
   unsupported responses;
6. cancellation and resource-limit tests for blocking or unbounded paths;
7. fuzz coverage for newly introduced parsers or response decoders; and
8. catalog, documentation, and conformance-manifest updates.

Route a specific extractor before the generic extractor. Do not add real
account credentials, expiring signed URLs, captured private pages, or
production client secrets to fixtures. Live canaries may detect drift, but
they do not promote a compatibility claim.

## Compatibility and fixture evidence

Every capability is tracked in `conformance/parity_manifest.yaml`. A
`compatible` entry is scoped to the corpus named in that entry; it is not a
claim of total yt-dlp parity.

Follow [the fixture policy](docs/FIXTURE_POLICY.md). Derived expectations must
identify the pinned upstream revision, relevant source or test location, the
derivation method, and all intentional normalization or deviation. Prefer
small hand-authored or generated fixtures over copied service responses.

If upstream behavior is unclear, preserve the uncertainty as an explicit
deviation. Never execute Python from a test to decide whether it passes.

## Design rules

- All blocking APIs accept `context.Context` and propagate cancellation.
- Core packages emit structured events; they do not render terminal output.
- Extractors describe media and downloaders transfer it. Neither assumes the
  other's responsibilities.
- Metadata preserves field order, unknown fields, and missing-versus-null
  semantics where the public model promises them.
- Avoid global mutable operation state.
- New dependencies need a documented purpose, compatible license, active
  maintenance, and a clear advantage over the standard library.
- Plugins and helper processes require explicit trust, bounded messages,
  permissions, resource limits, and categorized failure behavior.
- Generated files identify their generator and are reproducible.

## Submitting a pull request

A pull request should explain the user-visible outcome, compatibility scope,
known deviations, security impact, and tests run. Link its issue when one
exists. Keep broad refactors separate from behavior changes unless separating
them would make the change unsafe.

Review your diff for credentials, private data, unrelated changes, stale
claims, generated artifacts, and third-party license obligations. Hosted CI is
temporarily disabled, so include the exact local checks you ran; maintainers
will not treat a green badge or an absent check as evidence.

## Licensing and upstream separation

Unless explicitly stated otherwise, contributions intentionally submitted for
inclusion are licensed under the Apache License 2.0, as described by section 5
of the [project license](LICENSE). Do not submit code, fixtures, or assets that
you lack the right to contribute.

yt-dlp's repository has its own contribution rules, including restrictions on
AI-assisted submissions. This project does not grant permission to bypass
them. Do not submit issues, patches, fixtures, prose, or generated work from
this repository to upstream yt-dlp, and do not imply that this project is
affiliated with or endorsed by upstream.
