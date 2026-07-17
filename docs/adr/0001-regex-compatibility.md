# ADR 0001: Regular-expression compatibility

Status: Accepted for Phase 0

## Context

Go's RE2-based `regexp` package deliberately omits lookaround, backreferences,
conditionals, and several Python `re` details used by some yt-dlp extractors.
Silently simplifying those expressions would produce extraction errors that
look like site breakage.

## Decision

All extractor regexes will go through a small compatibility interface. The
standard library engine is the default and must be used when a pattern compiles
and its semantics are sufficient. A later risk pilot may add a bounded fallback
engine for patterns requiring backtracking features. Fallback evaluation must
accept a context/deadline and an input-size limit.

Unsupported syntax is a categorized, testable error. Patterns are never
silently rewritten unless a conformance fixture proves the rewrite equivalent.
Extractor migrations will inventory required constructs so the fallback
dependency is justified by actual coverage.

## Consequences

Simple extractors retain RE2's linear-time safety. Complex patterns incur an
explicit compatibility and denial-of-service review. Phase 1 owns the fallback
engine experiment and differential corpus.
