# Phase 1 exit review

Review date: 2026-07-18

Reference: `yt-dlp/yt-dlp@aefce1eea4d0b6bab1ec2bd3beff09bff91a39c8`

## Outcome

Phase 1's risk-retirement pilot is complete. The Go product, helper, protocol
downloaders, representative extractors, and plugin experiments have no Python
build-time or runtime dependency. All compatible claims are backed by named
automated evidence in `conformance/parity_manifest.yaml`; the manifest has no
Phase 1 `partial`, `not_started`, or `blocked` entry.

One literal scheduling limitation is retained as an intentional deviation:
eight weeks *after* the 2026-07-14 reference pin do not exist on the review
date. P1-11 therefore replayed the real consecutive eight-week history ending
at the pin. The checked-in Go inventory tool can perform the forward refresh
after 2026-09-08. This does not require a Python bridge or an architecture
change.

## Work-package disposition

| Package | Result | Principal evidence |
| --- | --- | --- |
| P1-01 differential runner | complete | `internal/differential`, pilot policy fixture, and `P1_DIFFERENTIAL_REVIEW.md` |
| P1-02 HLS core | complete | bounded/fuzzed parser; byte-range, map, AES-128, retry/resume, VOD/live polling and cancellation tests; product dispatch |
| P1-03 DASH core | complete | inherited static/dynamic addressing, timelines including negative repeat, polling/de-duplication/cancellation, separate-track download and product merge tests |
| P1-04 ffmpeg/ffprobe | complete | discovery, probe, merge, remux, bounded/redacted diagnostics, process cancellation, atomic cleanup, generated-media end-to-end tests |
| P1-05 parser prototypes | complete | pinned format-selector/output-template corpus, exact syntax failures, deterministic resolution, and fuzz targets |
| P1-06 isolated JavaScript/EJS | complete | versioned framed helper, limits and crash recovery, pinned EJS assets/corpus, challenge integration, scratch-image helper execution |
| P1-07 impersonating transport | complete | explicit Chrome 133 profile, cookie/proxy/cancellation/redaction tests, protected offline flow, and separately controlled live canary |
| P1-08 Chromium cookie import | complete | macOS Chrome v10 key/decrypt corpus, schema/WAL/path/cancellation tests, operation-jar integration, secret-safe diagnostics |
| P1-09 extractor pilots | complete | generic, YouTube, Vimeo, Twitch, SoundCloud, TikTok, synthetic authenticated, and SVT Play corpora; product-registry evidence |
| P1-10 plugin spikes | complete | RPC and WASM negotiation, permissions, limits, cancellation, malformed/crash/timeout handling, fuzzing, examples, and cross-platform build/test jobs |
| P1-11 delta replay | intentional scheduling deviation | classified 114 commits across a real eight-week window, replay review, reusable Go classifier, provenance, and regression tests |

## Gate G1

| Criterion | Disposition and evidence |
| --- | --- |
| 1. Direct HTTP, HLS VOD/live, DASH | pass: deterministic product and package end-to-end tests, all Python-free |
| 2. ffmpeg/ffprobe deterministic output | pass: license-safe generated media verifies probe, merge, remux, checksum/finalization, cancellation, and cleanup |
| 3. bounded JavaScript/EJS | pass: pure-Go helper executes the pinned EJS/YouTube corpus with time, input/output, module, and memory controls |
| 4. impersonating transport | pass: Chrome 133 protected-flow fixture plus opt-in live-canary evidence; no silent native fallback |
| 5. Chromium cookie import | pass: opt-in macOS Chrome import with operation-scoped jar and redacted diagnostics |
| 6. representative extractor risks | pass: simple, playlist/API, live, authenticated, manifest-heavy, anti-bot, and regional pilots are registered in the product |
| 7. differential semantics | pass: no unreviewed critical difference in the declared corpora; accepted boundaries are explicit in the manifest and differential review |
| 8. RPC and WASM plugins | pass: both spikes prove negotiation, permissions/limits, cancellation and hostile-plugin survival; RPC selected as Phase 2 primary |
| 9. eight-week delta absorption | pass for architectural risk, with the post-pin calendar wording retained as an intentional time-bounded deviation |
| 10. Python-free processes/artifacts | pass: source tripwire, no-cgo cross-builds, Alpine build without a Python command, and scratch runtime execution |

## Verification record

The exit run performs:

```text
test -z "$(gofmt -l .)"
go mod tidy -diff
go run ./cmd/paritycheck
go test ./...
go test -race ./...
go vet ./...
all checked-in fuzz targets (short smoke budget)
CGO_ENABLED=0 cross-builds for linux/amd64, darwin/amd64, windows/amd64
docker build -f .github/python-free.Dockerfile ...
scratch-image version and JavaScript-helper checks
```

The CI workflow preserves formatting/tidy/vet/parity, three-platform tests,
race tests, ffmpeg media tests, every Phase 1 fuzz target, no-cgo cross-builds,
and the Python-free Docker gate.

## Python-free audit

`internal/conformance.TestProductionSourcesDoNotInvokePython` rejects hard-coded
Python process launches and cgo imports in production Go sources. The Docker
builder proves `python` and `python3` are absent before running parity and all
tests. Release binaries are built with `CGO_ENABLED=0`; the final image is
`scratch` and contains only the Go product/helper/check binaries, CA bundle,
and license notices. The upstream checkout is neither copied nor referenced by
the build.

Python may be mentioned in planning, provenance, tests, or audit text. Such
mentions are not dependencies and are deliberately retained so the invariant
is reviewable.

## Phase 2 handoff

RPC is the primary plugin direction; WASM remains useful for pure sandboxed
extensions. Phase 2 must design trusted discovery/signing/update policy before
automatic loading, expand configuration and archive compatibility, and widen
site/options coverage only with new conformance evidence. The forward P1-11
refresh becomes possible after 2026-09-08 and should update the existing
inventory rather than reopen Phase 1 architecture.
