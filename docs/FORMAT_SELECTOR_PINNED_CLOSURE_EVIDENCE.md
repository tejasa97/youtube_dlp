# Pinned format-selector closure evidence

Reference: `yt-dlp/yt-dlp@aefce1eea4d0b6bab1ec2bd3beff09bff91a39c8`.

This record covers the pinned selector contract. The current-upstream delta is documented separately. Production, Go builds, Go tests, and Docker gates read only committed Go/JSON/YAML fixtures: none invoke Python, read the reference checkout, or use the network.

## Fixture provenance

`internal/format/testdata/pinned_closure_matrix.json` is a deterministic index over the already executable selector, sorter, and planner oracles. It uses the repository's selector-fixture schema and pins the reference SHA and `CPython 3.12.13`. Its derivation is a reviewed index, not a fresh Python capture; therefore there is no invented oracle provenance. The underlying fixture derivation and hashes remain in `conformance/compat/format_selector/PROVENANCE.md`, `conformance/compat/format_sorter/PROVENANCE.md`, and `conformance/format_planner/PROVENANCE.md`.

The matrix runner (`internal/format.TestPinnedClosureMatrix`) requires every authoritative case to be assigned exactly once: all **109** selector cases, **41** sorter cases, and **44** planner cases. It rejects missing, unknown, or multiply assigned IDs, and requires every non-passing selector case to be one of the explicit bounded safety deviations. The existing oracle runners then compare actual normalized formats, plans, and sort orders with their committed expected outputs.

## Closure checklist

| Matrix family | Deterministic fixture and executable coverage | Command |
| --- | --- | --- |
| Atoms, aliases/star forms/.N, grouping, `/`/`+`/`,`, precedence; numeric/string/negated/none-inclusive filters, regex, quoting, escaping, missing/mixed values | `selector_conformance.json`; `filter_oracle.json`; `python_regex_oracle.json`; `TestSelectorConformanceCorpus`, `TestFilterOracleFixture`, `TestPythonRegexOracleFixture`, `TestParserParity*`, `TestPinnedClosureMatrix` | `go test ./internal/format -count=1` |
| Normalization and sorting: aliases, limits, derived values, mixed values, stable ordering | `format_sorter_conformance.json`; `TestFormatSorterConformance`, `TestFormatSorter*`, `TestPinnedClosureMatrix` | `go test ./internal/format -count=1` |
| Muxed/audio-only/video-only/incomplete fallback/storyboards/DRM/defaults and all audio/video multistream combinations | `planner_conformance.json`; selector corpus; `TestPlannerConformanceCorpus`, `TestPlannerDefaultSelectorSpecMatchesPinnedPolicy`, `TestPinnedClosureMatrix` | `go test ./internal/format -count=1` |
| Official pinned selector examples and CLI selection controls | The exact ten examples shared by `pinnedOfficialSelectorExamples`, `TestParserParityOfficialExamples`, and `pinned_closure_matrix.json`; `TestRun*Format*`, `TestRunMergeOutputFormatPlumbing`, `TestFormatSelectorInvalidSyntaxFailsBeforeExtraction` | `go test ./internal/format ./internal/cli ./pkg/ytdlp -count=1` |
| Availability Auto/None/Selected/All; direct/HLS/DASH/ISM bounded GET probes, redirects, cookies, cache, timeout/cancellation/limits | `pkg/ytdlp/format_availability_test.go` | `go test ./pkg/ytdlp -run '^TestFormatAvailability' -count=1` |
| Interactive `-f -`: default, retry, EOF/cancellation, JSON/progress separation | `internal/cli/run_test.go`; `pkg/ytdlp/client_test.go` | `go test ./internal/cli ./pkg/ytdlp -run 'InteractiveFormat|FormatSelector' -count=1` |
| Single/two/N-track/mergeall, headers, container selection, cancellation, cleanup, atomic publication and media inspection | `pkg/ytdlp/ntrack_download_test.go`; `docs/FORMAT_NTRACK_EXECUTION_EVIDENCE.md` | `go test ./pkg/ytdlp -run 'NTrack|MergeAll|MergeOutput|TrackTemporary' -count=1` |
| Multi-output download/merge, postprocessors, chapter/SponsorBlock, subtitle/thumbnail download/embed, InfoJSON/sidecars, prints, artifact order, accounting, collisions, rollback, state isolation | `pkg/ytdlp/multioutput_{transaction,lifecycle}_test.go`; `pkg/ytdlp/output_lifecycle_test.go`; `pkg/ytdlp/print_test.go` | `go test ./pkg/ytdlp -run 'MultiOutput|OutputLifecycle|PrintLaterStages' -count=1` |
| Parser/filter/regex/sorter/planner limits and cancellation | `FuzzParserParitySpans`, `FuzzEvaluateSelector`, `FuzzPrepareFormats`, `FuzzFilter`, `FuzzPythonRegex`, `pkg/ytdlp` lifecycle and availability tests | bounded `go test -run=^$ -fuzz=... -fuzztime=100x -parallel=1` commands below |

The N-track and lifecycle tests use repository-owned, tiny deterministic local media fixtures and inspect merged media with `ffprobe` when available. No test fetches external media.

## Marker decision and retained deviations

`compat.format_selector_pilot` is retired and replaced with the compatible `compat.format_selector` capability. `internal/conformance.Manifest.Validate` requires `TestPinnedClosureMatrix`, this evidence record, the matrix fixture, and direct availability, CLI, N-track, and multi-output lifecycle test anchors whenever that replacement exists; `go run ./cmd/paritycheck` therefore rejects an unsupported marker removal.

There are no unresolved functional deviations for normal valid inputs inside the declared pinned contract. The retained entries in `docs/FORMAT_SELECTOR_PARITY.md` are deliberately outside it: malformed extractor metadata, explicit resource ceilings, and comment/string token forms that fail closed rather than silently rewriting a selector. They remain executable `deliberate_safety_gap` entries in the corpus. `Options.PreferExtensions` is a documented Go-only preference, not a claim of pinned CLI equivalence.

## Validation commands

Run from a clean worktree:

```sh
gofmt -w internal/format/pinned_closure_test.go internal/conformance/manifest.go internal/conformance/manifest_test.go
git diff --check
go mod tidy -diff
go vet ./...
go run ./cmd/paritycheck
go test ./...
go test -race ./internal/format ./internal/cli ./pkg/ytdlp ./internal/protocol/hls ./internal/protocol/dash ./internal/protocol/ism
go test -run=^$ -fuzz='^FuzzEvaluateSelector$' -fuzztime=100x -parallel=1 ./internal/format
go test -run=^$ -fuzz='^FuzzParserParitySpans$' -fuzztime=100x -parallel=1 ./internal/format
go test -run=^$ -fuzz='^FuzzPrepareFormats$' -fuzztime=100x -parallel=1 ./internal/format
for goos in linux darwin windows; do for goarch in amd64 arm64; do CGO_ENABLED=0 GOOS=$goos GOARCH=$goarch go build ./...; done; done
docker build -f .github/python-free.Dockerfile -t ytdlp-go:format-selector-pinned-closure .
```

The Dockerfile explicitly asserts that neither `python` nor `python3` exists before executing `paritycheck` and `go test ./...`; it is the repository's Python-free Docker gate.

## Validation record

Initial validation was performed on 2026-07-29 at
`df5820721dead4469e770d8c8fe52be417af12f9`:

| Command | Result |
| --- | --- |
| `gofmt`, `git diff --check`, `go mod tidy -diff`, `go vet ./...` | Pass |
| `go run ./cmd/paritycheck` | Pass; 75 capabilities, zero temporary fallbacks |
| `go test ./... -count=1` | Pass |
| Focused race: format, CLI, product, HLS, DASH, ISM | Pass |
| `FuzzEvaluateSelector`, `FuzzParserParitySpans`, `FuzzPrepareFormats` at `100x`, one worker | Pass |
| `CGO_ENABLED=0 go build ./...` for Linux/Darwin/Windows × amd64/arm64 | Pass |
| `.github/python-free.Dockerfile` build | Pass; image `ytdlp-go:format-selector-pinned-closure` = `sha256:d5a079708738efbd5d7a8f9a3f9421b0e1abc64697f4eab664be201708ca933e` |

No Python oracle was run for the closure index. It consumes the previously
captured, committed CPython 3.12.13 fixtures, so this record makes no claim of
a new Python capture.
