# Phase 3 native netrc evidence

The native package provides bounded in-memory parsing, secure file loading,
and context-aware lookup without Python, a shell, or an external command.

Supported semantics:

- `machine` and `default` entries; the final duplicate definition wins.
- `login`/`user`, `account`, and required `password`, including empty quoted
  values.
- Single/double quotes, backslash escaping outside single quotes, comments at
  token boundaries, and tokens spanning ordinary whitespace.
- Bounded `macdef` bodies are ignored and never tokenized, stored, or executed.
- Exact extractor aliases, canonical lower-case DNS names, IDNA Lookup-profile
  conversion, normalized IP literals, explicit host:port preference, host-only
  fallback, then `default`.
- Strict file, input, entry, token, macro-count, and macro-byte limits.
- Redacted credential formatting and categorized diagnostics that never include
  token contents.
- Unix owner-only permissions, current-user ownership, one hard link, and
  no-follow opening. Windows rejects reparse points and hard links, requires
  the current user as owner, and permits effective allow ACEs only for that
  owner, LocalSystem, and Administrators.

Deliberate deviations:

- Macros are ignored rather than retained because the product has no netrc
  macro execution feature and must not accidentally introduce one.
- File safety is enforced for every loaded path, while Python netrc historically
  applies its permission check only to the implicit default file.
- Host lookup adds explicit IDNA, IP, and port canonicalization; yt-dlp usually
  looks up extractor-defined machine aliases directly.
- A `#` starts a comment only at a token boundary. Literal `#` inside a token or
  quoted value is preserved.
