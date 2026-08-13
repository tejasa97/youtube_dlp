# ADR 0001: Regular-expression compatibility

Status: Accepted

## Context

Go's RE2-based `regexp` package deliberately omits lookaround, backreferences,
conditionals, and several Python `re` details used by some yt-dlp extractors.
Silently simplifying those expressions would produce extraction errors that
look like site breakage.

## Decision

Extractor regular expressions use the bounded compatibility interface. The
standard library engine is used when its syntax and semantics are sufficient.
Patterns requiring the documented Python-compatible subset use the bounded
adapter with source, translation, input, attempt, and execution budgets.

Unsupported syntax is a categorized, testable error. Patterns are never
silently rewritten unless deterministic conformance evidence establishes the
rewrite.

## Consequences

Simple expressions retain RE2's linear-time safety. Backtracking-compatible
expressions cross an explicit denial-of-service boundary and remain subject to
the limits documented by the compatibility package and capability manifest.
