# Format-planner oracle provenance

The planner oracle in `internal/format/testdata/planner_conformance.json` was
captured from the
read-only yt-dlp reference checkout at commit
`aefce1eea4d0b6bab1ec2bd3beff09bff91a39c8` using CPython 3.12.13.

The maintainer-only generator is `capture_oracle.py`. It verifies the reference
checkout SHA before importing yt-dlp and records the interpreter version in the
fixture. It exercises `YoutubeDL.build_format_selector`, its merge behavior,
the pinned `FormatSorter`, and `get_compatible_ext`.

Generation command:

```sh
python3 \
  conformance/format_planner/capture_oracle.py \
  --reference /path/to/yt-dlp-reference \
  --commit aefce1eea4d0b6bab1ec2bd3beff09bff91a39c8 \
  --output internal/format/testdata/planner_conformance.json
```

Artifact SHA-256:

| Artifact | SHA-256 |
| --- | --- |
| `internal/format/testdata/planner_conformance.json` | `feff70dccfb4eeaa4710cb1999b8ecc12b894b55b83fc433c1aedcad44ad24df` |

Python is never invoked by Go tests, builds, Docker, or production. The
committed JSON fixture is the complete verification input.
