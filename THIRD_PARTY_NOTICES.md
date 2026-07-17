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

The project itself still requires an explicit project-license decision before a
public release; this notice does not grant a license to the project code.
