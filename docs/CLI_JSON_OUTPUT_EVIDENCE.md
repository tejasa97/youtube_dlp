# CLI JSON dump evidence

## Reference provenance

Behavior was compared read-only with `yt-dlp/yt-dlp` at pinned commit
`aefce1eea4d0b6bab1ec2bd3beff09bff91a39c8`.

The relevant reference paths are:

- `yt_dlp/options.py`: `-j`/`--dump-json` and
  `-J`/`--dump-single-json`;
- `yt_dlp/__init__.py`: implicit quiet and simulation resolution; and
- `yt_dlp/YoutubeDL.py`: per-video forced JSON and whole-result JSON.

The reference establishes that both public dump modes are quiet and simulate
unless explicitly overridden. Per-video mode emits leaf video objects while
single mode emits the complete result, including a playlist object.

## Go behavior

`-j`/`--dump-json` emits one compact JSON line for each successfully processed
leaf video, recursively preserving playlist order and omitting playlist
containers, empty playlists, archive matches, and match-filter skips.

`-J`/`--dump-single-json` emits exactly one compact JSON line containing the
complete URL result. When both modes are requested, per-video lines are
followed by the whole result.

Both modes:

- imply `Request.Simulate` only when neither `--simulate` nor
  `--no-simulate` was explicit;
- imply quiet only when neither `--quiet` nor `--no-quiet` was explicit;
- suppress media, subtitle, postprocessor, and archive output in their default
  simulation mode;
- download normally under `--no-simulate`;
- retain `--progress-json` on stderr when explicitly requested;
- reject sharing stdout with `--telemetry-json`; and
- propagate cancellation and output-writer failures.

The pre-existing hidden compatibility flag `--print-json` retains its
ytdlp-go whole-result, non-simulating behavior. If it is combined with a public
dump mode, the public dump mode takes precedence rather than duplicating the
same JSON again.

## Known deviation

Per-video JSON is buffered in the product result and emitted in deterministic
playlist order after the complete operation returns. The pinned implementation
can stream each video's JSON earlier while later playlist entries are still
processing. JSON content and ordering are covered; inter-entry output timing is
not claimed.

## Automated evidence

- `internal/cli.TestRunDumpJSONModesSimulateQuietlyAndNoSimulateDownloads`
- `internal/cli.TestRunDumpJSONSimulationSuppressesRelatedFiles`
- `internal/cli.TestRunDumpJSONExplicitNoQuietAndCombinedModes`
- `internal/cli.TestRunDumpJSONFromConfigurationHonorsCommandLineNoSimulate`
- `internal/cli.TestWriteVideoJSONLinesFlattensPlaylistsAndHandlesFailures`
- `internal/cli.FuzzWriteJSONLine`
- `internal/cli.TestRunTelemetryJSONSuccessFailureAndConflict`
- `internal/cli.TestRunWalkingSkeletonAndJSONSeparation`
