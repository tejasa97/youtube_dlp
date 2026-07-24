# Native Safari cookie import

The Go client accepts `CookiesFromBrowser: "safari"` and the CLI accepts
`--cookies-from-browser safari` on macOS. The two standard
`Cookies.binarycookies` locations are checked in the same order as the pinned
reference. `safari:/absolute/path` and `safari:~/path` select an explicit
database.

The native parser covers the big-endian database page table, little-endian page
and record structures, secure flags, bounded UTF-8 strings, and Mac absolute
timestamps. Page, record, file, cookie, and field limits are enforced with
checked bounds. Structurally corrupt framing invalidates the result; a
trustworthily framed but invalid cookie is counted and skipped.

Filesystem import rejects relative paths, symlinks, non-regular files,
oversized inputs, identity changes between lookup and open, and size changes
during reading. Reads and parsing honor cancellation. Public errors and events
do not include cookie data or database paths.

Deterministic tests never read the developer's Safari profile. The corpus is
attributed in `conformance/cookies/safari/PROVENANCE.md`. Safari browser
fingerprint impersonation, iOS profile access, and real-profile observation
are not claimed.
