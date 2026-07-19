# Continuous integration

GitHub Actions is temporarily disabled. Branch protection does not require a
hosted status check, and no phase or compatibility claim depends on an Actions
result. The workflows are retained so they can be enabled deliberately when
hosted execution is wanted.

## Workflow tiers

| Workflow | Trigger when enabled | Jobs | Purpose |
| --- | --- | ---: | --- |
| `CI` | Pull requests and pushes to `main` | 1 Linux job | Full-history Gitleaks, formatting, module drift, vet, parity validation, and `go test ./...` |
| `Deep validation` | Weekly schedule and manual dispatch | 9 jobs | Six deterministic fuzz shards, native macOS and Windows tests, and one Linux job for race, vulnerability, media, cross-build, and Python-free Docker evidence |
| `Python-free alpha artifacts` | Manual dispatch only | 4 jobs | Three native build/lifecycle jobs plus deterministic release-set assembly |

This replaces the previous design, which created roughly seventy jobs on both
branch pushes and pull requests. Ordinary changes now consume one hosted Linux
job, and a branch push does not duplicate its pull-request run. Concurrency
cancels stale `CI` runs for the same ref.

## Why the deeper checks are separate

Race testing, every fuzz target, ffmpeg integration, Docker, vulnerability
database access, and native hosted runners are valuable but disproportionately
expensive on every edit. Running them weekly and on demand preserves broad
drift detection without making routine contributions fan out across dozens of
runners.

The six fuzz shards discover all `Fuzz*` targets from the Go package list, sort
packages and targets, and assign them by stable modulo. A newly added target is
therefore included automatically without expanding the workflow matrix.

Native macOS and Windows unit tests remain in deep validation. The alpha
workflow separately exercises native release binaries and updater lifecycle
behavior only when release artifacts are explicitly requested.

## Permissions and pinning

Every third-party action is pinned to an exact commit with its release version
recorded in a comment. The regular workflow grants only `contents: read`.
Gitleaks 8.30.1 is downloaded from its official release, verified against the
publisher's SHA-256 digest, and invoked directly over `--all` history.

Historical run `29696796244` used `gitleaks-action` and failed with HTTP 403
before scanning because that action attempted to enumerate PR commits without
`pull-requests: read`. Direct CLI execution removes that API dependency,
avoids PR write/comment permissions, and scans more than the action's bounded
PR commit range.

## Re-enabling workflows

Enable `CI` first and observe it on a small pull request before making it a
required branch-protection check. Enable `Deep validation` only when weekly or
manual hosted evidence is desired. Keep the alpha workflow disabled until
release authority, signing custody, and artifact-publication policy are
approved.

Local verification in [Contributing](../CONTRIBUTING.md) remains authoritative
while Actions is disabled.
