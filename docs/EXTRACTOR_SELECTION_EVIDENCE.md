# Extractor selection evidence

## Attributable reference

The behavior is anchored to the read-only checkout
`yt-dlp/yt-dlp@aefce1eea4d0b6bab1ec2bd3beff09bff91a39c8`:

- `yt_dlp/options.py:401-410` defines `--use-extractors` and `--ies`,
  comma-separated values, the `all`/`default`/`end` aliases, and leading `-`
  exclusions.
- `yt_dlp/options.py:248-254` trims and drops empty comma fields while
  preserving repeated-option order.
- `yt_dlp/utils/_utils.py:5336-5363` applies each rule to an ordered set;
  matching is case-insensitive full-match regex, exclusions remove matching
  entries, and a valid zero-match regex is a no-op.
- `yt_dlp/YoutubeDL.py:933-944` places the disabled `UnsupportedURLIE` at the
  exact final `end` position for the `all` alias while `default` excludes it.

The pinned checkout was inspected read-only. No upstream Python was executed or
copied into the Go implementation.

## Go contract

`Request.ExtractorSelection` carries the typed boundary. The CLI splits each
occurrence on commas, trims surrounding whitespace, drops empty fields, and
preserves occurrence order. If the final rule list is empty, the typed API and
CLI both select the default registry order, matching the pinned
`allowed_extractors: opts.allowed_extractors or ['default']` fallback. The
product registry compiles non-empty rules against its concrete deterministic
registration order before creating a network transport.

Automatic root selection and every non-plugin `ExtractorKey` URL-result
re-entry use the same compiled order. This means a disabled extractor cannot be
reached through a transparent or non-transparent child result. Unknown explicit
extractor keys retain the existing safe `ErrUnsupported` failure. Signed
installed plugins remain explicit-only and can still be selected by `PluginID`.

The Go policy bounds rule count and byte sizes, rejects malformed regex syntax,
and preserves valid zero-match patterns as no-ops. The ordered `end` sentinel
stops selection at its exact rule position; it is not a global post-loop flag.
Listing/descriptions, force-generic, default-search, extractor-args, plugin
discovery, and extractor breadth are outside this increment.

## Evidence

Focused coverage is provided by:

- `internal/extractor/selection_test.go` for order, aliases, case,
  exclusions, zero-match rules, `end`, disabled re-entry, explicit-only
  plugins, and safe unknown keys.
- `internal/cli/extractor_selection_test.go` for CLI wiring and malformed-rule
  preflight.
- `pkg/ytdlp/extractor_selection_test.go` for product root routing, transparent
  and non-transparent re-entry, playlist reuse, cancellation, preflight, and
  concurrent clients.
