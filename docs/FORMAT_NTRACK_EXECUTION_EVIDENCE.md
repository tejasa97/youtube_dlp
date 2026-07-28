# N-track execution evidence

Reference: yt-dlp commit `aefce1eea4d0b6bab1ec2bd3beff09bff91a39c8`

Branch: `codex/format-ntrack-execution`

Depends on: PR #147 (`codex/format-selector-planner-parity`, merged)

## Architecture

```mermaid
flowchart TD
  A[planPreparedFormats] --> B[validateOutputPlans]
  B --> C[printFilenameForPlan / outputPlanDestination]
  C --> D[downloadSelections]
  D --> E{track count}
  E -->|1| F[downloadSelection direct]
  E -->|2 SABR| G[downloadYouTubeSABRPair]
  E -->|2 live-from-start| H[downloadYouTubeLivePair]
  E -->|2..16 mergeable| I[downloadAndMergeTracks]
  I --> J[bounded concurrent downloads]
  J --> K[ffmpeg.MergeTracks]
  K --> L[atomic publication]
```

## Extension resolution

Execution consumes planner-owned metadata:

1. **Default:** `plan.Metadata.ext` from PR 5 `planMetadataFor`.
2. **Override:** when `Request.MergeOutputFormat` is set, recompute via
   `format.CompatibleExtensionForSelections` with parsed slash-separated
   preferences (product-layer override until CLI PR 9).
3. **Single-track:** metadata or track `Ext` via `plannedOutputExtension`.

The interim duplicate `get_compatible_ext` implementation in `pkg/ytdlp` was
removed; execution delegates to `internal/format`.

## FFmpeg input and map ordering

Matches pinned `FFmpegMergerPP`:

1. `-i <path>` in `OutputPlan.Tracks` order (= `requested_formats` order).
2. Per input: `-map <i>:a:0` then `-map <i>:v:0` when present.
3. `-c copy` plus HLS AAC `aac_adtstoasc` when required.

## Concurrency

- Maximum tracks: `format.MaxMergeTracks` (16).
- Download concurrency: 4 workers with semaphore.
- Events serialized through `lockedEventSink`.

## Cancellation and cleanup

- First track failure cancels siblings; root error preserved.
- Private workspace `.ytdlp-formats-*` removed on all paths.
- No partial destination publication on failure or cancellation.

## Byte accounting

- Per-track download events report individual track bytes.
- Successful merge `Result.Bytes` equals the published file size.

## Merge-output-format

- `Request.MergeOutputFormat` (`mp4/mkv`) overrides planner metadata for
  destination extension only.
- `Request.PreferFreeFormats` remains planner-owned via `Metadata.ext`.

## Platform verification

Verified locally on darwin/arm64:

- `go test ./internal/format ./internal/media/ffmpeg ./internal/downloader ./pkg/ytdlp`
- race tests on ffmpeg, downloader, ytdlp
- 20× cancellation/cleanup repetition
- `go vet`, `go run ./cmd/paritycheck`
- `go test ./...`
- cross-compilation linux/darwin/windows amd64/arm64
- `docker build -f .github/python-free.Dockerfile`

## Remaining gaps

- SABR and YouTube live-from-start remain specialized 2-track paths.
- PR 7 multi-output `Result` semantics and PR 8 postprocessor lifecycle not
  pulled forward.
- CLI `--merge-output-format` exposure remains in PR 9.
