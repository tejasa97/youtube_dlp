# Fallback inventory

The validated source of truth is `conformance/fallback_inventory.yaml`.
`go run ./cmd/paritycheck` rejects an unobservable, Python-backed, or silent
fallback.

There is no production Python fallback and no temporary compatibility
execution bridge. Unknown impersonation profiles, unsupported manifest
features, unavailable JavaScript helpers, unsupported parser syntax, missing
plugins, unavailable plugin sandboxes, unproved Windows pack ownership, and
missing external media tools fail explicitly with categorized errors. Plugin
extractors require exact `PluginID` selection; they are not a hidden fallback
for a failed native extractor. Update verification never falls back to an
older, unsigned, wrong-channel, or wrong-platform artifact.

The following uses of the word “fallback” are not temporary execution bridges:

| Surface | Meaning |
| --- | --- |
| format selector `/` | User-requested format-choice operator |
| JavaScript helper discovery | Absolute configured path, then executable directory; `PATH` discovery is disabled and the validation-to-exec pathname limitation is documented |
| extractor URL normalization | A site may synthesize its canonical API URL when optional webpage data is absent |

Temporary execution bridges, silent fallback, and Python-backed fallback are
not accepted compatibility behavior.
