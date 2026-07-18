# Phase 3 privacy-safe measurement

Status: implemented foundation

Date: 2026-07-19

Telemetry is disabled unless a caller constructs `ytdlp.TelemetryCollector` and
passes it with `ytdlp.WithTelemetryCollector`. The client records exactly one
bounded extraction outcome for each `Run`, including operations that fail
before routing. Cancellation does not erase the observation from the declared
denominator.

The CLI remains disabled by default. `--telemetry-json` enables a single-run
snapshot on stdout so an operator can merge snapshots outside the process. It
is mutually exclusive with `--print-json`, records failed operations before
the CLI returns, and never writes a URL or error message to the snapshot.

The public collector accepts a fixed extractor allowlist. Any extractor not in
that set, including a newly installed plugin, maps to the literal `unknown`
bucket. The only public capability is `extract`, and outcomes are limited to
`success`, `error`, `unsupported`, and `fallback`. No method accepts a URL,
path, query, title, username, header, cookie, credential, arbitrary label, or
error string.

Snapshots are timestamp-free, strictly decoded, deterministically ordered, and
bounded by byte and cell limits. Merge is atomic. Overflow and counter
saturation stay in the coverage denominator; a saturated calculation is marked
inexact and cannot support Gate G3. `Coverage.BasisPoints` is the floor of
successful operations divided by all observations, including errors,
unsupported inputs, fallbacks, and overflow.

This package does not select a destination, retention period, identifier set,
or network exporter. Those are deployment policy and must remain explicit and
opt-in. Aggregate usage can still be sensitive, so applications should retain
the minimum window required and authenticate snapshots accepted across trust
boundaries.
