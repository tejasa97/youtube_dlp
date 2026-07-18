# Upstream-delta replay provenance

`inventory.json` is a deterministic inventory of the eight consecutive
seven-day windows ending at the pinned reference commit
`yt-dlp/yt-dlp@aefce1eea4d0b6bab1ec2bd3beff09bff91a39c8` (committer timestamp
2026-07-14T14:14:20Z). It was generated from the read-only local checkout with:

```text
go run ./cmd/deltareplay \
  -reference /Users/tejas/projects/yt-dlp-reference \
  -commit aefce1eea4d0b6bab1ec2bd3beff09bff91a39c8
```

The checked-in artifact contains commit hashes, committer timestamps, subjects,
changed paths, week allocation, and deterministic path/subject classifications.
It contains no upstream source contents. Categories are multi-valued, so their
counts can exceed the 114 unique commits.

The Phase 1 plan originally asked for eight weeks *after* the pinned commit.
Only four days after that commit existed when the replay was performed on
2026-07-18. The evidence therefore uses the immediately preceding eight weeks,
2026-05-19T14:14:20Z through 2026-07-14T14:14:20Z. This preserves an actual
eight-week maintenance sample without inventing future changes. The same Go
tool can produce a forward-window refresh once that history exists.

The inventory has no runtime or build-time relationship to the reference
checkout. Product builds do not invoke Git, Python, or this developer command.
