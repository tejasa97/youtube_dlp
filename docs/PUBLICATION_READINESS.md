# Publication-readiness review

Review date: 2026-07-19
Reviewed baseline: `62d23cf` plus the publication and documentation changes

## Decision

The source repository is suitable to make publicly visible based on the local
checks in this review once the repository owner accepts Apache License 2.0.
This is a practical engineering and provenance review, not legal advice.
Making source public does not itself authorize publishing production-signed binaries,
enable private vulnerability reporting, or establish signing-key custody.

The repository remote is currently `github.com/tejasa97/youtube_dlp`, while
`go.mod` declares `github.com/ytdlp-go/ytdlp`. This does not block public source
visibility or builds inside a checkout, but it must be reconciled before the
project advertises normal `go get` installation, tags a public Go module, or
promises stable external module resolution.

## Licensing

Apache License 2.0 was selected for the project because it is permissive and
includes an explicit patent grant. The pinned upstream behavioral reference is
Unlicense; no upstream Python source is copied into production Go code. The
direct production dependency graph and retained embedded assets were reviewed:
their declared licenses are Apache-2.0, BSD-family, ISC, MIT, or Unlicense.
`THIRD_PARTY_NOTICES.md`, retained bundle banners, and
`third_party/licenses/` preserve the material notices identified by the review.
The supplementary `go-licenses v1.6.0` check cannot load Go 1.25's standard
library module metadata and is therefore not treated as evidence; the pinned
module review and the release system's SPDX/license-coverage tests are the
authoritative automated checks.

Contributors are told that intentional submissions are Apache-2.0 unless
explicitly marked otherwise. Service names and the yt-dlp name remain their
owners' marks; the README and NOTICE now state the project's independent,
unaffiliated status.

## Secret and credential review

Gitleaks v8.30.1 scanned the complete locally reachable Git history with
`gitleaks git --log-opts=--all` across every locally reachable commit and the
current tree. Additional targeted patterns checked for private-key headers and
common AWS, GitHub, Google, and Slack token formats. No real secret was found.

Scanner findings reduced to the following reviewed non-secret categories:

- two ARD public API routes and an NRK public hostname list;
- a deterministic Chromium password test vector documented by adjacent
  provenance;
- a deterministic Windows Chromium AES key vector documented by adjacent
  provenance;
- Twitch's public web client identifier, matching the identifier in the pinned
  yt-dlp behavioral reference.

`.gitleaks.toml` suppresses only those exact rule, path, and line combinations.
The secret-scan workflow is prepared to check complete history on pushes, pull
requests, and weekly once GitHub Actions is re-enabled. Any broader or changed
value remains detectable.

The non-fuzz job logs from failed GitHub Actions runs `29650623857` and
`29657803150` were also scanned without a finding. GitHub's own masking remains
necessary for values supplied through Actions secrets. Raw logs are not
retained in this repository.

## Fixture publication safety

The refreshed review covered all 51 `conformance/**/PROVENANCE.md` records and
all 118 other fixture/data files under `conformance/` (excluding the capability
manifest). The corpus consists of generated or hand-authored responses,
reserved or invented identifiers, deterministic non-production keys and
credentials, schema-derived expectations, and attributable differential
expectations. No real account cookie, access token, personal data, private
response, captured media, production signing key, or expiring signed URL was
found.

The embedded yt-dlp EJS 0.8.0 assets retain their Unlicense/ISC/MIT banner and
hash/version provenance. Real service hostnames used in synthetic routing tests
are public identifiers, not credentials or captured service data. The fixture
policy now requires this classification and a repeat publication review for
future additions.

## CI and operational state

The verification baseline uses Go 1.25.12 and runs formatting/tidy drift, vet,
parity, symbol-aware vulnerability analysis, deterministic fuzz targets,
cross-builds, host-native tests where available, race tests, media tests, and a
Python-free container. GitHub Actions is temporarily disabled, so this publication decision
does not depend on a hosted run. The Windows filesystem/config/fixture defects
exposed by the first hosted dry runs were fixed in commits `19957c2` through
`b374c5b`; local native macOS tests, race tests, and no-cgo cross-builds provide
the evidence recorded here. Gate G2's native Windows lifecycle observation
remains a separate alpha-release limitation, not a source-visibility blocker.

Before changing visibility:

1. Retain the passing local verification record for the exact published commit.
2. Confirm that the owner accepts Apache-2.0 and the public commit author
   names/emails already present in Git history.
3. Keep releases disabled until signing custody, publishing credentials, and
   the Phase 2/Phase 3 operational gates are explicitly approved.
4. Decide whether to publish at the declared canonical module path or migrate
   `go.mod`; until then, keep external module installation documented as
   unavailable rather than relying on Git redirection behavior.

Immediately after changing visibility:

1. Enable GitHub private vulnerability reporting and verify that the Security
   contact link opens the private advisory form. The API does not expose this
   feature for the repository in its current private state.
2. Enable branch protection for `main`. GitHub currently reports that branch
   protection requires either a public repository or an account-plan upgrade.
3. Verify the public README, issue forms, support links, license detection, and
   unauthenticated clone from a signed-out session.

These post-visibility settings should be completed in the same publication
window. They do not require GitHub Actions to be enabled.

## Local verification record

The integrated publication candidate passed the following without relying on
GitHub Actions:

- Gitleaks v8.30.1 over all locally reachable commits and the working tree: zero
  unallowlisted findings;
- formatting, module-tidy drift, `go vet ./...`, `go test ./...`, and
  `go test -race ./...`;
- `actionlint v1.7.7` over every checked-in workflow;
- parity validation: 55 capabilities and zero temporary fallbacks;
- `govulncheck v1.6.0 ./...`: zero reachable vulnerabilities;
- no-cgo amd64 builds for Linux, macOS, and Windows;
- the two release fuzz targets at 100 mutations each;
- Python-free scratch-image build, full in-image Go tests, and native execution
  of the product, JavaScript probe, pack, updater, and release tools;
- an assembled alpha archive containing the project `LICENSE`, an Apache-2.0
  project entry in `LICENSES.txt`, and `Apache-2.0` in its SPDX package record.
