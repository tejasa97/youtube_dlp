# Phase 2 fallback inventory

Review baseline: 2026-07-18

There is no production Python fallback and no temporary compatibility fallback
at the Phase 2 baseline. Unknown impersonation profiles, unsupported manifest
features, unavailable JavaScript helpers, unsupported parser syntax, missing
plugins and missing external media tools fail explicitly with categorized
errors.

The following uses of the word “fallback” are not temporary execution bridges:

| Surface | Meaning | Owner | Milestone |
| --- | --- | --- | --- |
| format selector `/` | user-requested format-choice operator | compatibility | permanent syntax |
| JavaScript helper path search | configured path, executable directory, then `PATH` discovery of the same pure-Go helper | runtime | permanent discovery policy |
| extractor URL normalization | a site may synthesize its canonical API URL when optional webpage data is absent | extractor owner | corpus-specific behavior |

Any future temporary fallback must emit an observable capability-decision
event, name its native target, include a non-secret reason, identify an owner,
and specify a removal milestone in this file and the parity manifest. Silent
fallback and any Python-backed fallback are prohibited.
