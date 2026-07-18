# Phase 1 upstream-delta replay

## Scope and method

The replay inventories 114 upstream commits across eight consecutive historical
weeks ending at the pinned reference. A checked-in machine-readable inventory
and its provenance live under `conformance/upstream-delta/`. The classifier is a
bounded Go development tool under `cmd/deltareplay`; it resolves the requested
commit, derives the 56-day window from its committer timestamp, and labels each
commit from its subject and changed paths.

The categories are deliberately aligned with P1-11: extractor, transport,
challenge, CLI, downloader, postprocessor, and other. A commit may affect more
than one category.

| Week | UTC window start | Commits | Extractor | Transport | Challenge | CLI | Downloader | Postprocessor | Other |
| ---: | --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: |
| 1 | 2026-05-19 | 6 | 3 | 0 | 3 | 0 | 0 | 0 | 3 |
| 2 | 2026-05-26 | 0 | 0 | 0 | 0 | 0 | 0 | 0 | 0 |
| 3 | 2026-06-02 | 10 | 5 | 0 | 1 | 1 | 0 | 1 | 4 |
| 4 | 2026-06-09 | 30 | 16 | 3 | 0 | 5 | 6 | 2 | 3 |
| 5 | 2026-06-16 | 11 | 9 | 0 | 0 | 0 | 0 | 0 | 2 |
| 6 | 2026-06-23 | 27 | 19 | 0 | 1 | 2 | 1 | 0 | 6 |
| 7 | 2026-06-30 | 25 | 17 | 1 | 0 | 4 | 1 | 0 | 7 |
| 8 | 2026-07-07 | 5 | 4 | 0 | 2 | 0 | 1 | 0 | 0 |
| **Total labels** | | **114 commits** | **73** | **4** | **7** | **12** | **9** | **3** | **25** |

An already-built classifier completed warm local runs in 0.05–0.06 seconds;
the first cold run completed in 0.39 seconds. Human review focused on every
pilot-relevant delta and representative cross-cutting changes, rather than
pretending that automated labels prove semantic absorption.

## Replay findings

| Upstream change(s) | Classification | Pilot boundary exercised | Required Go-port response |
| --- | --- | --- | --- |
| `aefce1eea` HLS empty test fragment list | downloader | HLS parser → fragment plan | Preserve an empty plan as an explicit categorized error and add a no-panic regression; no boundary change. |
| `8c1f07d81` YouTube live adaptive formats | extractor, challenge | YouTube normalized formats → HLS/DASH protocols | Existing format normalization and protocol dispatch accept adaptive/live manifest URLs; retain a pinned live fixture. |
| `59d9ae606`, `1328586f7` client versions and visionOS client | extractor, challenge | webpage/player configuration | Prefer response/page-provided client configuration; a new client profile is data plus fixtures, not a transport rewrite. VisionOS is outside the pilot corpus. |
| `7fdc46d01` PO-token sanitization | extractor, challenge | challenge/token policy | The selected EJS corpus is unaffected. PO-token provider breadth remains an explicit YouTube deviation, with no Python bridge. |
| runtime-version changes `e534a3261`, `b536d72c8`, `98e42eb04` | challenge | isolated JavaScript engine contract | The embedded Go engine has no Deno/Node/Bun runtime dependency. Engine conformance remains fixture-driven. |
| YouTube tab pagination/metadata series `b23046bbc` through `a75ba96fa` | extractor | lazy continuation entry sequence | Renderer-specific traversal changes remain extractor-local; pagination, cancellation, loop detection, and nested playlist boundaries remain reusable. |
| `aaa1c7895` Twitch dead rechat removal | extractor | Twitch pilot | No edit: Phase 1 Twitch does not claim subtitles/chat. Metadata, token, and live HLS behavior stay extractor-local. |
| SoundCloud series including `8bdfbfd44` | extractor | API/pagination extractor | Replayed into the Phase 1 SoundCloud lane as API response fixtures and lazy playlist expectations. |
| Instagram series `f49b551a0`, `8b8e3e3cb`, `ac4c955ea` | extractor, transport | impersonation, cookie/auth categorization | Replayed into the anti-bot lane; native fallback must be explicit and invalid cookies must become authentication failures. |
| `272657252` external curl redirect-cookie leak | transport, downloader | shared cookie jar and request director | Product downloads use the scoped Go cookie jar rather than command-line cookie headers. Cross-host redirect scoping belongs in shared transport tests. |
| `a6791415e` ffmpeg direct-merge headers | downloader, postprocessor | supervised argument construction | HTTP header propagation is an ffmpeg operation input, not downloader-specific shell construction; arguments remain redacted and shell-free. |
| `e85da3b98` FFmpeg metadata language conversion | postprocessor | typed postprocessor operation | Metadata writing is outside the minimal merge/remux pilot and remains a named deviation; adding it does not alter process supervision. |
| `b6590aaa1` safe `--write-link` output and safe-extension changes | CLI | CLI/output confinement | Link-writing is outside the Phase 1 CLI corpus. Existing output confinement remains the reusable home for future implementation. |

## Churn and maintenance assessment

The sample is extractor-heavy: 73 extractor labels across 114 commits. The
important maintenance result is that the reviewed changes map to existing
boundaries—extractor-local traversal/configuration, shared transport/cookies,
challenge execution, protocol downloaders, output confinement, or typed
postprocessor operations. None requires embedding or invoking Python.

The representative replay touches the following Go areas without replacing
their contracts:

- extractor fixtures and site-local parsing for YouTube, Twitch, SoundCloud,
  and Instagram;
- shared HLS/fragment error handling;
- shared cookie scoping and signed-URL redaction;
- JavaScript corpus/profile data;
- typed ffmpeg operation inputs.

Fixture churn is concentrated in platform-specific corpora. Cross-cutting
changes require reusable tests in one shared package rather than copies in each
extractor. This is the maintenance model Phase 2 should retain.

## Explicit limitation

This is a backward historical replay because eight future weeks after the
2026-07-14 pin do not yet exist. It demonstrates absorption over a real
eight-week change sample, but it cannot truthfully claim observation of future
post-pin changes. Re-running the same inventory tool after 2026-09-08 is the
bounded follow-up needed to satisfy the original forward-window wording.
