# Third-Party Notices

## gopkg.in/yaml.v3 v3.0.1

Used only by the capability-manifest tooling. The module declares a combination
of MIT licensing for files ported from libyaml and Apache License 2.0 for its
remaining files. Its complete `LICENSE` and `NOTICE` files are distributed in
the Go module source and should be included by any source redistribution process.

## gopkg.in/check.v1

Present in the module graph as a transitive testing dependency of YAML v3. It is
licensed under a three-clause BSD-style license and is not linked into the
`ytdlp-go` production binary.

## Test data

The Phase 0 HTTP fixture server generates its media bytes and responses in Go.
No captured yt-dlp site response, media file, or upstream source code is included
in the repository.

## goja

The isolated JavaScript helper uses `github.com/dop251/goja`, an ECMAScript
engine licensed under the MIT License. The exact module revision and transitive
dependency versions are recorded in `go.mod` and `go.sum`.

## yt-dlp-ejs 0.8.0 bundles

The Go binary embeds the official `yt.solver.core.min.js` and
`yt.solver.lib.min.js` assets from `yt-dlp-ejs` 0.8.0. EJS is Unlicense. The
library bundle contains Meriyah 6.1.4 under the ISC License and Astring 1.9.0
under the MIT License. Their complete generated license banner remains embedded
in `yt.solver.lib.min.js`; provenance and upstream allowlist hashes are recorded
in `conformance/javascript/ejs-0.8.0/PROVENANCE.md`.

## Browser impersonation stack

The optional `chrome-133` transport profile uses
`github.com/bogdanfinn/tls-client` v1.9.2 (BSD-4-Clause),
`github.com/bogdanfinn/fhttp` v0.5.34, and
`github.com/bogdanfinn/utls` v1.6.5 (BSD-3-Clause), together with their pinned
transitive dependencies in `go.sum`. Binary redistributions must reproduce the
tls-client BSD-4-Clause notice and acknowledgement. The exact upstream
tls-client and uTLS texts are retained under `third_party/licenses/`.

The fhttp v0.5.34 module does not ship a repository-level license file. Many
files retain the Go Authors BSD-style source header, but that is not treated as
a project-wide license conclusion here. Public binary distribution of this
dependency therefore requires explicit legal review or replacement. Exact
technical selection provenance is recorded in
`conformance/network/impersonation/PROVENANCE.md`.

The project itself still requires an explicit project-license decision before a
public release; this notice does not grant a license to the project code.
