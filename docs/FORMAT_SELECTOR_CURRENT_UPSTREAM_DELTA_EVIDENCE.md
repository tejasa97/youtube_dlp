# Current-upstream format-selector delta

Frozen target: `yt-dlp/yt-dlp@fdcc954df4955267ec1627cbeb347b661a110e7c`.
This evidence establishes parity only through that SHA; it does not claim a
rolling-master guarantee. The committed record is
`conformance/upstream-delta/format-selector-current.json` and is checked
offline by `internal/upstreamdelta.TestFormatSelectorCurrentUpstreamDelta` and
`go run ./cmd/paritycheck`.

| Source | Pinned blob | Target blob | Result |
| --- | --- | --- | --- |
| `yt_dlp/YoutubeDL.py` | `a2675bd44d9e7ff128bf93f780b62b8eab2b20bd` | same | unchanged |
| `yt_dlp/options.py` | `b46dba20f24c69ef479d388c9a71ca04fd12ef3c` | same | unchanged |
| `yt_dlp/utils/_utils.py` | `a791556eb55c84633d6774a0226b246114c7c37f` | same | unchanged |
| `test/test_YoutubeDL.py` | `0f84d7e1694b56f2db5ca214406fceab151a9f0a` | same | unchanged |

Maintainer-only inspection found merge-base
`aefce1eea4d0b6bab1ec2bd3beff09bff91a39c8` and seven commits ahead. Every
changed path is classified in the artifact: extractor paths are
`extractor_input_adjacent_outside_selection_semantics`; README and changelog
paths are `unrelated`. The YouTube adaptive-fragment and player-client patches,
Vimeo client changes, Instagram logged-in extraction, and Apple extractor
rework affect input inventory, metadata, or availability only. They do not
touch the selector/filter/sorter/planner/CLI normative sources above.

Therefore no product-code change or fixture recapture is warranted. The
closure matrix and marker guard remain the executable evidence for unchanged
normative behavior. No current selector gap was identified. Extractor, SABR,
and client-policy behavior is outside this selector claim.
