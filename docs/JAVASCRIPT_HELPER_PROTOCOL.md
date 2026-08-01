# JavaScript Helper Protocol v1

The product talks to a separately supervised JavaScript helper over two
length-prefixed streams. Each frame is a four-byte big-endian length followed by
one JSON request or response. Frames are limited to 16 MiB.

The protocol is engine-neutral. A request supplies JavaScript source, an
optional explicit module set, an operation (`evaluate` or `call`), JSON
arguments, and budgets. The helper never resolves a module from the filesystem
or network. Its response contains either JSON output or one categorized error.

Version 1 enforces these hard ceilings before engine execution:

- 8 MiB script source;
- 64 modules and 8 MiB aggregate module source;
- 8 MiB serialized output;
- 512 MiB requested memory budget;
- 30 seconds requested wall time (untrusted requests);
- 60 seconds requested wall time (EJS preprocessing only). The supervisor
  validates the in-process `Trusted` flag, strips any caller-provided
  `trusted_wall_time_ms`, and mints the serialized grant exclusively for
  `operation=call, function="jsc"` requests. Callers cannot forge the grant;
  generic evaluate/call requests are bounded at 30 s regardless of flags.

Defaults are lower: 2 MiB source, 16 modules/2 MiB, 1 MiB output, 64 MiB
memory, and two seconds. The host may impose stricter limits.

The EJS challenge solver splits player processing into two bounded phases:
a preprocess phase (meriyah-based player parsing, up to 55 s wall time) and
a solve phase (transform execution, up to 10 s). Preprocessed players are
cached by SHA-256 in a bounded LRU (max 8 entries) that persists at the
client level across separate downloads. Concurrent requests for the same
uncached player are coalesced via a flight-owned singleflight: preprocessing
runs in a dedicated goroutine independent of any individual caller's context.
Every caller selects between flight completion and its own context. When all
waiters cancel, the shared preprocessing is canceled to avoid orphaned work;
when at least one waiter remains, preprocessing continues. The result is
cached atomically before the flight entry is removed.

Scripts are keyed by lowercase SHA-256. A long-lived helper may cache compiled
immutable programs by this hash, but each request receives a fresh runtime so
global mutations never cross requests. Cancellation or a helper fault causes
the host to terminate the helper process and discard its cache.

The Go helper starts with both `GOMEMLIMIT` and `runtime/debug.SetMemoryLimit`
set to the supervisor's configured process budget. Requests cannot ask for more
memory than that process budget. This is a Go-runtime memory ceiling rather than
an operating-system sandbox guarantee; abnormal exhaustion kills only the
helper and is reported by the supervisor as a helper crash. The process has a
minimal environment and the JavaScript runtime exposes no filesystem, network,
Node, browser, timer, or subprocess host functions.

Helper discovery is hardened by default: the supervisor accepts an explicit
absolute helper path or the helper beside the application executable and never
searches `PATH`. The selected file must be regular, non-symlink, executable,
owned by the current user, and not group/world-writable on Unix. Canonical Unix
parent directories must be owned by root or the current user and must not be
group/world-writable, except for root-owned sticky ancestors such as `/tmp`.
Those attributes and the opened-file identity are checked
immediately before constructing the process command. Embeddings that have a
release-artifact digest may supply `supervisor.Config.ExpectedHelperDigest` to
pin the helper's SHA-256 bytes during validation.

Portable Go process launch still resolves `Config.Path` after the verified file
handle is closed. A same-UID actor, or an actor able to replace the path after
validation on a platform without the Unix parent checks, can therefore race the
validation-to-exec window. The optional digest is not launch-bound and the public
`pkg/ytdlp` configuration does not populate it. This boundary claims safer
fail-closed discovery and pre-launch validation, not handle-atomic execution or
unconditional release identity.

The stable error codes distinguish invalid input, incompatible versions,
syntax and execution failures, missing functions, unsupported modules, timeout,
cancellation, input/output/memory limits, helper crashes, and protocol faults.
Diagnostics must never include script bodies, arguments, cookies, or URLs with
secret query values.

This boundary contains no Python dependency. Oracle generation for migration
fixtures remains outside the product and Python-free CI path.

The Phase 1 implementation embeds the official `yt-dlp-ejs` 0.8.0 core and
library JavaScript assets, not its Python wheel modules. Go verifies their
published SHA3-512 allowlist hashes and executes them inside the helper. Bundle
and corpus provenance is recorded in
`conformance/javascript/ejs-0.8.0/PROVENANCE.md`.
