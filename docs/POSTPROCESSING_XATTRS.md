# Xattrs post-processing boundary

`--xattrs` and `--xattr` write a fixed, bounded metadata mapping to each
entry lifecycle's final media file. The supported names are
`user.xdg.referrer.url`,
`user.dublincore.title`, `user.dublincore.date`,
`user.dublincore.contributor`, `user.dublincore.format`, and
`user.dublincore.description`; on Darwin the reference
`com.apple.metadata:kMDItemWhereFroms` plist is also written. Dates are
normalized from `YYYYMMDD` to `YYYY-MM-DD`.

The stage accepts at most seven names, 128-byte names, 4 KiB values, and 16 KiB
total payload. Empty fields are omitted. Media paths must be regular,
non-symlink files whose parent chain is confined beneath the output root.
Windows and unsupported filesystems fail closed with an unsupported error;
there is no external xattr-tool or arbitrary-name fallback.

Existing mapped attributes are snapshotted before writes. A write failure,
completion-handler failure, or entered cancellation restores the prior values;
the surrounding output transaction restores the media destination and leaves
the download archive unchanged. Chapter artifacts and the later playlist
`pl_video` concat result are intentionally outside this per-entry xattr scope;
they are not claimed to receive xattrs. Platform capability tests are
build-tagged for Unix round trips and Windows fail-closed behavior.
