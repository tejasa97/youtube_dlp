# Typed fixup policy

This stack boundary adds the bounded `--fixup` policy surface. Accepted values
are `never`, `ignore`, `warn`, `detect_or_warn`, and `force`; the CLI default is
`detect_or_warn`. An API request with an empty policy leaves automatic fixup
disabled for compatibility with the existing zero value.

Detection is closed over the existing typed operations: MPEG-TS containers use
`ffmpeg.FixupMPEGTS`, and AAC audio in an `.m4a` uses
`ffmpeg.FixupM4AAudio`. No arbitrary ffmpeg arguments, codec strings, shell
commands, or postprocessor-argument escape hatch are accepted.

`warn` reports a detected repair without mutating the file. `detect_or_warn`
applies a detected typed repair and warns when inspection is unavailable.
`force` fails closed if inspection fails or no supported repair is detectable.
Each in-place mutation snapshots the media in the active output transaction;
ffmpeg itself writes through its private atomic temporary output, so a failed
repair leaves the original artifact available for rollback.

The registered product contract covers a real generated MPEG-TS repair, a
byte-preserving `warn` run, unavailable inspection/tooling, fail-closed
`force`, entered cancellation, and an injected ffmpeg failure after the typed
operation starts. Cancellation and failure restore a pre-existing destination,
leave the download archive unchanged, remove private temporary/partial files,
and expose only bounded error/event text without source paths or credentials.
