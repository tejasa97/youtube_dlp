# Format-selector CLI parity evidence

This change exposes planner options through the CLI without placing IO in
`internal/format`. It is based on the pinned yt-dlp reference commit
`aefce1eea4d0b6bab1ec2bd3beff09bff91a39c8`.

Implemented flags are `--video-multistreams`,
`--no-video-multistreams`, `--audio-multistreams`,
`--no-audio-multistreams`, `--format-sort-force`, `--S-force`,
`--no-format-sort-force`, `--format-sort-reset`,
`--prefer-free-formats`, `--no-prefer-free-formats`,
`--allow-unplayable-formats`, `--no-allow-unplayable-formats`,
`--all-formats`, `--check-formats`, `--check-all-formats`,
`--no-check-formats`, and `--merge-output-format`.

Format sort fields are accumulated in invocation order with later `-S` values
given precedence, and `--format-sort-reset` clears prior values. Boolean flags
are parsed in the combined config/CLI order, so the last occurrence wins.
Merge container preferences are validated and selected only by the merged PR 6
planner/executor APIs; the CLI does not perform container compatibility logic.

Availability probing is an operation-scoped adapter injected through
`format.EvaluationOptions`. It has no format-package IO, uses a canonical
opaque SHA-256 cache key over URL, protocol, and headers, caps probes at the
same 4096-entry normalized-format bound, caps each probe at 2 MiB and the
operation at 16 MiB, uses five-second probes and at most five redirects, and
never emits URLs or credentials in errors. Direct media uses a one-byte range
GET. HLS resolves a master playlist to a media playlist then probes its first
segment; DASH and ISM probe an initial resolved segment. Redirects strip
explicit Authorization, Proxy-Authorization, and Cookie headers across origin
changes while allowing the operation cookie jar to apply destination-scoped
cookies.

Modes are Auto (default; only `has_drm`/`__needs_testing` candidates), None,
Selected, and All. Explicit allow-unplayable disables Auto probing. Ordinary
probe errors and internal per-probe deadlines make a candidate unavailable;
parent cancellation and `ErrFormatCheckLimit` abort selection. `--check-all-formats`
walks the canonical prepared objects before planning and reuses the same cache.

Interactive `-f -` is requested after extraction, accepts an empty default
selector, retries bounded syntax/no-match failures, and shares one bounded
stdin coordinator with interactive match filters. Prompts are written solely
to stderr; `--progress-json` rejects either interactive mode so stdout JSON is
not polluted.

PR 8 remains the final integration gate for the multi-output lifecycle. This
change deliberately does not alter PR 7 transaction/public-result behavior.
