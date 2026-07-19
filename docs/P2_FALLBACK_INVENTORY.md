# Phase 2 fallback inventory

Review baseline: 2026-07-18; helper-discovery policy updated 2026-07-19

The validated source of truth is
`conformance/fallback_inventory.yaml`; `go run ./cmd/paritycheck` rejects an
unobservable, ownerless, unscheduled, Python-backed, or silent fallback. This
document explains that inventory for reviewers.

There is no production Python fallback and no temporary compatibility fallback
at the Phase 2 baseline. Unknown impersonation profiles, unsupported manifest
features, unavailable JavaScript helpers, unsupported parser syntax, missing
plugins, unavailable plugin sandboxes, unproved Windows pack ownership, and
missing external media tools fail explicitly with categorized errors. Plugin
extractors require exact `PluginID` selection; they are not a hidden fallback
for a failed native extractor. Update verification never falls back to an
older, unsigned, wrong-channel, or wrong-platform artifact.

The following uses of the word “fallback” are not temporary execution bridges:

| Surface | Meaning | Owner | Milestone |
| --- | --- | --- | --- |
| format selector `/` | user-requested format-choice operator | compatibility | permanent syntax |
| JavaScript helper discovery | configured path, then executable directory; `PATH` discovery was removed by `49c3440` | runtime | permanent fail-closed discovery policy |
| extractor URL normalization | a site may synthesize its canonical API URL when optional webpage data is absent | extractor owner | corpus-specific behavior |

Any future temporary fallback must emit an observable capability-decision
event, name its native target, include a non-secret reason, identify an owner,
and specify a removal milestone in this file and the parity manifest. Silent
fallback and any Python-backed fallback are prohibited.
