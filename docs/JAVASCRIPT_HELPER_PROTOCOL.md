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
- 30 seconds requested wall time.

Defaults are lower: 2 MiB source, 16 modules/2 MiB, 1 MiB output, 64 MiB
memory, and two seconds. The host may impose stricter limits.

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

The stable error codes distinguish invalid input, incompatible versions,
syntax and execution failures, missing functions, unsupported modules, timeout,
cancellation, input/output/memory limits, helper crashes, and protocol faults.
Diagnostics must never include script bodies, arguments, cookies, or URLs with
secret query values.

This boundary contains no Python dependency. Oracle generation for migration
fixtures remains outside the product and Python-free CI path.
